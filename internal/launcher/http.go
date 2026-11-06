package launcher

import (
 "crypto/subtle"
 "encoding/json"
 "io"
 "net/http"
 "strings"
)

const localOrigin="http://127.0.0.1:4174"
type API struct{manager *Manager;token,configPath string;logs func(string)(string,error)}
func NewAPI(m *Manager,logs func(string)(string,error),configPath string)(*API,error){token,err:=randomID();if err!=nil{return nil,err};return &API{manager:m,token:token,configPath:configPath,logs:logs},nil}
func jsonReply(w http.ResponseWriter,status int,value any){w.Header().Set("Content-Type","application/json; charset=utf-8");w.Header().Set("Cache-Control","no-store");w.Header().Set("X-Content-Type-Options","nosniff");w.WriteHeader(status);_ =json.NewEncoder(w).Encode(value)}
func apiError(w http.ResponseWriter,code int,text string){jsonReply(w,code,map[string]string{"error":text})}
func(a *API)ServeHTTP(w http.ResponseWriter,r *http.Request){
 if r.Host!="127.0.0.1:4174"||(r.Header.Get("Origin")!=""&&r.Header.Get("Origin")!=localOrigin)||r.Header.Get("Sec-Fetch-Site")=="cross-site"{apiError(w,403,"拒绝非本地页面请求");return}
 path:=r.URL.Path
 if r.Method==http.MethodGet&&(path=="/api/local/session"||path=="/api/local/status"){
  if path=="/api/local/session"{jsonReply(w,200,map[string]any{"kind":"danmakuflow-launcher","token":a.token,"port":8080,"configPath":a.configPath});return};jsonReply(w,200,a.manager.Snapshot());return
 }
 if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-DanmakuFlow-Token")),[]byte(a.token))!=1{apiError(w,403,"启动器会话已失效，请点击重新连接启动器");return}
 if r.Method==http.MethodGet&&strings.HasPrefix(path,"/api/local/logs/"){
  id:=strings.TrimPrefix(path,"/api/local/logs/");if _,ok:=labels[id];!ok{apiError(w,404,"未知组件");return};text,err:=a.logs(id);if err!=nil{apiError(w,500,"无法读取该组件日志");return};jsonReply(w,200,map[string]string{"text":text});return
 }
 if path!="/api/local/start"&&path!="/api/local/stop"{apiError(w,404,"未提供该操作");return}
 if r.Method!=http.MethodPost{apiError(w,405,"请使用页面上的启停按钮");return}
 if r.Header.Get("Origin")!=localOrigin{apiError(w,403,"缺少本地页面来源");return}
 b,err:=io.ReadAll(io.LimitReader(r.Body,1));if err!=nil||len(b)!=0||r.URL.RawQuery!=""{apiError(w,400,"启停操作不接受命令、路径或参数");return}
 if path=="/api/local/start"{err=a.manager.Start()}else{err=a.manager.Stop()}
 if err!=nil{apiError(w,409,err.Error());return};jsonReply(w,http.StatusAccepted,a.manager.Snapshot())
}
