package infra

import (
	"reflect"
	"testing"
)

func TestRedisAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want string
	}{
		{name: "empty uses host default", env: "", want: "127.0.0.1:6379"},
		{name: "whitespace uses host default", env: " \t\n", want: "127.0.0.1:6379"},
		{name: "container address", env: "redis:6379", want: "redis:6379"},
		{name: "trims surrounding whitespace", env: " \tredis:6380\n", want: "redis:6380"},
		{name: "preserves IPv6 address", env: " [::1]:6380 ", want: "[::1]:6380"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DANMAKUFLOW_REDIS_ADDR", tc.env)
			if got := RedisAddress(); got != tc.want {
				t.Fatalf("RedisAddress() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKafkaBrokers(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want []string
	}{
		{name: "empty uses host default", env: "", want: []string{"127.0.0.1:9092"}},
		{name: "whitespace uses host default", env: " \t\n", want: []string{"127.0.0.1:9092"}},
		{name: "empty entries use host default", env: " , ,\t,\n", want: []string{"127.0.0.1:9092"}},
		{name: "container address", env: "kafka:29092", want: []string{"kafka:29092"}},
		{name: "preserves broker order and skips empty entries", env: " , kafka-b:29092,\t,kafka-a:29093, ", want: []string{"kafka-b:29092", "kafka-a:29093"}},
		{name: "preserves IPv6 address", env: " [::1]:9092, kafka:29092 ", want: []string{"[::1]:9092", "kafka:29092"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DANMAKUFLOW_KAFKA_BROKERS", tc.env)
			if got := KafkaBrokers(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("KafkaBrokers() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInitRedisClientUsesConfiguredAddress(t *testing.T) {
	t.Setenv("DANMAKUFLOW_REDIS_ADDR", " redis:6380 ")
	// Client construction is lazy; inspecting options performs no network I/O.
	client := InitRedisClient()
	t.Cleanup(func() { _ = client.Close() })
	if got := client.Options().Addr; got != "redis:6380" {
		t.Fatalf("Redis client address = %q, want redis:6380", got)
	}
}
