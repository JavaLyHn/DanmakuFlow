package ws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"github.com/JavaLyHn/DanmakuFlow/internal/infra"
	"github.com/JavaLyHn/DanmakuFlow/internal/model"
)

const (
	BroadCastInterVal    = 3 * time.Second
	BroadcastChanSize    = 1024
	BroadcastJobChanSize = 1000
	WorkerCount          = 100
)

type asyncProducer interface {
	Input() chan<- *sarama.ProducerMessage
	Errors() <-chan *sarama.ProducerError
	Successes() <-chan *sarama.ProducerMessage
	AsyncClose()
}
type managerIO struct {
	publish   func(context.Context, string, []byte) error
	likes     func(context.Context, string, string, uint64) error
	stats     func(context.Context, string, int) (model.StatsData, error)
	subscribe func(context.Context, string, func([]byte))
	close     func() error
}
type BroadcastJob struct {
	Clients []*Client
	Payload []byte
}
type likeBatch struct {
	id    string
	count uint64
}

// Manager owns admission, callbacks, subscriptions, work queues and producer
// completion. Channels are private so callers cannot bypass admission.
type Manager struct {
	ServerID      string
	mu            sync.Mutex
	accepting     bool
	Rooms         map[string]map[*Client]struct{}
	cancelSub     map[string]context.CancelFunc
	callbacks     sync.WaitGroup
	pumps         sync.WaitGroup
	subscriptions sync.WaitGroup
	statistics    sync.WaitGroup
	statsRunning  bool
	workers       sync.WaitGroup
	broadcast     chan *model.WsPacket
	jobs          chan BroadcastJob
	stop          chan struct{}
	loopDone      chan struct{}
	drained       chan struct{}
	producerDone  chan struct{}
	startOnce     sync.Once
	stopOnce      sync.Once
	producerOnce  sync.Once
	shutdownGate  chan struct{}
	likesMu       sync.Mutex
	localLikes    map[string]uint64
	pendingLikes  map[string]likeBatch
	flushMu       sync.Mutex
	errMu         sync.Mutex
	workErr       error
	producerErr   error
	closed        bool
	producer      asyncProducer
	io            managerIO
}

func NewManager() *Manager {
	rdb := infra.InitRedisClient()
	id := randomID()
	return newManager(id, infra.InitKafkaProducer([]string{"127.0.0.1:9092"}), managerIO{
		publish: func(ctx context.Context, room string, data []byte) error {
			return rdb.Publish(ctx, fmt.Sprintf(infra.KeyRoomPubSub, room), data).Err()
		},
		likes: func(ctx context.Context, room, batch string, n uint64) error {
			return infra.FlushRoomLikes(ctx, rdb, room, id, batch, n)
		},
		stats: func(ctx context.Context, room string, count int) (model.StatsData, error) {
			pipe := rdb.Pipeline()
			pipe.Set(ctx, fmt.Sprintf(infra.KeyRoomServerOnline, room, id), count, BroadCastInterVal*2+time.Second)
			pipe.SAdd(ctx, fmt.Sprintf(infra.KeyServerOfRoom, room), id)
			if _, err := pipe.Exec(ctx); err != nil {
				return model.StatsData{}, err
			}
			return model.StatsData{Online: infra.GetTotalOnline(rdb, room), Likes: infra.GetRoomLikes(rdb, room)}, nil
		},
		subscribe: func(ctx context.Context, room string, deliver func([]byte)) {
			sub := rdb.Subscribe(ctx, fmt.Sprintf(infra.KeyRoomPubSub, room))
			defer sub.Close()
			ch := sub.Channel()
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-ch:
					if !ok {
						return
					}
					deliver([]byte(msg.Payload))
				}
			}
		}, close: rdb.Close,
	})
}

// randomID also identifies a Redis batch across retries after an ambiguous reply.
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func newManager(id string, p asyncProducer, io managerIO) *Manager {
	m := &Manager{ServerID: id, accepting: true, Rooms: make(map[string]map[*Client]struct{}), cancelSub: make(map[string]context.CancelFunc), broadcast: make(chan *model.WsPacket, BroadcastChanSize), jobs: make(chan BroadcastJob, BroadcastJobChanSize), stop: make(chan struct{}), loopDone: make(chan struct{}), drained: make(chan struct{}), producerDone: make(chan struct{}), localLikes: make(map[string]uint64), pendingLikes: make(map[string]likeBatch), producer: p, io: io, shutdownGate: make(chan struct{}, 1)}
	go m.collectProducerResults()
	return m
}
func (m *Manager) collectProducerResults() {
	defer close(m.producerDone)
	failures, successes := m.producer.Errors(), m.producer.Successes()
	for failures != nil || successes != nil {
		select {
		case e, ok := <-failures:
			if !ok {
				failures = nil
			} else if e != nil {
				m.errMu.Lock()
				m.producerErr = errors.New("Kafka failed to persist accepted messages")
				m.errMu.Unlock()
			}
		case _, ok := <-successes:
			if !ok {
				successes = nil
			}
		}
	}
}
func (m *Manager) begin() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.accepting {
		return false
	}
	m.callbacks.Add(1)
	return true
}

// BeginUpgrade tracks the HTTP-to-WebSocket transition, including a connection
// upgraded concurrently with quiescing. The handler must always call release.
func (m *Manager) BeginUpgrade() (release func(), ok bool) {
	if !m.begin() {
		return nil, false
	}
	return m.callbacks.Done, true
}
func (m *Manager) Submit(packet *model.WsPacket) bool {
	if !m.begin() {
		return false
	}
	defer m.callbacks.Done()
	m.broadcast <- packet
	return true
}
func (m *Manager) AddLikes(room string, n uint64) bool {
	if n == 0 {
		return false
	}
	if !m.begin() {
		return false
	}
	defer m.callbacks.Done()
	m.likesMu.Lock()
	m.localLikes[room] += n
	m.likesMu.Unlock()
	return true
}
func (m *Manager) RegisterClient(c *Client) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.accepting {
		c.Close()
		return false
	}
	m.registerLocked(c)
	return true
}
func (m *Manager) registerLocked(c *Client) {
	if m.Rooms[c.RoomID] == nil {
		m.Rooms[c.RoomID] = make(map[*Client]struct{})
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelSub[c.RoomID] = cancel
		m.subscriptions.Add(1)
		go func() {
			defer m.subscriptions.Done()
			m.io.subscribe(ctx, c.RoomID, func(data []byte) { m.broadcastToLocalClients(c.RoomID, data) })
		}()
	}
	m.Rooms[c.RoomID][c] = struct{}{}
}

// ServeClient reserves both pump lifetimes atomically with admission.
func (m *Manager) ServeClient(c *Client) bool {
	m.mu.Lock()
	if !m.accepting {
		m.mu.Unlock()
		c.Close()
		return false
	}
	m.registerLocked(c)
	m.pumps.Add(2)
	m.mu.Unlock()
	go func() { defer m.pumps.Done(); c.WritePump() }()
	defer m.pumps.Done()
	c.ReadPump(m)
	return true
}
func (m *Manager) UnregisterClient(c *Client) {
	c.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	clients := m.Rooms[c.RoomID]
	delete(clients, c)
	if len(clients) == 0 {
		delete(m.Rooms, c.RoomID)
		if cancel := m.cancelSub[c.RoomID]; cancel != nil {
			cancel()
			delete(m.cancelSub, c.RoomID)
		}
	}
}
func (m *Manager) Start() { m.startOnce.Do(m.run) }
func (m *Manager) run() {
	ticker := time.NewTicker(BroadCastInterVal)
	defer ticker.Stop()
	m.runWithTicks(ticker.C)
}
func (m *Manager) runWithTicks(tick <-chan time.Time) {
	defer close(m.loopDone)
	for i := 0; i < WorkerCount; i++ {
		m.workers.Add(1)
		go func() {
			defer m.workers.Done()
			for job := range m.jobs {
				for _, c := range job.Clients {
					select {
					case c.Send <- job.Payload:
					default:
					}
				}
			}
		}()
	}
	for {
		select {
		case packet, ok := <-m.broadcast:
			if !ok {
				return
			}
			m.handleBroadcast(packet)
		case <-tick:
			m.startStats()
		case <-m.stop:
			tick = nil
			for packet := range m.broadcast {
				m.handleBroadcast(packet)
			}
			return
		}
	}
}

// Statistics perform Redis I/O outside the message loop. Admission and the
// in-flight flag share the same lock so quiescing cannot race a new task.
func (m *Manager) startStats() {
	m.mu.Lock()
	if !m.accepting || m.statsRunning {
		m.mu.Unlock()
		return
	}
	m.statsRunning = true
	m.statistics.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.statistics.Done()
		defer func() { m.mu.Lock(); m.statsRunning = false; m.mu.Unlock() }()
		m.broadcastStats()
	}()
}
func (m *Manager) handleBroadcast(packet *model.WsPacket) {
	if packet == nil {
		return
	}
	data, err := json.Marshal(packet)
	if err != nil {
		m.recordError(errors.New("accepted message encoding failed"))
		return
	}
	switch packet.Type {
	case model.TypeDanmaku:
		m.producer.Input() <- &sarama.ProducerMessage{Topic: infra.DanmakuSaveTopic, Value: sarama.ByteEncoder(data)}
		ctx, cancel := context.WithTimeout(context.Background(), infra.RedisCancelTimeout)
		err = m.io.publish(ctx, packet.RoomID, data)
		cancel()
		if err != nil {
			m.recordError(errors.New("Redis failed to distribute accepted messages"))
		}
	case model.TypeStats:
		m.broadcastToLocalClients(packet.RoomID, data)
	}
}
func (m *Manager) recordError(err error) {
	m.errMu.Lock()
	if m.workErr == nil {
		m.workErr = err
	}
	m.errMu.Unlock()
}
func (m *Manager) broadcastToLocalClients(room string, payload []byte) {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.Rooms[room]))
	for c := range m.Rooms[room] {
		clients = append(clients, c)
	}
	m.mu.Unlock()
	if len(clients) > 0 {
		m.jobs <- BroadcastJob{clients, payload}
	}
}
func (m *Manager) flushLikes(ctx context.Context) error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.likesMu.Lock()
		for room, count := range m.localLikes {
			if _, pending := m.pendingLikes[room]; !pending && count > 0 {
				m.pendingLikes[room] = likeBatch{randomID(), count}
				delete(m.localLikes, room)
			}
		}
		m.likesMu.Unlock()
		for room, batch := range m.pendingLikes {
			if err := m.io.likes(ctx, room, batch.id, batch.count); err != nil {
				return errors.New("Redis pending likes flush failed")
			}
			delete(m.pendingLikes, room)
		}
		// A room may have accumulated a new batch while an older one awaited retry.
		m.likesMu.Lock()
		remaining := len(m.localLikes) > 0
		m.likesMu.Unlock()
		if !remaining {
			return nil
		}
	}
}
func (m *Manager) broadcastStats() {
	ctx, cancel := context.WithTimeout(context.Background(), infra.RedisCancelTimeout)
	defer cancel()
	_ = m.flushLikes(ctx)
	m.mu.Lock()
	rooms := make(map[string]int, len(m.Rooms))
	for room, clients := range m.Rooms {
		rooms[room] = len(clients)
	}
	m.mu.Unlock()
	for room, count := range rooms {
		stats, err := m.io.stats(ctx, room, count)
		if err != nil {
			continue
		}
		data, _ := json.Marshal(stats)
		payload, _ := json.Marshal(&model.WsPacket{Type: model.TypeStats, RoomID: room, Data: data})
		m.broadcastToLocalClients(room, payload)
	}
}

// Quiesce closes admission immediately. Its drain runs independently of the
// caller's deadline so a timeout cannot discard accepted messages or kill the
// process. A subsequent Shutdown call resumes waiting and retries likes.
func (m *Manager) Quiesce() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.accepting = false
		close(m.stop)
		for _, clients := range m.Rooms {
			for c := range clients {
				c.Close()
			}
		}
		for _, cancel := range m.cancelSub {
			cancel()
		}
		m.mu.Unlock()
		go func() {
			m.callbacks.Wait()
			m.pumps.Wait()
			m.subscriptions.Wait()
			close(m.broadcast)
			<-m.loopDone
			m.statistics.Wait()
			close(m.jobs)
			m.workers.Wait()
			close(m.drained)
		}()
		go m.Start()
	})
}
func (m *Manager) Shutdown(ctx context.Context) error {
	m.Quiesce()
	// Serialize attempts without holding a mutex beyond the caller's deadline.
	select {
	case <-m.drained:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case m.shutdownGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.shutdownGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.closed {
		return nil
	}
	if err := m.flushLikes(ctx); err != nil {
		return err
	}
	m.producerOnce.Do(m.producer.AsyncClose)
	select {
	case <-m.producerDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	m.errMu.Lock()
	err := errors.Join(m.workErr, m.producerErr)
	m.errMu.Unlock()
	if err != nil {
		return err
	}
	if err = m.io.close(); err != nil {
		return errors.New("Redis client close failed")
	}
	m.closed = true
	return nil
}

// Assert production dependencies implement the owned producer contract.
var _ asyncProducer = (sarama.AsyncProducer)(nil)
