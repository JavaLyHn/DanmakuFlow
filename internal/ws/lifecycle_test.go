package ws

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/JavaLyHn/DanmakuFlow/internal/model"
)

type fakeProducer struct {
	input     chan *sarama.ProducerMessage
	failures  chan *sarama.ProducerError
	successes chan *sarama.ProducerMessage
	release   chan struct{}
	count     int
	fail      bool
}

func newFakeProducer() *fakeProducer {
	return &fakeProducer{input: make(chan *sarama.ProducerMessage, 16), failures: make(chan *sarama.ProducerError, 1), successes: make(chan *sarama.ProducerMessage), release: make(chan struct{})}
}
func (p *fakeProducer) Input() chan<- *sarama.ProducerMessage     { return p.input }
func (p *fakeProducer) Errors() <-chan *sarama.ProducerError      { return p.failures }
func (p *fakeProducer) Successes() <-chan *sarama.ProducerMessage { return p.successes }
func (p *fakeProducer) AsyncClose() {
	go func() {
		<-p.release
		close(p.input)
		for range p.input {
			p.count++
		}
		if p.fail {
			p.failures <- &sarama.ProducerError{Err: errors.New("write failed")}
		}
		close(p.failures)
		close(p.successes)
	}()
}

func testManager(p *fakeProducer, likes func(context.Context, string, string, uint64) error) *Manager {
	return newManager("test", p, managerIO{publish: func(context.Context, string, []byte) error { return nil }, likes: likes, stats: func(context.Context, string, int) (model.StatsData, error) { return model.StatsData{}, nil }, subscribe: func(ctx context.Context, _ string, _ func([]byte)) { <-ctx.Done() }, close: func() error { return nil }})
}
func TestShutdownDrainsAcceptedAndRejectsNewWork(t *testing.T) {
	p := newFakeProducer()
	close(p.release)
	var got uint64
	m := testManager(p, func(_ context.Context, _, _ string, n uint64) error { got += n; return nil })
	go m.Start()
	c := NewClient(1, "test", "last-room", nil)
	if !m.RegisterClient(c) {
		t.Fatal("register")
	}
	m.AddLikes("last-room", 7)
	m.UnregisterClient(c)
	for i := 0; i < 5; i++ {
		if !m.Submit(&model.WsPacket{Type: model.TypeDanmaku, RoomID: "last-room"}) {
			t.Fatal("submit")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got != 7 || p.count != 5 {
		t.Fatalf("likes=%d messages=%d", got, p.count)
	}
	if m.RegisterClient(c) || m.Submit(&model.WsPacket{}) || m.AddLikes("late", 1) {
		t.Fatal("accepted after stop")
	}
}
func TestShutdownTimeoutCanRetryAndReportsProducerFailure(t *testing.T) {
	p := newFakeProducer()
	p.fail = true
	m := testManager(p, func(context.Context, string, string, uint64) error { return nil })
	go m.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", err)
	}
	close(p.release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := m.Shutdown(ctx2); err == nil {
		t.Fatal("producer failure hidden")
	}
}
func TestShutdownProducerTimeoutThenSuccessfulRetry(t *testing.T) {
	p := newFakeProducer()
	m := testManager(p, func(context.Context, string, string, uint64) error { return nil })
	go m.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", err)
	}
	close(p.release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := m.Shutdown(ctx2); err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(ctx2); err != nil {
		t.Fatalf("repeated shutdown: %v", err)
	}
}
func TestConcurrentUnregisterShutdown(t *testing.T) {
	p := newFakeProducer()
	close(p.release)
	m := testManager(p, func(context.Context, string, string, uint64) error { return nil })
	go m.Start()
	c := NewClient(1, "test", "room", nil)
	m.RegisterClient(c)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.UnregisterClient(c)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}
func TestShutdownRetainsFailedLikesForRetry(t *testing.T) {
	p := newFakeProducer()
	close(p.release)
	attempt := 0
	var token string
	m := testManager(p, func(_ context.Context, _ string, id string, _ uint64) error {
		attempt++
		if token != "" && token != id {
			t.Fatal("retry changed idempotency token")
		}
		token = id
		if attempt == 1 {
			return errors.New("redis unavailable")
		}
		return nil
	})
	go m.Start()
	m.AddLikes("empty", 3)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err == nil {
		t.Fatal("lost likes error")
	}
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestShutdownJoinsAcceptedCallbackBeforeClosingQueue(t *testing.T) {
	p := newFakeProducer()
	close(p.release)
	m := testManager(p, func(context.Context, string, string, uint64) error { return nil })
	go m.Start()
	entered, release := make(chan struct{}), make(chan struct{})
	const action = 98127
	RegisterAction(action, func(_ *Client, m *Manager, _ []byte) {
		close(entered)
		<-release
		m.broadcast <- &model.WsPacket{Type: model.TypeDanmaku}
	})
	defer delete(actionRegistry, action)
	callbackDone := make(chan struct{})
	go func() { defer close(callbackDone); Dispatch(nil, m, action, nil) }()
	<-entered
	m.Quiesce()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("did not wait for callback: %v", err)
	}
	close(release)
	<-callbackDone
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := m.Shutdown(ctx2); err != nil {
		t.Fatal(err)
	}
	if p.count != 1 {
		t.Fatalf("accepted callback lost: %d", p.count)
	}
}
func TestShutdownJoinsSubscriptions(t *testing.T) {
	p := newFakeProducer()
	close(p.release)
	m := testManager(p, func(context.Context, string, string, uint64) error { return nil })
	entered, release := make(chan struct{}), make(chan struct{})
	m.io.subscribe = func(ctx context.Context, _ string, _ func([]byte)) { close(entered); <-ctx.Done(); <-release }
	go m.Start()
	m.RegisterClient(NewClient(1, "test", "room", nil))
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("did not join subscription: %v", err)
	}
	close(release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := m.Shutdown(ctx2); err != nil {
		t.Fatal(err)
	}
}
func TestStatisticsDoNotBlockMessagesAndShutdownJoins(t *testing.T) {
	p := newFakeProducer()
	close(p.release)
	m := testManager(p, func(context.Context, string, string, uint64) error { return nil })
	ticks := make(chan time.Time)
	entered, release, published, redisClosed := make(chan struct{}, 2), make(chan struct{}), make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	m.io.stats = func(context.Context, string, int) (model.StatsData, error) {
		entered <- struct{}{}
		<-release
		return model.StatsData{}, nil
	}
	m.io.publish = func(context.Context, string, []byte) error { published <- struct{}{}; return nil }
	m.io.close = func() error { close(redisClosed); return nil }
	m.RegisterClient(NewClient(1, "test", "room", nil))
	go func() { m.startOnce.Do(func() { m.runWithTicks(ticks) }) }()
	select {
	case ticks <- time.Now():
	case <-time.After(time.Second):
		t.Fatal("manager did not start")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("statistics did not start")
	}
	// A second tick must be consumed without starting an overlapping stats task.
	select {
	case ticks <- time.Now():
	case <-time.After(time.Second):
		t.Fatal("statistics blocked the main loop")
	}
	if !m.Submit(&model.WsPacket{Type: model.TypeDanmaku, RoomID: "room"}) {
		t.Fatal("message rejected")
	}
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("statistics blocked message processing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("did not join statistics: %v", err)
	}
	select {
	case <-redisClosed:
		t.Fatal("Redis closed while statistics running")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := m.Shutdown(ctx2); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
		t.Fatal("overlapping statistics task started")
	default:
	}
	if p.count != 1 {
		t.Fatalf("accepted messages=%d", p.count)
	}
}
