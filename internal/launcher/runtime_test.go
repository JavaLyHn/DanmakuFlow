package launcher

import (
 "context"
 "net"
 "testing"
 "time"

 "github.com/IBM/sarama"
)
func TestGroupOwnershipAndReadiness(t *testing.T){
 absent:=&sarama.GroupDescription{Err:sarama.ErrGroupIDNotFound}
 if err:=validateGroup(absent,"",false);err!=nil{t.Fatal("new group should be allowed",err)}
 if err:=validateGroup(absent,"owned",true);err==nil{t.Fatal("absent group is not ready")}
 g:=&sarama.GroupDescription{Members:map[string]*sarama.GroupMemberDescription{"m":{ClientId:"foreign"}}}
 if err:=validateGroup(g,"owned",true);err==nil{t.Fatal("unknown member accepted")}
 g.Members["m"].ClientId="danmakuflow-owned";if err:=validateGroup(g,"owned",true);err!=nil{t.Fatal(err)}
 if err:=validateGroup(g,"",false);err==nil{t.Fatal("existing consumer allowed to be duplicated")}
}
func TestNativeMySQLReadinessDoesNotRequireApplicationSchema(t *testing.T){
 listener,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)};defer listener.Close()
 done:=make(chan struct{});go func(){defer close(done);c,e:=listener.Accept();if e!=nil{return};defer c.Close();_,_=c.Write([]byte{1,0,0,0,10});_ =c.SetReadDeadline(time.Now().Add(time.Second));b:=make([]byte,1);if n,_:=c.Read(b);n!=0{t.Error("readiness sent database commands")}}()
 ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel();if err:=mysqlGreeting(ctx,listener.Addr().String());err!=nil{t.Fatal(err)};<-done
}
