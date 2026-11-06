package main

import (
	"flag" // command line arguments
 "context"
 "errors"
 "net"
 "net/http"
 "os"
 "os/signal"
 "syscall"
 "time"
 "github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"

	"github.com/JavaLyHn/DanmakuFlow/internal/api"
	"github.com/JavaLyHn/DanmakuFlow/internal/infra"
	"github.com/JavaLyHn/DanmakuFlow/internal/logger"
	"github.com/JavaLyHn/DanmakuFlow/internal/repo"
	"github.com/JavaLyHn/DanmakuFlow/internal/service"
	"github.com/JavaLyHn/DanmakuFlow/internal/webui"
	"github.com/JavaLyHn/DanmakuFlow/internal/ws"
	"github.com/gin-gonic/gin" // Gin is a web framework for Go.
	"go.uber.org/zap"
)

func main() {
	// 1. Initialize Logger
	// Use "dev" for colored output, "prod" for JSON.
	logger.InitLogger("dev")
	defer logger.Sync()

	// Define command-line flag for port. Default is 8080.
	// port is argument name,8080 is default value, "server port" is help message
	// port is in *string type
	port := flag.String("port", "8080", "server port")
	webDir := flag.String("web-dir", "web/dist", "built frontend directory (optional)")
	//resolve arguments,port is given value
	flag.Parse()
	logger.Log.Info("[SERVER]Starting server...", zap.String("port", *port))

	child, err := runcontrol.FromEnv("server")
 if err != nil { logger.Log.Error("[SERVER] Local lifecycle unavailable"); return }
 //IMPORTANT:
	//initialize Global WebSocket Handlers
	//logic handler functions[Danmaku,Like,Broadcast stats]
	ws.RegisterActionHandlers()

	// Connect only; normal startup must never initialize or reset tables.
	db, err := infra.OpenDB()
	if err != nil {
		logger.Log.Fatal("[SERVER]Database connection failed", zap.Error(err))
	}
	sqlDB, err := db.DB()
	if err != nil {
		logger.Log.Fatal("[SERVER]Database pool unavailable", zap.Error(err))
	}
	defer sqlDB.Close()
	if err := infra.CheckSchema(db); err != nil {
		_ = sqlDB.Close() // Fatal exits without running deferred functions.
		logger.Log.Fatal("[SERVER]Database schema check failed", zap.Error(err))
	}
	// 2. Init Layers (Dependency Injection)
	// Repo -> Service -> Handler
	messageRepo := repo.NewMessageRepo(db)
	chatService := service.NewChatService(messageRepo)
	// 3. Init WebSocket Manager
	manager := ws.NewManager()
	go manager.Start()

	// 4. Setup API Handlers (Dependency Injection)
	// We pass the manager and service into the handler.
	chatHandler := api.NewChatHandler(chatService, manager)

	// 4. Init Router
	// r := gin.Default()
	//blank slate
	r := gin.New()
	// Manually attach Recovery middleware (Critical for preventing server crash on panic)
	r.Use(gin.Recovery())

	// WebSocket Route
	r.GET("/ws", chatHandler.ConnectWebSocket)
	if err := webui.Register(r, *webDir); err != nil {
		logger.Log.Warn("[SERVER]Web UI unavailable; build web/ or set -web-dir. Terminal client remains available",
			zap.String("directory", *webDir), zap.Error(err))
	}

	addr := ":" + *port
 listener, err := net.Listen("tcp", addr)
 if err != nil {
  child.Report("failed", errors.New("server listener unavailable"))
  logger.Log.Error("[SERVER] Listener unavailable")
  ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
  defer cancel()
  if stopErr:=manager.Shutdown(ctx);stopErr!=nil{logger.Log.Error("[SERVER] Startup cleanup failed",zap.Error(stopErr))}
  return
 }
 server:= &http.Server{Handler:r,ReadHeaderTimeout:10*time.Second}
 serveResult:=make(chan error,1)
 child.Report("ready",nil)
 if child.Err()!=nil { _=listener.Close();ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second);defer cancel();_ =manager.Shutdown(ctx);logger.Log.Error("[SERVER] Could not persist readiness; admission disabled");return }
 go func(){serveResult<-server.Serve(listener)}()
 logger.Log.Info("[SERVER] Server listening",zap.String("address",addr))
 lifecycleCtx,cancelLifecycle:=context.WithCancel(context.Background())
 defer cancelLifecycle()
 stopGate:=make(chan struct{},1)
 stopped:=false
 stop:=func(ctx context.Context,_ runcontrol.Request)error{
  select{case stopGate<-struct{}{}:case <-ctx.Done():return ctx.Err()}
  defer func(){<-stopGate}()
  if stopped{return nil}
  manager.Quiesce()
  httpErr:=server.Shutdown(ctx)
  managerErr:=manager.Shutdown(ctx)
  if err:=errors.Join(httpErr,managerErr);err!=nil{return err}
  if err:=sqlDB.Close();err!=nil{return errors.New("database pool close failed")}
  stopped=true
  return nil
 }
 controlResult:=make(chan error,1)
 go func(){controlResult<-child.Serve(lifecycleCtx,stop)}()
 signals:=make(chan os.Signal,1)
 signal.Notify(signals,os.Interrupt,syscall.SIGTERM)
 defer signal.Stop(signals)
 for{select{
 case err:=<-controlResult:
  if err==nil{return}
  logger.Log.Error("[SERVER] Local lifecycle watcher failed",zap.Error(err))
  controlResult=nil
 case err:=<-serveResult:
  serveResult=nil
  if err!=nil&&!errors.Is(err,http.ErrServerClosed){child.Report("failed",errors.New("HTTP serving failed"));logger.Log.Error("[SERVER] HTTP serving failed")}
 case <-signals:
  child.Report("stopping",nil)
  ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
  err:=stop(ctx,runcontrol.Request{});cancel()
  if err==nil{child.Report("stopped",nil);return}
  child.Report("stop_failed",err)
  logger.Log.Error("[SERVER] Stop incomplete; process retained for retry",zap.Error(err))
 }}
}
