package launcher

import (
 "context"
 "errors"
 "sync"
 "testing"
 "time"
 "path/filepath"

 "github.com/JavaLyHn/DanmakuFlow/internal/localproc"
 "github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
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

type growingTargetsDriver struct{
 *fakeDriver
 queries int
 received []int64
 statePath string
 afterTargets func()
}
func(f *growingTargetsDriver)Targets(context.Context)(map[int32]int64,error){
 f.mu.Lock();f.queries++;target:=int64(f.queries*12);hook:=f.afterTargets;f.mu.Unlock()
 if hook!=nil{hook()};return map[int32]int64{0:target},nil
}
func(f *growingTargetsDriver)Stop(ctx context.Context,id string,inst Instance,targets map[int32]int64)error{
 if id=="consumer"{
  f.mu.Lock();f.received=append(f.received,targets[0]);f.mu.Unlock()
  var saved savedState
  if err:=runcontrol.ReadJSON(f.statePath,&saved);err!=nil{return err}
  if saved.ConsumerTargets==nil||saved.ConsumerTargets[0]!=targets[0]{return errors.New("consumer request preceded durable target record")}
 }
 return f.fakeDriver.Stop(ctx,id,inst,targets)
}
func TestConsumerStopTargetSurvivesRetryAndLauncherRestart(t *testing.T){
 path:=filepath.Join(t.TempDir(),"instances.json")
 f:=&growingTargetsDriver{fakeDriver:newFake(),statePath:path};m,err:=NewManager(f,path,"test");if err!=nil{t.Fatal(err)}
 if err=m.Start();err!=nil{t.Fatal(err)};waitOperation(t,m)
 f.failStop="consumer"
 for i:=0;i<2;i++{if err=m.Stop();err!=nil{t.Fatal(err)};waitOperation(t,m);if m.Snapshot().CanStart{t.Fatal("failed stop permits restart")}}
 recovered,err:=NewManager(f,path,"test");if err!=nil{t.Fatal(err)}
 f.failStop="";if err=recovered.Stop();err!=nil{t.Fatal(err)};waitOperation(t,recovered)
 if recovered.Snapshot().Phase!="stopped"{t.Fatalf("recovered stop failed: %+v",recovered.Snapshot())}
 if f.queries!=1||len(f.received)!=3{t.Fatalf("queries=%d requests=%v",f.queries,f.received)}
 for _,target:=range f.received{if target!=12{t.Fatalf("stop boundary moved: %v",f.received)}}
 var saved savedState;if err=runcontrol.ReadJSON(path,&saved);err!=nil{t.Fatal(err)};if saved.ConsumerTargets!=nil{t.Fatal("completed stop retained its old boundary")}
 if err=recovered.Start();err!=nil{t.Fatal(err)};waitOperation(t,recovered)
 if err=recovered.Stop();err!=nil{t.Fatal(err)};waitOperation(t,recovered)
 if f.queries!=2||len(f.received)!=4||f.received[3]!=24{t.Fatalf("new run reused old boundary: queries=%d requests=%v",f.queries,f.received)}
}
func TestConsumerStopTargetSaveFailureDoesNotSendRequest(t *testing.T){
 dir:=t.TempDir();path:=filepath.Join(dir,"instances.json")
 f:=&growingTargetsDriver{fakeDriver:newFake(),statePath:path};m,err:=NewManager(f,path,"test");if err!=nil{t.Fatal(err)}
 if err=m.Start();err!=nil{t.Fatal(err)};waitOperation(t,m)
 // Fail persistence precisely after target collection, after begin/producer stop.
 f.afterTargets=func(){m.mu.Lock();m.stateFile=filepath.Join(dir,"missing","instances.json");m.mu.Unlock()}
 if err=m.Stop();err!=nil{t.Fatal(err)};waitOperation(t,m)
 if len(f.received)!=0||!f.live["consumer"]||!f.live["kafka"]{t.Fatal("consumer or dependencies stopped without saved targets")}
 if m.Snapshot().CanStart{t.Fatal("target save failure permits a new start")}
 m.mu.Lock();m.stateFile=path;m.mu.Unlock();f.afterTargets=nil
 if err=m.Stop();err!=nil{t.Fatal(err)};waitOperation(t,m)
 if m.Snapshot().Phase!="stopped"||f.queries!=1||len(f.received)!=1||f.received[0]!=12{t.Fatalf("save retry changed stop boundary: state=%+v queries=%d requests=%v",m.Snapshot(),f.queries,f.received)}
}
