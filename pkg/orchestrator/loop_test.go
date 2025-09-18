package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type loopStep struct {
	outcome Outcome
	err     error
}

type fakeLoopRunner struct {
	mu    sync.Mutex
	steps []loopStep
	idx   int
	calls int
}

func (f *fakeLoopRunner) RunOnce(ctx context.Context) (Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.steps) == 0 {
		return Outcome{}, nil
	}
	if f.idx >= len(f.steps) {
		last := f.steps[len(f.steps)-1]
		return last.outcome, last.err
	}
	step := f.steps[f.idx]
	f.idx++
	return step.outcome, step.err
}

type fakeExecutor struct {
	mu       sync.Mutex
	commands [][]string
	err      error
}

func (f *fakeExecutor) Execute(ctx context.Context, command []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, append([]string(nil), command...))
	return f.err
}

func TestLoopExecutesCommandWhenReady(t *testing.T) {
	cfg := baseConfig()
	cfg.DryRun = false
	runner := &fakeLoopRunner{steps: []loopStep{
		{outcome: Outcome{Status: OutcomeNoAction}},
		{outcome: Outcome{Status: OutcomeReady, Command: cfg.RebootCommand}},
	}}
	executor := &fakeExecutor{}

	loop, err := NewLoop(cfg, runner, executor, WithLoopInterval(0), WithLoopSleepFunc(func(time.Duration) {}))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}

	if runner.calls != 2 {
		t.Fatalf("expected 2 runner calls, got %d", runner.calls)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.commands) != 1 {
		t.Fatalf("expected executor to be invoked once, got %d", len(executor.commands))
	}
	if got := executor.commands[0]; len(got) != len(cfg.RebootCommand) {
		t.Fatalf("unexpected command captured: %v", got)
	}
}

func TestLoopStopsOnDryRunReady(t *testing.T) {
	cfg := baseConfig()
	cfg.DryRun = true
	runner := &fakeLoopRunner{steps: []loopStep{{outcome: Outcome{Status: OutcomeReady, DryRun: true, Command: cfg.RebootCommand}}}}
	executor := &fakeExecutor{}

	loop, err := NewLoop(cfg, runner, executor)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.commands) != 0 {
		t.Fatalf("expected executor not to run in dry-run mode, got %d calls", len(executor.commands))
	}
}

func TestLoopRespectsContextCancellation(t *testing.T) {
	cfg := baseConfig()
	runner := &fakeLoopRunner{steps: []loopStep{{outcome: Outcome{Status: OutcomeNoAction}}}}
	executor := &fakeExecutor{}

	ctx, cancel := context.WithCancel(context.Background())
	loop, err := NewLoop(cfg, runner, executor, WithLoopInterval(10*time.Second), WithLoopSleepFunc(func(time.Duration) {
		cancel()
	}))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestLoopRetriesOnRunnerError(t *testing.T) {
	cfg := baseConfig()
	firstErr := errors.New("boom")
	runner := &fakeLoopRunner{steps: []loopStep{{err: firstErr}, {outcome: Outcome{Status: OutcomeNoAction}}}}
	executor := &fakeExecutor{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var errCount int
	loop, err := NewLoop(cfg, runner, executor,
		WithLoopInterval(0),
		WithLoopSleepFunc(func(time.Duration) {}),
		WithLoopErrorHandler(func(runErr error) {
			errCount++
			if !errors.Is(runErr, firstErr) {
				t.Fatalf("unexpected error received by handler: %v", runErr)
			}
		}),
		WithLoopIterationHook(func(out Outcome) {
			if out.Status == OutcomeNoAction {
				cancel()
			}
		}),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}

	if runner.calls < 2 {
		t.Fatalf("expected runner to be retried, got %d calls", runner.calls)
	}
	if errCount != 1 {
		t.Fatalf("expected error handler to run once, ran %d times", errCount)
	}
}

func TestLoopAppliesErrorBackoff(t *testing.T) {
	cfg := baseConfig()
	transient := errors.New("transient")
	runner := &fakeLoopRunner{steps: []loopStep{{err: transient}, {err: transient}, {outcome: Outcome{Status: OutcomeNoAction}}}}
	executor := &fakeExecutor{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	sleeps := make([]time.Duration, 0)

	loop, err := NewLoop(cfg, runner, executor,
		WithLoopInterval(0),
		WithLoopSleepFunc(func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			sleeps = append(sleeps, d)
		}),
		WithLoopErrorBackoff(5*time.Millisecond, 20*time.Millisecond),
		WithLoopErrorHandler(func(error) {}),
		WithLoopIterationHook(func(out Outcome) {
			if out.Status == OutcomeNoAction {
				cancel()
			}
		}),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(sleeps) < 2 {
		t.Fatalf("expected at least two sleep intervals, got %v", sleeps)
	}
	if sleeps[0] != 5*time.Millisecond {
		t.Fatalf("expected first backoff to be 5ms, got %v", sleeps[0])
	}
	if sleeps[1] != 10*time.Millisecond {
		t.Fatalf("expected second backoff to double to 10ms, got %v", sleeps[1])
	}
}

func TestNewLoopValidatesInputs(t *testing.T) {
	cfg := baseConfig()
	if _, err := NewLoop(nil, &fakeLoopRunner{}, &fakeExecutor{}); err == nil {
		t.Fatal("expected error when config nil")
	}
	if _, err := NewLoop(cfg, nil, &fakeExecutor{}); err == nil {
		t.Fatal("expected error when runner nil")
	}
}
