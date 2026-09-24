package panel

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingAuditCleanupStore struct {
	calls     atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
	started   chan struct{}
	finished  chan error
	delay     time.Duration
}

func (s *blockingAuditCleanupStore) CleanupAudit(ctx context.Context) error {
	s.calls.Add(1)
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		maximum := s.maxActive.Load()
		if active <= maximum || s.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	select {
	case s.started <- struct{}{}:
	default:
	}

	var err error
	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-timer.C:
		}
	} else {
		<-ctx.Done()
		err = ctx.Err()
	}
	if s.finished != nil {
		select {
		case s.finished <- err:
		default:
		}
	}
	return err
}

func TestAuditRetentionCleanerBoundsSweepAndStopsWithContext(t *testing.T) {
	store := &blockingAuditCleanupStore{started: make(chan struct{}, 1), finished: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAuditRetentionCleaner(ctx, store, time.Hour, 20*time.Millisecond)
	}()

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("initial audit cleanup did not start")
	}
	select {
	case err := <-store.finished:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("audit cleanup did not honor its timeout")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("audit cleaner did not stop after cancellation")
	}
	assert.Equal(t, int32(1), store.maxActive.Load())
}

func TestAuditRetentionCleanerRunsPeriodicallyWithoutOverlap(t *testing.T) {
	store := &blockingAuditCleanupStore{started: make(chan struct{}, 8), delay: 8 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Millisecond)
	defer cancel()
	runAuditRetentionCleaner(ctx, store, 2*time.Millisecond, 30*time.Millisecond)
	assert.GreaterOrEqual(t, store.calls.Load(), int32(2))
	assert.Equal(t, int32(1), store.maxActive.Load())
}
