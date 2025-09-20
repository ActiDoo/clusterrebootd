package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
)

// SinglePassRunner abstracts the single-pass orchestration runner for reuse in the loop.
type SinglePassRunner interface {
	RunOnce(ctx context.Context) (Outcome, error)
}

// Loop drives repeated orchestration passes until a reboot is triggered or the context is cancelled.
type Loop struct {
	cfg           *config.Config
	runner        SinglePassRunner
	executor      CommandExecutor
	interval      time.Duration
	sleep         func(time.Duration)
	iterationHook func(Outcome)
	errorHandler  func(error)
	errorBackoff  time.Duration
	errorMinDelay time.Duration
	errorMaxDelay time.Duration
	rnd           *rand.Rand
}

// LoopOption customises loop behaviour.
type LoopOption func(*Loop)

// WithLoopSleepFunc overrides the sleep implementation between iterations.
func WithLoopSleepFunc(fn func(time.Duration)) LoopOption {
	return func(l *Loop) {
		l.sleep = fn
	}
}

// WithLoopIterationHook registers a callback invoked after each successful iteration.
func WithLoopIterationHook(fn func(Outcome)) LoopOption {
	return func(l *Loop) {
		l.iterationHook = fn
	}
}

// WithLoopInterval forces a custom interval between iterations, overriding the configuration value.
func WithLoopInterval(d time.Duration) LoopOption {
	return func(l *Loop) {
		l.interval = d
	}
}

// WithLoopErrorHandler registers a callback for retryable orchestration errors.
func WithLoopErrorHandler(fn func(error)) LoopOption {
	return func(l *Loop) {
		l.errorHandler = fn
	}
}

// WithLoopErrorBackoff overrides the retry backoff window applied after errors.
func WithLoopErrorBackoff(min, max time.Duration) LoopOption {
	return func(l *Loop) {
		l.errorMinDelay = min
		l.errorMaxDelay = max
	}
}

// WithLoopRandSource overrides the random source used for jittering error backoff delays.
func WithLoopRandSource(src rand.Source) LoopOption {
	return func(l *Loop) {
		if src != nil {
			l.rnd = rand.New(src)
		}
	}
}

// NewLoop constructs a Loop backed by the provided runner and executor.
func NewLoop(cfg *config.Config, runner SinglePassRunner, executor CommandExecutor, opts ...LoopOption) (*Loop, error) {
	if cfg == nil {
		return nil, errors.New("config must not be nil")
	}
	if runner == nil {
		return nil, errors.New("runner must not be nil")
	}
	if executor == nil {
		executor = NewExecCommandExecutor(nil, nil)
	}

	loop := &Loop{
		cfg:           cfg,
		runner:        runner,
		executor:      executor,
		interval:      cfg.CheckInterval(),
		sleep:         time.Sleep,
		errorMinDelay: 5 * time.Second,
		errorMaxDelay: time.Minute,
	}

	for _, opt := range opts {
		opt(loop)
	}

	if loop.sleep == nil {
		loop.sleep = time.Sleep
	}
	if loop.interval <= 0 {
		loop.interval = cfg.CheckInterval()
	}
	if loop.interval <= 0 {
		loop.interval = time.Minute
	}
	if loop.errorMinDelay <= 0 {
		loop.errorMinDelay = 5 * time.Second
	}
	if loop.errorMaxDelay <= 0 {
		loop.errorMaxDelay = loop.errorMinDelay
	}
	if loop.errorMaxDelay < loop.errorMinDelay {
		loop.errorMaxDelay = loop.errorMinDelay
	}
	if loop.rnd == nil {
		loop.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	return loop, nil
}

// Run executes the orchestration loop until a reboot command is invoked, context is cancelled, or an error occurs.
func (l *Loop) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		outcome, err := l.runner.RunOnce(ctx)
		if err != nil {
			if releaseErr := outcome.ReleaseLock(context.Background()); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			if l.errorHandler != nil {
				l.errorHandler(err)
			}
			if delay := l.nextErrorDelay(); delay > 0 {
				if sleepErr := l.sleepWithContext(ctx, delay); sleepErr != nil {
					return sleepErr
				}
			}
			continue
		}
		l.resetErrorBackoff()

		if l.iterationHook != nil {
			l.iterationHook(outcome)
		}

		if outcome.Status == OutcomeReady {
			if outcome.DryRun || len(outcome.Command) == 0 {
				if releaseErr := outcome.ReleaseLock(context.Background()); releaseErr != nil {
					return releaseErr
				}
				return nil
			}
			if err := outcome.startCooldown(ctx); err != nil {
				releaseErr := outcome.ReleaseLock(context.Background())
				if releaseErr != nil {
					return errors.Join(err, releaseErr)
				}
				return err
			}
			if err := l.executor.Execute(ctx, outcome.Command); err != nil {
				// Keep the cooldown marker active so other nodes continue to back off; the reboot
				// command may still be in progress even though it reported an error.
				execErr := fmt.Errorf("execute reboot command: %w", err)
				releaseErr := outcome.ReleaseLock(context.Background())
				if releaseErr != nil {
					return errors.Join(execErr, releaseErr)
				}
				return execErr
			}
			return nil
		}

		if releaseErr := outcome.ReleaseLock(context.Background()); releaseErr != nil {
			return releaseErr
		}

		if err := l.sleepWithContext(ctx, l.interval); err != nil {
			return err
		}
	}
}

func (l *Loop) nextErrorDelay() time.Duration {
	if l.errorMinDelay <= 0 {
		return 0
	}
	if l.errorBackoff <= 0 {
		l.errorBackoff = l.errorMinDelay
	} else {
		l.errorBackoff *= 2
		if l.errorBackoff < l.errorMinDelay {
			l.errorBackoff = l.errorMinDelay
		}
	}
	if l.errorBackoff > l.errorMaxDelay {
		l.errorBackoff = l.errorMaxDelay
	}
	if l.errorBackoff <= l.errorMinDelay {
		return l.errorBackoff
	}
	jitterRange := l.errorBackoff - l.errorMinDelay
	if jitterRange <= 0 {
		return l.errorBackoff
	}
	if l.rnd == nil {
		l.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	jitter := time.Duration(l.rnd.Int63n(int64(jitterRange) + 1))
	return l.errorMinDelay + jitter
}

func (l *Loop) resetErrorBackoff() {
	l.errorBackoff = 0
}

func (l *Loop) sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	done := make(chan struct{})
	go func() {
		l.sleep(d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
