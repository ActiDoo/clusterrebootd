package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu        sync.Mutex
	enabled   bool
	publishCh chan struct{}
	calls     int
	errs      []error
}

func (f *fakeRunner) HealthPublishingEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

func (f *fakeRunner) PublishHeartbeat(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	f.mu.Lock()
	f.calls++
	err := error(nil)
	if len(f.errs) > 0 {
		err = f.errs[0]
		f.errs = f.errs[1:]
	}
	ch := f.publishCh
	f.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return err
}

func TestNewHealthPublisherRequiresRunner(t *testing.T) {
	if _, err := NewHealthPublisher(nil, time.Second); err == nil {
		t.Fatal("expected error when runner is nil")
	}
}

func TestHealthPublisherPublishesUntilCancelled(t *testing.T) {
	runner := &fakeRunner{
		enabled:   true,
		publishCh: make(chan struct{}, 4),
	}
	publisher, err := NewHealthPublisher(runner, 10*time.Millisecond,
		WithHealthPublisherSleepFunc(func(time.Duration) { time.Sleep(time.Millisecond) }),
	)
	if err != nil {
		t.Fatalf("unexpected error creating publisher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- publisher.Run(ctx) }()

	for i := 0; i < 2; i++ {
		select {
		case <-runner.publishCh:
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for heartbeat %d", i+1)
		}
	}
	cancel()

	select {
	case runErr := <-errCh:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for publisher to exit")
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls < 2 {
		t.Fatalf("expected at least two heartbeat publications, got %d", runner.calls)
	}
}

func TestHealthPublisherInvokesErrorHandler(t *testing.T) {
	runner := &fakeRunner{
		enabled:   true,
		publishCh: make(chan struct{}, 1),
		errs:      []error{errors.New("boom")},
	}
	errCh := make(chan error, 1)
	publisher, err := NewHealthPublisher(runner, 10*time.Millisecond,
		WithHealthPublisherSleepFunc(func(time.Duration) { time.Sleep(time.Millisecond) }),
		WithHealthPublisherErrorHandler(func(err error) { errCh <- err }),
	)
	if err != nil {
		t.Fatalf("unexpected error creating publisher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- publisher.Run(ctx) }()

	select {
	case <-runner.publishCh:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for heartbeat")
	}

	select {
	case handlerErr := <-errCh:
		if handlerErr == nil || handlerErr.Error() != "boom" {
			t.Fatalf("unexpected handler error: %v", handlerErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for error handler invocation")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("publisher did not exit after cancellation")
	}
}

func TestHealthPublisherSkipsWhenDisabled(t *testing.T) {
	runner := &fakeRunner{enabled: false}
	sleepCh := make(chan struct{}, 2)
	publisher, err := NewHealthPublisher(runner, 5*time.Millisecond,
		WithHealthPublisherSleepFunc(func(time.Duration) {
			sleepCh <- struct{}{}
			time.Sleep(time.Millisecond)
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error creating publisher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	select {
	case <-sleepCh:
	case <-time.After(time.Second):
		t.Fatal("expected sleep invocation when publishing disabled")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher did not exit after cancellation")
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 0 {
		t.Fatalf("expected no heartbeat publications, got %d", runner.calls)
	}
}
