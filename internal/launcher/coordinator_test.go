package launcher

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/IBM/sarama"
)

func TestCoordinatorCheckWaitsForTransientInitialization(t *testing.T) {
	responses := []error{
		sarama.ErrConsumerCoordinatorNotAvailable,
		fmt.Errorf("loading: %w", sarama.ErrOffsetsLoadInProgress),
		sarama.ErrNotCoordinatorForConsumer,
		nil,
	}
	attempts := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := retryCoordinatorCheck(ctx, time.Millisecond, func() error {
		result := responses[attempts]
		attempts++
		return result
	})
	if err != nil || attempts != 4 {
		t.Fatalf("coordinator initialization was not retried: attempts=%d err=%v", attempts, err)
	}
}

func TestCoordinatorCheckRejectsPermanentErrors(t *testing.T) {
	foreign := validateGroup(&sarama.GroupDescription{Members: map[string]*sarama.GroupMemberDescription{
		"member": {ClientId: "another-consumer"},
	}}, "", false)
	for _, failure := range []error{foreign, sarama.ErrGroupAuthorizationFailed, sarama.ErrUnsupportedVersion, sarama.ErrOutOfBrokers} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		attempts := 0
		err := retryCoordinatorCheck(ctx, time.Millisecond, func() error {
			attempts++
			return failure
		})
		cancel()
		if !errors.Is(err, failure) || attempts != 1 {
			t.Fatalf("permanent check failure was retried or ignored: attempts=%d err=%v want=%v", attempts, err, failure)
		}
	}
}

func TestCoordinatorCheckHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err := retryCoordinatorCheck(ctx, time.Hour, func() error {
		attempts++
		return nil
	})
	if !errors.Is(err, context.Canceled) || attempts != 0 {
		t.Fatalf("canceled check still executed: attempts=%d err=%v", attempts, err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	err = retryCoordinatorCheck(ctx, time.Hour, func() error {
		attempts++
		cancel()
		return sarama.ErrConsumerCoordinatorNotAvailable
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("cancellation did not interrupt coordinator backoff: attempts=%d err=%v", attempts, err)
	}
}

func TestCoordinatorCheckStopsAtDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	attempts := 0
	err := retryCoordinatorCheck(ctx, time.Hour, func() error {
		attempts++
		return sarama.ErrConsumerCoordinatorNotAvailable
	})
	if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 {
		t.Fatalf("deadline did not stop coordinator retry: attempts=%d err=%v", attempts, err)
	}
}
