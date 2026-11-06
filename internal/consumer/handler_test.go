package consumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/JavaLyHn/DanmakuFlow/internal/model"
)

type fakeStore struct {
	err   error
	calls int
}

func (f *fakeStore) CreateInBatchesContext(_ context.Context, _ []*model.DanmakuMessage) error {
	f.calls++
	return f.err
}

type fakeSession struct{ marked []int64 }

func (s *fakeSession) Claims() map[string][]int32             { return map[string][]int32{"topic": {0}} }
func (*fakeSession) MemberID() string                         { return "member" }
func (*fakeSession) GenerationID() int32                      { return 1 }
func (*fakeSession) MarkOffset(string, int32, int64, string)  {}
func (*fakeSession) Commit()                                  {}
func (*fakeSession) ResetOffset(string, int32, int64, string) {}
func (s *fakeSession) MarkMessage(m *sarama.ConsumerMessage, _ string) {
	s.marked = append(s.marked, m.Offset)
}
func (*fakeSession) Context() context.Context { return context.Background() }
func TestFlushMarksOnlyAfterSuccessfulWrite(t *testing.T) {
	store := &fakeStore{err: errors.New("failed")}
	h := NewDanmakuHandler(store, 2)
	s := &fakeSession{}
	batch := []queued{{msg: &sarama.ConsumerMessage{Offset: 1}, row: &model.DanmakuMessage{}}, {msg: &sarama.ConsumerMessage{Offset: 2}}}
	if h.flush(context.Background(), s, batch) == nil || len(s.marked) != 0 {
		t.Fatal("failed write marked offsets")
	}
	store.err = nil
	if err := h.flush(context.Background(), s, batch); err != nil || len(s.marked) != 2 || s.marked[1] != 2 {
		t.Fatal("successful batch not marked in order")
	}
}
func TestSessionQueueReplaced(t *testing.T) {
	h := NewDanmakuHandler(&fakeStore{}, 2)
	s := &fakeSession{}
	if err := h.Setup(s); err != nil {
		t.Fatal(err)
	}
	first := h.queue
	if err := h.Cleanup(s); err != nil {
		t.Fatal(err)
	}
	if err := h.Setup(s); err != nil {
		t.Fatal(err)
	}
	if first == h.queue {
		t.Fatal("queue reused")
	}
	if err := h.Cleanup(s); err != nil {
		t.Fatal(err)
	}
}
func TestCleanupDrainsQueuedRecordsInOrder(t *testing.T) {
	store := &fakeStore{}
	h := NewDanmakuHandler(store, 100)
	s := &fakeSession{}
	if err := h.Setup(s); err != nil {
		t.Fatal(err)
	}
	h.queue <- queued{msg: &sarama.ConsumerMessage{Offset: 1}, row: &model.DanmakuMessage{}}
	h.queue <- queued{msg: &sarama.ConsumerMessage{Offset: 2}}
	if err := h.Cleanup(s); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 || len(s.marked) != 2 || s.marked[0] != 1 || s.marked[1] != 2 {
		t.Fatalf("drain: writes=%d marked=%v", store.calls, s.marked)
	}
}
func TestQuiesceFlushesAcceptedRecords(t *testing.T) {
	h := NewDanmakuHandler(&fakeStore{}, 100)
	s := &fakeSession{}
	if err := h.Setup(s); err != nil {
		t.Fatal(err)
	}
	h.queue <- queued{msg: &sarama.ConsumerMessage{Partition: 0, Offset: 7}, row: &model.DanmakuMessage{}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	offsets, err := h.Quiesce(ctx)
	if err != nil || offsets[0] != 8 {
		t.Fatalf("offsets=%v err=%v", offsets, err)
	}
	h.Resume()
	if err := h.Cleanup(s); err != nil {
		t.Fatal(err)
	}
}

type controlledStore struct {
	entered     chan struct{}
	release     chan struct{}
	calls       int
	cancelFirst bool
}

func (s *controlledStore) CreateInBatchesContext(ctx context.Context, _ []*model.DanmakuMessage) error {
	s.calls++
	if s.calls == 1 {
		close(s.entered)
		if s.cancelFirst {
			<-ctx.Done()
			return ctx.Err()
		}
		<-s.release
		return errors.New("temporary write failure")
	}
	return nil
}

type contextualSession struct {
	fakeSession
	ctx context.Context
}

func (s *contextualSession) Context() context.Context { return s.ctx }
func TestQuiesceTimeoutRetainsFailedBatchForRetry(t *testing.T) {
	store := &controlledStore{entered: make(chan struct{}), release: make(chan struct{})}
	h := NewDanmakuHandler(store, 100)
	s := &fakeSession{}
	if err := h.Setup(s); err != nil {
		t.Fatal(err)
	}
	h.queue <- queued{msg: &sarama.ConsumerMessage{Partition: 0, Offset: 7}, row: &model.DanmakuMessage{}}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := h.Quiesce(ctx); first <- err }()
	<-store.entered
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("quiesce=%v", err)
	}
	close(store.release)
	retry, cancelRetry := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelRetry()
	offsets, err := h.Quiesce(retry)
	if err != nil || offsets[0] != 8 {
		t.Fatalf("retry offsets=%v err=%v", offsets, err)
	}
	if err := h.Cleanup(s); err != nil {
		t.Fatal(err)
	}
	if len(s.marked) != 1 || s.marked[0] != 7 || store.calls != 2 {
		t.Fatalf("marks=%v calls=%d", s.marked, store.calls)
	}
}
func TestRebalanceUnblocksQuiesceAndUsesFreshDrainContext(t *testing.T) {
	store := &controlledStore{entered: make(chan struct{}), cancelFirst: true}
	h := NewDanmakuHandler(store, 100)
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()
	s := &contextualSession{ctx: sessionCtx}
	if err := h.Setup(s); err != nil {
		t.Fatal(err)
	}
	h.queue <- queued{msg: &sarama.ConsumerMessage{Partition: 0, Offset: 3}, row: &model.DanmakuMessage{}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	quiesced := make(chan error, 1)
	go func() { _, err := h.Quiesce(ctx); quiesced <- err }()
	<-store.entered
	cancelSession()
	cleaned := make(chan error, 1)
	go func() { cleaned <- h.Cleanup(s) }()
	if err := <-quiesced; err == nil {
		t.Fatal("quiesce succeeded across failed session")
	}
	select {
	case err := <-cleaned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup deadlocked with quiesce")
	}
	if len(s.marked) != 1 || s.marked[0] != 3 || store.calls != 2 {
		t.Fatalf("marks=%v calls=%d", s.marked, store.calls)
	}
	h.Resume()
	next := &fakeSession{}
	if err := h.Setup(next); err != nil {
		t.Fatal(err)
	}
	if err := h.Cleanup(next); err != nil {
		t.Fatal(err)
	}
}
