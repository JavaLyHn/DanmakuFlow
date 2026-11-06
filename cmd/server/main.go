package main

import (
	"flag" // command line arguments

	"github.com/JavaLyHn/DanmakuFlow/internal/api"
	"github.com/JavaLyHn/DanmakuFlow/internal/infra"
	"github.com/JavaLyHn/DanmakuFlow/internal/logger"
	"github.com/JavaLyHn/DanmakuFlow/internal/repo"
	"github.com/JavaLyHn/DanmakuFlow/internal/service"
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
	//resolve arguments,port is given value
	flag.Parse()
	logger.Log.Info("[SERVER]Starting server...", zap.String("port", *port))

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

	addr := ":" + *port
	if err := r.Run(addr); err != nil {
		logger.Log.Fatal("[SERVER]Server startup failed", zap.Error(err))
	}
	logger.Log.Info("[SERVER]Server listening", zap.String("address", addr))
}
