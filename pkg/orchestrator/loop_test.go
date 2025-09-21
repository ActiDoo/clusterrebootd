package orchestrator

import (
	"context"
	"errors"
	"math/rand"
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

func TestLoopReleasesLockWhenCommandFails(t *testing.T) {
	cfg := baseConfig()
	releaseCalled := false
	runner := &fakeLoopRunner{steps: []loopStep{{outcome: Outcome{
		Status:      OutcomeReady,
		Command:     cfg.RebootCommand,
		lockRelease: func(context.Context) error { releaseCalled = true; return nil },
	}}}}
	executor := &fakeExecutor{err: errors.New("boom")}

	loop, err := NewLoop(cfg, runner, executor, WithLoopInterval(0), WithLoopSleepFunc(func(time.Duration) {}))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if err := loop.Run(context.Background()); err == nil {
		t.Fatal("expected loop to return error when executor fails")
	}
	if !releaseCalled {
		t.Fatal("expected lock release to be invoked on executor failure")
	}
}

func TestLoopStartsCooldownBeforeExecuting(t *testing.T) {
	cfg := baseConfig()
	cfg.DryRun = false
	startCalls := 0
	ready := Outcome{
		Status:  OutcomeReady,
		Command: cfg.RebootCommand,
		cooldownStart: func(context.Context) error {
			startCalls++
			return nil
		},
	}
	runner := &fakeLoopRunner{steps: []loopStep{{outcome: ready}}}
	executor := &fakeExecutor{}

	loop, err := NewLoop(cfg, runner, executor, WithLoopInterval(0), WithLoopSleepFunc(func(time.Duration) {}))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("unexpected loop error: %v", err)
	}
	if startCalls != 1 {
		t.Fatalf("expected cooldown start to be invoked once, got %d", startCalls)
	}
	executor.mu.Lock()
	calls := len(executor.commands)
	executor.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected executor to run once, got %d", calls)
	}
}

func TestLoopKeepsCooldownWhenExecutorFails(t *testing.T) {
	cfg := baseConfig()
	startCalls := 0
	clearCalls := 0
	ready := Outcome{
		Status:  OutcomeReady,
		Command: cfg.RebootCommand,
		cooldownStart: func(context.Context) error {
			startCalls++
			return nil
		},
		cooldownClear: func(context.Context) error {
			clearCalls++
			return nil
		},
	}
	runner := &fakeLoopRunner{steps: []loopStep{{outcome: ready}}}
	executor := &fakeExecutor{err: errors.New("boom")}

	loop, err := NewLoop(cfg, runner, executor, WithLoopInterval(0), WithLoopSleepFunc(func(time.Duration) {}))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if err := loop.Run(context.Background()); err == nil {
		t.Fatal("expected loop to return error when executor fails")
	}
	if startCalls != 1 {
		t.Fatalf("expected cooldown start to be invoked once, got %d", startCalls)
	}
	if clearCalls != 0 {
		t.Fatalf("expected cooldown clear to be skipped on failure, got %d", clearCalls)
	}
}

func TestLoopLeavesLockHeldOnSuccessfulCommand(t *testing.T) {
	cfg := baseConfig()
	releaseCalled := false
	runner := &fakeLoopRunner{steps: []loopStep{{outcome: Outcome{
		Status:      OutcomeReady,
		Command:     cfg.RebootCommand,
		lockRelease: func(context.Context) error { releaseCalled = true; return nil },
	}}}}
	executor := &fakeExecutor{}

	loop, err := NewLoop(cfg, runner, executor, WithLoopInterval(0), WithLoopSleepFunc(func(time.Duration) {}))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("unexpected loop error: %v", err)
	}
	if releaseCalled {
		t.Fatal("expected lock release to be deferred after successful command")
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

func TestLoopErrorBackoffJitter(t *testing.T) {
	cfg := baseConfig()
	runner := &fakeLoopRunner{}
	executor := &fakeExecutor{}

	loop, err := NewLoop(cfg, runner, executor,
		WithLoopInterval(0),
		WithLoopSleepFunc(func(time.Duration) {}),
		WithLoopErrorBackoff(2*time.Second, 8*time.Second),
		WithLoopRandSource(rand.NewSource(1)),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	expectedDelays := []time.Duration{
		2 * time.Second,
		3159276016 * time.Nanosecond,
		7636376015 * time.Nanosecond,
		4644565053 * time.Nanosecond,
		7562143253 * time.Nanosecond,
	}
	expectedBackoffs := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		8 * time.Second,
		8 * time.Second,
	}

	for i := range expectedDelays {
		delay := loop.nextErrorDelay()
		if delay != expectedDelays[i] {
			t.Fatalf("unexpected delay[%d]: got %v want %v", i, delay, expectedDelays[i])
		}
		if loop.errorBackoff != expectedBackoffs[i] {
			t.Fatalf("unexpected backoff[%d]: got %v want %v", i, loop.errorBackoff, expectedBackoffs[i])
		}
		if delay < 2*time.Second || delay > 8*time.Second {
			t.Fatalf("delay[%d] out of bounds: %v", i, delay)
		}
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
	if sleeps[1] < 5*time.Millisecond || sleeps[1] > 10*time.Millisecond {
		t.Fatalf("expected second backoff within [5ms,10ms], got %v", sleeps[1])
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
