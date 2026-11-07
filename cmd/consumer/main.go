package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/IBM/sarama"
	"github.com/JavaLyHn/DanmakuFlow/internal/consumer"
	"github.com/JavaLyHn/DanmakuFlow/internal/infra"
	"github.com/JavaLyHn/DanmakuFlow/internal/logger"
	"github.com/JavaLyHn/DanmakuFlow/internal/repo"
	"github.com/JavaLyHn/DanmakuFlow/internal/runcontrol"
	"go.uber.org/zap"
)

const (
	KafkaTopic   = "danmaku_save_topic"
	KafkaGroupID = "danmaku_group_v1"
	BatchSize    = 1000
)

type offsetReader func(context.Context, map[int32]int64) (map[int32]int64, error)

// Both the private watcher and OS signals share the same shutdown transaction.
func serializedStop(stop func(context.Context, runcontrol.Request) error) func(context.Context, runcontrol.Request) error {
	gate := make(chan struct{}, 1)
	return func(ctx context.Context, req runcontrol.Request) error {
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		defer func() { <-gate }()
		if err := ctx.Err(); err != nil {
			return err
		}
		return stop(ctx, req)
	}
}
func awaitShutdown(signals <-chan os.Signal, control <-chan error, stop func(context.Context, runcontrol.Request) error, report func(string, error)) {
	for {
		select {
		case err := <-control:
			if err == nil {
				return
			}
			report("control_failed", err)
			control = nil
		case <-signals:
			report("stopping", nil)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := stop(ctx, runcontrol.Request{})
			cancel()
			if err == nil {
				report("stopped", nil)
				return
			}
			report("stop_failed", err)
		}
	}
}
func waitTargets(ctx context.Context, targets map[int32]int64, read offsetReader, interval time.Duration) error {
	if len(targets) == 0 {
		return nil
	}
	var last error
	for {
		offsets, err := read(ctx, targets)
		if err == nil {
			complete := true
			for p, target := range targets {
				n, ok := offsets[p]
				if !ok || !offsetReached(n, target) {
					complete = false
					break
				}
			}
			if complete {
				return nil
			}
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			if last != nil {
				return fmt.Errorf("waiting for committed offsets: %v: %w", last, ctx.Err())
			}
			return fmt.Errorf("waiting for committed offsets: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}
func offsetReached(offset, target int64) bool {
	return offset >= target || (offset == -1 && target == 0)
}
func offsetRequest() *sarama.OffsetFetchRequest {
	return &sarama.OffsetFetchRequest{Version: 1, ConsumerGroup: KafkaGroupID}
}
func brokerOffsets(client sarama.Client) offsetReader {
	return func(ctx context.Context, targets map[int32]int64) (map[int32]int64, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		coordinator, err := client.Coordinator(KafkaGroupID)
		if err != nil {
			return nil, err
		}
		request := offsetRequest()
		for p := range targets {
			request.AddPartition(KafkaTopic, p)
		}
		response, err := coordinator.FetchOffset(request)
		if err != nil {
			_ = client.RefreshCoordinator(KafkaGroupID)
			return nil, err
		}
		out := make(map[int32]int64, len(targets))
		for p := range targets {
			block := response.GetBlock(KafkaTopic, p)
			if block == nil {
				return nil, fmt.Errorf("missing committed offset for partition %d", p)
			}
			if block.Err != sarama.ErrNoError {
				if block.Err == sarama.ErrNotCoordinatorForConsumer {
					_ = client.RefreshCoordinator(KafkaGroupID)
				}
				return nil, block.Err
			}
			out[p] = block.Offset
		}
		return out, nil
	}
}
func main() {
	logger.InitLogger("dev")
	defer logger.Sync()
	child, err := runcontrol.FromEnv("consumer")
	if err != nil {
		logger.Log.Fatal("lifecycle configuration", zap.Error(err))
	}
	db, err := infra.OpenDB()
	if err != nil {
		logger.Log.Fatal("database connection", zap.Error(err))
	}
	sqlDB, err := db.DB()
	if err != nil {
		logger.Log.Fatal("database pool", zap.Error(err))
	}
	if err = infra.CheckSchema(db); err != nil {
		_ = sqlDB.Close()
		logger.Log.Fatal("database schema", zap.Error(err))
	}
	config := sarama.NewConfig()
	config.Consumer.Offsets.Initial = sarama.OffsetOldest
	config.Consumer.Fetch.Default = 5 * 1024 * 1024
	config.Consumer.Return.Errors = true
	config.Net.DialTimeout = time.Second
	config.Net.ReadTimeout = time.Second
	config.Net.WriteTimeout = time.Second
	config.ClientID = "danmakuflow-standalone"
	if id := os.Getenv("DANMAKUFLOW_RUN_ID"); id != "" {
		config.ClientID = "danmakuflow-" + id
	}
	client, err := sarama.NewClient(infra.KafkaBrokers(), config)
	if err != nil {
		logger.Log.Fatal("Kafka client", zap.Error(err))
	}
	group, err := sarama.NewConsumerGroupFromClient(KafkaGroupID, client)
	if err != nil {
		logger.Log.Fatal("Kafka consumer group", zap.Error(err))
	}
	go func() {
		for err := range group.Errors() {
			logger.Log.Error("Kafka consumer error", zap.Error(err))
		}
	}()
	handler := consumer.NewDanmakuHandler(repo.NewMessageRepo(db), BatchSize)
	handler.Ready = func() {
		child.Report("ready", nil)
		if child.Err() != nil {
			logger.Log.Fatal("could not persist readiness; admission disabled")
		}
	}
	handler.NotReady = func() { child.Report("starting", nil) }
	consumeCtx, cancelConsume := context.WithCancel(context.Background())
	consumeDone := make(chan error, 1)
	go func() {
		for {
			err := group.Consume(consumeCtx, []string{KafkaTopic}, handler)
			if consumeCtx.Err() != nil {
				if errors.Is(err, context.Canceled) {
					err = nil
				}
				consumeDone <- err
				return
			}
			if err != nil {
				logger.Log.Error("consume", zap.Error(err))
				select {
				case <-consumeCtx.Done():
					consumeDone <- err
					return
				case <-time.After(time.Second):
				}
			}
		}
	}()
	var mu sync.Mutex
	var shutdownDone chan struct{}
	var shutdownErr error
	stop := serializedStop(func(ctx context.Context, req runcontrol.Request) error {
		mu.Lock()
		done := shutdownDone
		mu.Unlock()
		if done == nil {
			if err := waitTargets(ctx, req.Targets, brokerOffsets(client), 200*time.Millisecond); err != nil {
				return err
			}
			group.PauseAll()
			finalTargets, err := handler.Quiesce(ctx)
			if err != nil {
				handler.Resume()
				group.ResumeAll()
				return err
			}
			if err = waitTargets(ctx, finalTargets, brokerOffsets(client), 200*time.Millisecond); err != nil {
				handler.Resume()
				group.ResumeAll()
				return err
			}
			mu.Lock()
			if shutdownDone == nil {
				shutdownDone = make(chan struct{})
				cancelConsume()
				go func() {
					consumeErr := <-consumeDone
					closeErr := group.Close()
					clientErr := client.Close()
					dbErr := sqlDB.Close()
					mu.Lock()
					shutdownErr = errors.Join(consumeErr, closeErr, clientErr, dbErr)
					close(shutdownDone)
					mu.Unlock()
				}()
			}
			done = shutdownDone
			mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			mu.Lock()
			defer mu.Unlock()
			return shutdownErr
		}
	})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	controlCtx, controlCancel := context.WithCancel(context.Background())
	defer controlCancel()
	controlDone := make(chan error, 1)
	if child != nil {
		go func() { controlDone <- child.Serve(controlCtx, stop) }()
	}
	awaitShutdown(sig, controlDone, stop, func(phase string, err error) {
		if phase != "control_failed" {
			child.Report(phase, err)
		}
		if err != nil {
			logger.Log.Error("consumer lifecycle", zap.String("phase", phase), zap.Error(err))
		}
	})
}
