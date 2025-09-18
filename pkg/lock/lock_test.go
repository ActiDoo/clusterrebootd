package lock

import (
	"context"
	"testing"
	"time"
)

func TestNoopManagerAcquire(t *testing.T) {
	manager := NewNoopManager()
	ctx := context.Background()
	lease, err := manager.Acquire(ctx)
	if err != nil {
		t.Fatalf("expected acquire to succeed, got error: %v", err)
	}
	if lease == nil {
		t.Fatal("expected lease to be non-nil")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("expected release to succeed, got error: %v", err)
	}
}

func TestNoopManagerAcquireContextCancelled(t *testing.T) {
	manager := NewNoopManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Acquire(ctx); err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestNoopManagerReleaseIgnoresContextDeadline(t *testing.T) {
	manager := NewNoopManager()
	lease, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatalf("unexpected acquire error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if err := lease.Release(ctx); err != nil {
		t.Fatalf("expected release to succeed even when context expired, got %v", err)
	}
}
