package orchestrator

import (
	"context"
	"errors"
	"time"
)

type healthPublishingRunner interface {
	HealthPublishingEnabled() bool
	PublishHeartbeat(context.Context) error
}

// HealthPublisher drives a lightweight loop that periodically refreshes the
// cluster health record for the local node.
//
// It executes alongside the main orchestration loop so peers observe fresh
// health information even when no reboot iteration is in progress.
// healthPublishingRunner is intentionally minimal so tests can provide fakes.
type HealthPublisher struct {
	runner       healthPublishingRunner
	interval     time.Duration
	sleep        func(time.Duration)
	errorHandler func(error)
}

// HealthPublisherOption customises the behaviour of the heartbeat loop.
type HealthPublisherOption func(*HealthPublisher)

// WithHealthPublisherSleepFunc overrides the sleep implementation between
// heartbeat publications.
func WithHealthPublisherSleepFunc(fn func(time.Duration)) HealthPublisherOption {
	return func(p *HealthPublisher) {
		if fn != nil {
			p.sleep = fn
		}
	}
}

// WithHealthPublisherErrorHandler registers a callback for heartbeat errors.
func WithHealthPublisherErrorHandler(fn func(error)) HealthPublisherOption {
	return func(p *HealthPublisher) {
		p.errorHandler = fn
	}
}

// NewHealthPublisher constructs a background heartbeat loop.
func NewHealthPublisher(runner healthPublishingRunner, interval time.Duration, opts ...HealthPublisherOption) (*HealthPublisher, error) {
	if runner == nil {
		return nil, errors.New("health publisher requires a runner")
	}
	if interval <= 0 {
		return nil, errors.New("health publish interval must be greater than zero")
	}

	publisher := &HealthPublisher{
		runner:   runner,
		interval: interval,
		sleep:    time.Sleep,
	}
	for _, opt := range opts {
		opt(publisher)
	}
	if publisher.sleep == nil {
		publisher.sleep = time.Sleep
	}
	return publisher, nil
}

// Run executes the heartbeat loop until the context is cancelled.
func (p *HealthPublisher) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !p.runner.HealthPublishingEnabled() {
			if err := p.sleepWithContext(ctx, p.interval); err != nil {
				return err
			}
			continue
		}

		if err := p.runner.PublishHeartbeat(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if p.errorHandler != nil {
				p.errorHandler(err)
			}
		}

		if err := p.sleepWithContext(ctx, p.interval); err != nil {
			return err
		}
	}
}

func (p *HealthPublisher) sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	done := make(chan struct{})
	go func() {
		p.sleep(d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
