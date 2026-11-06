package consumer

import (
 "context"
 "encoding/json"
 "time"
 "sync"
 "errors"

 "github.com/IBM/sarama"
 "github.com/JavaLyHn/DanmakuFlow/internal/model"
)

const (
 FlushInterval = 2 * time.Second
 ChannelBufferSize = 2000
 BackoffDuration = 500 * time.Millisecond
 finalFlushTimeout = 10 * time.Second
)

type batchStore interface { CreateInBatchesContext(context.Context, []*model.DanmakuMessage) error }
type queued struct { msg *sarama.ConsumerMessage; row *model.DanmakuMessage; barrier chan struct{} }

type DanmakuHandler struct {
 repo batchStore
 batchSize int
 queue chan queued
 done chan error
 // Ready is called after a session with at least one assigned partition starts its worker.
 Ready func()
 NotReady func()
 mu sync.Mutex
 paused bool
 active int
 processed map[int32]int64
 sessionDone chan struct{}
}
func NewDanmakuHandler(r batchStore, size int) *DanmakuHandler {
 if size < 1 { size = 1 }; return &DanmakuHandler{repo:r, batchSize:size}
}
func (h *DanmakuHandler) Setup(s sarama.ConsumerGroupSession) error {
 h.mu.Lock(); h.queue = make(chan queued, ChannelBufferSize); h.done = make(chan error, 1); h.sessionDone=make(chan struct{}); if h.processed==nil {h.processed=make(map[int32]int64)}; h.mu.Unlock()
 go h.worker(s)
 for _, partitions := range s.Claims() { if len(partitions) > 0 && h.Ready != nil { h.Ready(); break } }
 return nil
}
func (h *DanmakuHandler) Cleanup(_ sarama.ConsumerGroupSession) error { if h.NotReady != nil { h.NotReady() }; h.mu.Lock(); close(h.sessionDone); h.sessionDone=nil; h.mu.Unlock(); for {h.mu.Lock();n:=h.active;h.mu.Unlock();if n==0{break};time.Sleep(time.Millisecond)}; close(h.queue); return <-h.done }
func (h *DanmakuHandler) ConsumeClaim(s sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
 for { select {
 case m, ok := <-claim.Messages():
  if !ok { return nil }
  row, _ := h.parseMessage(m)
  for { h.mu.Lock(); if !h.paused {h.active++;h.mu.Unlock();break};h.mu.Unlock();select {case <-s.Context().Done():return nil;case <-time.After(10*time.Millisecond):} }
  select { case h.queue <- queued{msg:m,row:row}: case <-s.Context().Done(): h.mu.Lock();h.active--;h.mu.Unlock();return nil }
  h.mu.Lock();h.active--;h.mu.Unlock()
 case <-s.Context().Done(): return nil
 } }
}
func (h *DanmakuHandler) worker(s sarama.ConsumerGroupSession) {
 batch := make([]queued, 0, h.batchSize); ticker := time.NewTicker(FlushInterval); defer ticker.Stop()
 flush := func(ctx context.Context) error {
  for len(batch)>0 {
   if err := h.flush(ctx,s,batch); err == nil { batch = batch[:0]; return nil } else if ctx.Err()!=nil { return err }
   select { case <-ctx.Done(): return ctx.Err(); case <-time.After(BackoffDuration): }
  }; return nil
 }
 for { select {
 case item, ok := <-h.queue:
  if !ok { ctx,cancel := context.WithTimeout(context.Background(), finalFlushTimeout); err:=flush(ctx); cancel(); h.done <- err; return }
  if item.barrier!=nil { if err:=flush(s.Context());err!=nil {h.drainFinal(s,batch);return};close(item.barrier);continue }
  batch=append(batch,item)
  if len(batch)>=h.batchSize { if err:=flush(s.Context()); err!=nil { h.drainFinal(s,batch); return } }
 case <-ticker.C:
  if err:=flush(s.Context()); err!=nil { h.drainFinal(s,batch); return }
 } }
}
// After cancellation, consume every item already accepted by the queue before ending the session.
func (h *DanmakuHandler) drainFinal(s sarama.ConsumerGroupSession, batch []queued) {
 ctx,cancel:=context.WithTimeout(context.Background(),finalFlushTimeout); defer cancel()
 for item := range h.queue { if item.barrier==nil {batch=append(batch,item)} }
 for len(batch)>0 {
  if err:=h.flush(ctx,s,batch); err==nil { h.done<-nil; return } else if ctx.Err()!=nil { h.done<-err; return }
  select { case <-ctx.Done(): h.done<-ctx.Err(); return; case <-time.After(BackoffDuration): }
 }
 h.done<-nil
}
func (h *DanmakuHandler) flush(ctx context.Context, s sarama.ConsumerGroupSession, batch []queued) error {
 rows:=make([]*model.DanmakuMessage,0,len(batch)); for _, item:=range batch { if item.row!=nil { rows=append(rows,item.row) } }
 if len(rows)>0 { if err:=h.repo.CreateInBatchesContext(ctx,rows); err!=nil { return err } }
 for _, item:=range batch { s.MarkMessage(item.msg,"");h.mu.Lock();if h.processed==nil {h.processed=make(map[int32]int64)};if item.msg.Offset+1>h.processed[item.msg.Partition] {h.processed[item.msg.Partition]=item.msg.Offset+1};h.mu.Unlock() }; return nil
}

// Quiesce prevents new admissions and waits until accepted records are marked.
func (h *DanmakuHandler) Quiesce(ctx context.Context) (map[int32]int64,error) {
 h.mu.Lock();h.paused=true;done:=h.sessionDone;q:=h.queue;if done!=nil {h.active++};h.mu.Unlock()
 if done==nil {return nil,errors.New("no active consumer session")};defer func(){h.mu.Lock();h.active--;h.mu.Unlock()}()
 for {h.mu.Lock();n:=h.active;h.mu.Unlock();if n==1 {break};select{case <-ctx.Done():return nil,ctx.Err();case <-done:return nil,errors.New("consumer session ended");case <-time.After(10*time.Millisecond):}}
 barrier:=make(chan struct{})
 select{case q<-queued{barrier:barrier}:case <-ctx.Done():return nil,ctx.Err();case <-done:return nil,errors.New("consumer session ended")}
 select{case <-barrier:case <-ctx.Done():return nil,ctx.Err();case <-done:return nil,errors.New("consumer session ended")}
 h.mu.Lock();defer h.mu.Unlock();result:=make(map[int32]int64,len(h.processed));for p,n:=range h.processed {result[p]=n};return result,nil
}
func (h *DanmakuHandler) Resume() {h.mu.Lock();h.paused=false;h.mu.Unlock()}
func (h *DanmakuHandler) parseMessage(m *sarama.ConsumerMessage) (*model.DanmakuMessage,error) {
 var packet model.WsPacket; if err:=json.Unmarshal(m.Value,&packet); err!=nil { return nil,err }; if packet.Type!=model.TypeDanmaku { return nil,nil }
 var row model.DanmakuMessage; if err:=json.Unmarshal(packet.Data,&row); err!=nil { return nil,err }; return &row,nil
}
