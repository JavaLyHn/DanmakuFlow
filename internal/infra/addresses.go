package infra

import (
	"os"
	"strings"
)

// RedisAddress uses an explicit container or host address when configured.
func RedisAddress() string {
	if addr := strings.TrimSpace(os.Getenv("DANMAKUFLOW_REDIS_ADDR")); addr != "" {
		return addr
	}
	return RedisAddr
}

// KafkaBrokers preserves the configured broker order and ignores empty entries.
func KafkaBrokers() []string {
	var brokers []string
	for _, addr := range strings.Split(os.Getenv("DANMAKUFLOW_KAFKA_BROKERS"), ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			brokers = append(brokers, addr)
		}
	}
	if len(brokers) == 0 {
		return []string{"127.0.0.1:9092"}
	}
	return brokers
}
