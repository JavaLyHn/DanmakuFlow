package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/JavaLyHn/DanmakuFlow/internal/launcher"
	"github.com/JavaLyHn/DanmakuFlow/internal/localproc"
	"github.com/JavaLyHn/DanmakuFlow/internal/webui"
	"github.com/gin-gonic/gin"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	configPath := flag.String("config", ".local/launcher.json", "local configuration")
	stateDir := flag.String("state-dir", ".local/runtime", "private runtime directory")
	webDir := flag.String("web-dir", "web/dist", "built frontend")
	consoleStop := flag.String("console-stop", "", "internal console helper")
	flag.Parse()
	if *consoleStop != "" {
		return localproc.ConsoleHelper(*consoleStop)
	}
	cfgPath, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(*stateDir)
	if err != nil {
		return err
	}
	// Binding first guarantees that a second launcher cannot race its state writes.
	listener, err := net.Listen("tcp4", "127.0.0.1:4174")
	if err != nil {
		return fmt.Errorf("本地页面端口 4174 不可用，未启动任何后端：%w", err)
	}
	defer listener.Close()
	if err = localproc.SecureDir(dir); err != nil {
		return err
	}
	config, configErr := launcher.LoadConfig(cfgPath)
	runtime := launcher.NewRuntime(config, configErr, dir)
	manager, err := launcher.NewManager(runtime, filepath.Join(dir, "instances.json"), runtime.ConfigKey())
	if err != nil {
		return err
	}
	api, err := launcher.NewAPI(manager, runtime.LogTail, cfgPath)
	if err != nil {
		return err
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Any("/api/local/*path", gin.WrapH(api))
	if err = webui.Register(router, *webDir); err != nil {
		return fmt.Errorf("页面尚未构建，请运行 scripts/build-local.ps1：%w", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:4174" {
			http.Error(w, "invalid local host", http.StatusForbidden)
			return
		}
		router.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				probe, done := context.WithTimeout(ctx, 8*time.Second)
				manager.Refresh(probe)
				done()
				runtime.TrimLogs()
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Println("DanmakuFlow local page: http://127.0.0.1:4174/ (services start only when requested)")
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
