package main

import (
 "context"
 "errors"
 "os"
 "testing"
 "time"
 "github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
)
func TestWaitTargetsRequiresBrokerOffsets(t *testing.T) {
 calls:=0
 err:=waitTargets(context.Background(),map[int32]int64{0:5},func(context.Context,map[int32]int64)(map[int32]int64,error){ calls++; if calls==1{return map[int32]int64{0:4},nil};return map[int32]int64{0:5},nil },time.Millisecond)
 if err!=nil || calls!=2 {t.Fatalf("calls=%d err=%v",calls,err)}
}
func TestWaitTargetsTimeoutAndReadFailure(t *testing.T) {
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Millisecond);defer cancel()
 err:=waitTargets(ctx,map[int32]int64{0:1},func(context.Context,map[int32]int64)(map[int32]int64,error){return nil,errors.New("broker unavailable")},time.Millisecond)
 if err==nil {t.Fatal("unconfirmed offsets succeeded")}
}
func TestWaitTargetsMissingPartition(t *testing.T) {
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Millisecond);defer cancel()
 err:=waitTargets(ctx,map[int32]int64{0:0},func(context.Context,map[int32]int64)(map[int32]int64,error){return map[int32]int64{},nil},time.Millisecond)
 if err==nil {t.Fatal("missing partition accepted")}
}
func TestUncommittedEmptyPartition(t *testing.T) {
 if !offsetReached(-1,0) { t.Fatal("empty partition should meet zero target") }
 if offsetReached(-1,1) { t.Fatal("uncommitted offset met positive target") }
}
func TestOffsetFetchVersion(t *testing.T) {
 if offsetRequest().Version < 1 { t.Fatal("obsolete OffsetFetch version") }
}
func TestSignalFailureStillObservesPrivateStopSuccess(t *testing.T) {
 signals:=make(chan os.Signal,1);control:=make(chan error,1);failed:=make(chan struct{});done:=make(chan struct{})
 go func(){defer close(done);awaitShutdown(signals,control,func(context.Context,runcontrol.Request)error{return errors.New("retry needed")},func(phase string,_ error){if phase=="stop_failed"{close(failed)}})}()
 signals<-os.Interrupt
 select{case <-failed:case <-time.After(time.Second):t.Fatal("signal stop not attempted")}
 control<-nil
 select{case <-done:case <-time.After(time.Second):t.Fatal("private stop success did not exit after failed signal stop")}
}
func TestSignalStopCanRetryAfterWatcherFailure(t *testing.T) {
 signals:=make(chan os.Signal,2);control:=make(chan error,1);done:=make(chan struct{});watcherFailed:=make(chan struct{});calls:=0
 go func(){defer close(done);awaitShutdown(signals,control,func(context.Context,runcontrol.Request)error{calls++;if calls==1{return errors.New("retry needed")};return nil},func(phase string,_ error){if phase=="control_failed"{close(watcherFailed)}})}()
 control<-errors.New("watcher failed");<-watcherFailed;signals<-os.Interrupt;signals<-os.Interrupt
 select{case <-done:case <-time.After(time.Second):t.Fatal("signal retry did not finish")}
 if calls!=2{t.Fatalf("stop calls=%d",calls)}
}
func TestSerializedStopWaitingAttemptHonorsCancellation(t *testing.T) {
 entered,release,done:=make(chan struct{}),make(chan struct{}),make(chan struct{})
 stop:=serializedStop(func(context.Context,runcontrol.Request)error{close(entered);<-release;return nil})
 go func(){defer close(done);_ = stop(context.Background(),runcontrol.Request{})}();<-entered
 ctx,cancel:=context.WithCancel(context.Background());cancel()
 if err:=stop(ctx,runcontrol.Request{});!errors.Is(err,context.Canceled){t.Fatalf("waiting attempt=%v",err)}
 close(release);<-done
}
