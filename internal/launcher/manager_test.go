package launcher

import (
 "context"
 "errors"
 "sync"
 "testing"
 "time"
 "path/filepath"

 "github.com/JavaLyHn/DanmakuFlow/internal/localproc"
)

type fakeDriver struct{mu sync.Mutex;events []string;live map[string]bool;external map[string]bool;failStart,failStop,guard string;wait chan struct{}}
func newFake()*fakeDriver{return &fakeDriver{live:map[string]bool{},external:map[string]bool{}}}
func(f *fakeDriver)Validate()error{return nil}
func(f *fakeDriver)Discover(_ context.Context,id string,old *Instance)(*Instance,error){f.mu.Lock();defer f.mu.Unlock();if f.live[id]{if old!=nil{return old,nil};return &Instance{Identity:localproc.Identity{PID:1,Created:1,Executable:id},Owned:false},nil};return nil,nil}
func(f *fakeDriver)Start(ctx context.Context,id string)(Instance,error){if f.wait!=nil{select{case<-f.wait:case<-ctx.Done():return Instance{},ctx.Err()}};f.mu.Lock();defer f.mu.Unlock();f.events=append(f.events,"start:"+id);if id==f.failStart{return Instance{},errors.New("spawn failed")};f.live[id]=true;return Instance{Identity:localproc.Identity{PID:1,Created:1,Executable:id},Owned:true,RunID:id},nil}
func(f *fakeDriver)Probe(context.Context,string,Instance)error{return nil}
func(f *fakeDriver)Alive(i Instance)bool{f.mu.Lock();defer f.mu.Unlock();return f.live[i.Identity.Executable]}
func(f *fakeDriver)Stop(_ context.Context,id string,_ Instance,_ map[int32]int64)error{f.mu.Lock();defer f.mu.Unlock();f.events=append(f.events,"stop:"+id);if id==f.failStop{return errors.New("flush pending")};delete(f.live,id);return nil}
func(f *fakeDriver)Targets(context.Context)(map[int32]int64,error){return map[int32]int64{0:12},nil}
func(f *fakeDriver)BeforeStopDependency(_ context.Context,id string)error{if id==f.guard{return errors.New("unknown external dependent")};return nil}
func waitOperation(t *testing.T,m *Manager){t.Helper();limit:=time.Now().Add(time.Second*3);for time.Now().Before(limit){if !m.Snapshot().Busy{return};time.Sleep(time.Millisecond)};t.Fatal("operation stuck")}

func TestRepeatedStartReusesServicesAndStopsOnlyOwned(t *testing.T){
 f:=newFake();f.live["mysql"]=true;m,err:=NewManager(f,"","test");if err!=nil{t.Fatal(err)}
 if err=m.Start();err!=nil{t.Fatal(err)};waitOperation(t,m)
 if err=m.Start();err!=nil{t.Fatal(err)};waitOperation(t,m)
 if err=m.Stop();err!=nil{t.Fatal(err)};waitOperation(t,m)
 f.mu.Lock();defer f.mu.Unlock()
 want:=[]string{"start:redis","start:kafka","start:consumer","start:server","stop:server","stop:consumer","stop:kafka","stop:redis"}
 if len(f.events)!=len(want){t.Fatalf("events=%v",f.events)};for i:=range want{if f.events[i]!=want[i]{t.Fatalf("events=%v",f.events)}}
 if !f.live["mysql"]{t.Fatal("external mysql stopped")}
}
func TestPartialStartCanBeStoppedAndStopFailurePreservesDependencies(t *testing.T){
 f:=newFake();f.failStart="server";m,_:=NewManager(f,"","test");_ = m.Start();waitOperation(t,m)
 if m.Snapshot().Error==""{t.Fatal("startup failure hidden")}
 f.failStop="consumer";_ = m.Stop();waitOperation(t,m)
 if m.Snapshot().CanStart{t.Fatal("start allowed during unfinished stop")}
 f.mu.Lock();if !f.live["kafka"]||!f.live["mysql"]{t.Fatal("dependencies stopped before consumer")};f.failStop="";f.mu.Unlock()
 _=m.Stop();waitOperation(t,m);if !m.Snapshot().CanStart{t.Fatal("successful retry did not permit start")}
}
func TestConcurrentActionsAreSerialized(t *testing.T){f:=newFake();f.wait=make(chan struct{});m,_:=NewManager(f,"","test");if err:=m.Start();err!=nil{t.Fatal(err)};if err:=m.Start();err==nil{t.Fatal("duplicate operation accepted")};if err:=m.Stop();err==nil{t.Fatal("concurrent stop accepted")};close(f.wait);waitOperation(t,m)}
func TestExistingConsumerKeepsItsDependencies(t *testing.T){f:=newFake();f.live["consumer"]=true;m,_:=NewManager(f,"","test");_ =m.Start();waitOperation(t,m);_=m.Stop();waitOperation(t,m);f.mu.Lock();defer f.mu.Unlock();if !f.live["consumer"]||!f.live["mysql"]||!f.live["kafka"]{t.Fatalf("external consumer dependencies stopped: %v",f.live)}}
func TestConfirmedStopsSurviveLauncherRestart(t *testing.T){
 f:=newFake();path:=filepath.Join(t.TempDir(),"instances.json");m,_:=NewManager(f,path,"test");_=m.Start();waitOperation(t,m)
 f.failStop="redis";_=m.Stop();waitOperation(t,m);f.failStop=""
 recovered,err:=NewManager(f,path,"test");if err!=nil{t.Fatal(err)};_=recovered.Stop();waitOperation(t,recovered)
 count:=0;for _,event:=range f.events{if event=="stop:kafka"{count++}};if count!=1{t.Fatalf("confirmed Kafka stop repeated %d times",count)};if f.live["mysql"]{t.Fatal("retry did not finish")}
}
func TestUnknownExternalDependencyIsPreservedAfterPartialStart(t *testing.T){f:=newFake();f.failStart="consumer";f.guard="mysql";m,_:=NewManager(f,"","test");_=m.Start();waitOperation(t,m);_=m.Stop();waitOperation(t,m);if !f.live["mysql"]{t.Fatal("external dependency was stopped")};if m.Snapshot().Phase!="partial"{t.Fatal("preserved component hidden")}}
