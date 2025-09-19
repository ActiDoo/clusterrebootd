package orchestrator

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
	"github.com/clusterrebootd/clusterrebootd/pkg/detector"
	"github.com/clusterrebootd/clusterrebootd/pkg/health"
	"github.com/clusterrebootd/clusterrebootd/pkg/lock"
	"github.com/clusterrebootd/clusterrebootd/pkg/observability"
)

type evalStep struct {
	requires bool
	results  []detector.Result
	err      error
}

type fakeEngine struct {
	mu      sync.Mutex
	steps   []evalStep
	pointer int
	calls   int
}

func (f *fakeEngine) Evaluate(ctx context.Context) (bool, []detector.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.steps) == 0 {
		return false, nil, nil
	}
	if f.pointer >= len(f.steps) {
		last := f.steps[len(f.steps)-1]
		return last.requires, cloneResults(last.results), last.err
	}
	step := f.steps[f.pointer]
	f.pointer++
	return step.requires, cloneResults(step.results), step.err
}

func cloneResults(results []detector.Result) []detector.Result {
	copied := make([]detector.Result, len(results))
	copy(copied, results)
	return copied
}

type healthStep struct {
	result health.Result
	err    error
}

type fakeHealth struct {
	mu       sync.Mutex
	steps    []healthStep
	pointer  int
	calls    int
	lastEnvs []map[string]string
}

func (f *fakeHealth) Run(ctx context.Context, extraEnv map[string]string) (health.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	envCopy := make(map[string]string, len(extraEnv))
	for k, v := range extraEnv {
		envCopy[k] = v
	}
	f.lastEnvs = append(f.lastEnvs, envCopy)
	if len(f.steps) == 0 {
		return health.Result{}, nil
	}
	if f.pointer >= len(f.steps) {
		last := f.steps[len(f.steps)-1]
		return last.result, last.err
	}
	step := f.steps[f.pointer]
	f.pointer++
	return step.result, step.err
}

type acquireOutcome struct {
	lease lock.Lease
	err   error
}

type fakeLease struct {
	mu       sync.Mutex
	released bool
}

func (l *fakeLease) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = true
	return nil
}

type fakeLocker struct {
	mu       sync.Mutex
	outcomes []acquireOutcome
	pointer  int
	calls    int
}

func (f *fakeLocker) Acquire(ctx context.Context) (lock.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.outcomes) == 0 {
		return &fakeLease{}, nil
	}
	if f.pointer >= len(f.outcomes) {
		outcome := f.outcomes[len(f.outcomes)-1]
		return outcome.lease, outcome.err
	}
	outcome := f.outcomes[f.pointer]
	f.pointer++
	return outcome.lease, outcome.err
}

func baseConfig() *config.Config {
	minFrac := 0.5
	minAbs := 1
	return &config.Config{
		NodeName: "node-a",
		RebootRequiredDetectors: []config.DetectorConfig{{
			Name: "stub",
			Type: "file",
			Path: "/tmp/marker",
		}},
		HealthScript:     "/bin/true",
		HealthTimeoutSec: 30,
		CheckIntervalSec: 60,
		BackoffMinSec:    1,
		BackoffMaxSec:    2,
		LockKey:          "/cluster/lock",
		LockTTLSec:       120,
		EtcdEndpoints:    []string{"127.0.0.1:2379"},
		RebootCommand:    []string{"/sbin/shutdown", "-r", "now"},
		ClusterPolicies: config.ClusterPolicies{
			MinHealthyFraction: &minFrac,
			MinHealthyAbsolute: &minAbs,
		},
	}
}

func TestRunnerNoRebootRequired(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: false, results: []detector.Result{{Name: "stub"}}}}}
	healthRunner := &fakeHealth{}
	locker := &fakeLocker{}

	runner, err := NewRunner(cfg, engine, healthRunner, locker)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeNoAction {
		t.Fatalf("expected OutcomeNoAction, got %s", outcome.Status)
	}
	if len(outcome.DetectorResults) != 1 {
		t.Fatalf("expected 1 detector result, got %d", len(outcome.DetectorResults))
	}
	if healthRunner.calls != 0 {
		t.Fatalf("expected health script not to run, got %d calls", healthRunner.calls)
	}
	if locker.calls != 0 {
		t.Fatalf("expected no lock attempts, got %d", locker.calls)
	}
}

func TestRunnerKillSwitchActive(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: true}}}
	healthRunner := &fakeHealth{}
	locker := &fakeLocker{}
	checkerCalls := 0
	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithKillSwitchChecker(func(string) (bool, error) {
		checkerCalls++
		return true, nil
	}))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeKillSwitch {
		t.Fatalf("expected OutcomeKillSwitch, got %s", outcome.Status)
	}
	if engine.calls != 0 {
		t.Fatalf("expected detectors not to run, got %d calls", engine.calls)
	}
	if checkerCalls == 0 {
		t.Fatal("expected kill switch checker to be invoked")
	}
}

func TestRunnerHealthEnvironmentIncludesLockContext(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{
		{requires: true, results: []detector.Result{{Name: "pre"}}},
		{requires: true, results: []detector.Result{{Name: "post"}}},
	}}
	healthRunner := &fakeHealth{steps: []healthStep{
		{result: health.Result{ExitCode: 0}},
		{result: health.Result{ExitCode: 0}},
	}}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: &fakeLease{}}}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker)

	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeReady {
		t.Fatalf("expected OutcomeReady, got %s", outcome.Status)
	}

	if len(healthRunner.lastEnvs) != 2 {
		t.Fatalf("expected 2 health executions, got %d", len(healthRunner.lastEnvs))
	}

	pre := healthRunner.lastEnvs[0]
	if got := pre["RC_PHASE"]; got != "pre-lock" {
		t.Fatalf("expected pre-lock phase, got %q", got)
	}
	if got := pre["RC_LOCK_ENABLED"]; got != "true" {
		t.Fatalf("expected RC_LOCK_ENABLED true, got %q", got)
	}
	if got := pre["RC_LOCK_HELD"]; got != "false" {
		t.Fatalf("expected RC_LOCK_HELD false, got %q", got)
	}
	if got := pre["RC_LOCK_ATTEMPTS"]; got != "0" {
		t.Fatalf("expected RC_LOCK_ATTEMPTS 0, got %q", got)
	}

	post := healthRunner.lastEnvs[1]
	if got := post["RC_PHASE"]; got != "post-lock" {
		t.Fatalf("expected post-lock phase, got %q", got)
	}
	if got := post["RC_LOCK_ENABLED"]; got != "true" {
		t.Fatalf("expected RC_LOCK_ENABLED true post-lock, got %q", got)
	}
	if got := post["RC_LOCK_HELD"]; got != "true" {
		t.Fatalf("expected RC_LOCK_HELD true, got %q", got)
	}
	if got := post["RC_LOCK_ATTEMPTS"]; got != "1" {
		t.Fatalf("expected RC_LOCK_ATTEMPTS 1, got %q", got)
	}
	if got := post["RC_NODE_NAME"]; got != cfg.NodeName {
		t.Fatalf("expected RC_NODE_NAME %q, got %q", cfg.NodeName, got)
	}
}

func TestRunnerBlockedByDenyWindow(t *testing.T) {
	cfg := baseConfig()
	cfg.Windows.Deny = []string{"Mon 00:00-Tue 00:00"}
	engine := &fakeEngine{steps: []evalStep{{requires: true}}}
	healthRunner := &fakeHealth{}
	locker := &fakeLocker{}

	now := func() time.Time {
		return time.Date(2024, time.March, 4, 12, 0, 0, 0, time.UTC)
	}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithTimeSource(now))

	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeWindowDenied {
		t.Fatalf("expected OutcomeWindowDenied, got %s", outcome.Status)
	}
	if engine.calls != 0 {
		t.Fatalf("expected detectors to be skipped, got %d calls", engine.calls)
	}
	if healthRunner.calls != 0 {
		t.Fatalf("expected health script to be skipped, got %d calls", healthRunner.calls)
	}
	if locker.calls != 0 {
		t.Fatalf("expected no lock attempts, got %d", locker.calls)
	}
	if outcome.Message != "blocked by deny window \"Mon 00:00-Tue 00:00\"" {
		t.Fatalf("unexpected message: %q", outcome.Message)
	}
}

func TestRunnerOutsideAllowWindow(t *testing.T) {
	cfg := baseConfig()
	cfg.Windows.Allow = []string{"Tue 22:00-23:00"}
	engine := &fakeEngine{steps: []evalStep{{requires: true}}}
	healthRunner := &fakeHealth{}
	locker := &fakeLocker{}

	now := func() time.Time {
		return time.Date(2024, time.March, 4, 10, 0, 0, 0, time.UTC)
	}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithTimeSource(now))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeWindowOutside {
		t.Fatalf("expected OutcomeWindowOutside, got %s", outcome.Status)
	}
	if engine.calls != 0 {
		t.Fatalf("expected detectors to be skipped, got %d calls", engine.calls)
	}
	if healthRunner.calls != 0 {
		t.Fatalf("expected health script to be skipped, got %d calls", healthRunner.calls)
	}
	if locker.calls != 0 {
		t.Fatalf("expected no lock attempts, got %d", locker.calls)
	}
}

func TestRunnerWithinAllowWindow(t *testing.T) {
	cfg := baseConfig()
	cfg.Windows.Allow = []string{"Tue 22:00-23:00"}
	lease := &fakeLease{}
	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "pre"}}}, {requires: true, results: []detector.Result{{Name: "post"}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}, {result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: lease}}}

	now := func() time.Time {
		return time.Date(2024, time.March, 5, 22, 30, 0, 0, time.UTC)
	}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithTimeSource(now))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeReady {
		t.Fatalf("expected OutcomeReady, got %s", outcome.Status)
	}
	if !lease.released {
		t.Fatal("expected lease to be released")
	}
}

func TestRunnerHealthBlocksBeforeLock(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "stub"}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 2}}}}
	locker := &fakeLocker{}

	runner, err := NewRunner(cfg, engine, healthRunner, locker)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeHealthBlocked {
		t.Fatalf("expected OutcomeHealthBlocked, got %s", outcome.Status)
	}
	if locker.calls != 0 {
		t.Fatalf("expected no lock attempts, got %d", locker.calls)
	}
}

func TestRunnerLockUnavailable(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: true}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}}}
	outcomes := []acquireOutcome{{err: lock.ErrNotAcquired}, {err: lock.ErrNotAcquired}, {err: lock.ErrNotAcquired}}
	locker := &fakeLocker{outcomes: outcomes}
	sleepDurations := make([]time.Duration, 0)

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithMaxLockAttempts(3), WithSleepFunc(func(d time.Duration) {
		sleepDurations = append(sleepDurations, d)
	}), WithRandSource(rand.NewSource(1)))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeLockUnavailable {
		t.Fatalf("expected OutcomeLockUnavailable, got %s", outcome.Status)
	}
	if locker.calls != 3 {
		t.Fatalf("expected 3 lock attempts, got %d", locker.calls)
	}
	if len(sleepDurations) != 2 {
		t.Fatalf("expected 2 backoff sleeps, got %d", len(sleepDurations))
	}
}

func TestRunnerLockSkipped(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "pre"}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{}

	reason := "lock acquisition skipped for diagnostics"
	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithLockAcquisition(false, reason))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if outcome.Status != OutcomeLockSkipped {
		t.Fatalf("expected OutcomeLockSkipped, got %s", outcome.Status)
	}
	if outcome.Message != reason {
		t.Fatalf("expected message %q, got %q", reason, outcome.Message)
	}
	if outcome.LockAcquired {
		t.Fatal("expected LockAcquired to be false when lock is skipped")
	}
	if len(outcome.Command) != len(cfg.RebootCommand) {
		t.Fatalf("expected reboot command to be populated, got %v", outcome.Command)
	}
	if locker.calls != 0 {
		t.Fatalf("expected no lock attempts, got %d", locker.calls)
	}
	if healthRunner.calls != 1 {
		t.Fatalf("expected one health script execution, got %d", healthRunner.calls)
	}
}

func TestRunnerRecheckCleared(t *testing.T) {
	cfg := baseConfig()
	lease := &fakeLease{}
	engine := &fakeEngine{steps: []evalStep{
		{requires: true, results: []detector.Result{{Name: "pre"}}},
		{requires: false, results: []detector.Result{{Name: "post"}}},
	}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: lease, err: nil}}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeRecheckCleared {
		t.Fatalf("expected OutcomeRecheckCleared, got %s", outcome.Status)
	}
	if !lease.released {
		t.Fatal("expected lease to be released")
	}
	if !outcome.LockAcquired {
		t.Fatal("expected LockAcquired to be true")
	}
	if len(outcome.PostLockDetectorResults) != 1 {
		t.Fatalf("expected post-lock results recorded, got %d", len(outcome.PostLockDetectorResults))
	}
}

func TestRunnerHealthBlocksAfterLock(t *testing.T) {
	cfg := baseConfig()
	lease := &fakeLease{}
	engine := &fakeEngine{steps: []evalStep{
		{requires: true, results: []detector.Result{{Name: "pre"}}},
		{requires: true, results: []detector.Result{{Name: "post"}}},
	}}
	healthRunner := &fakeHealth{steps: []healthStep{
		{result: health.Result{ExitCode: 0}},
		{result: health.Result{ExitCode: 1}},
	}}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: lease, err: nil}}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeHealthBlocked {
		t.Fatalf("expected OutcomeHealthBlocked, got %s", outcome.Status)
	}
	if !lease.released {
		t.Fatal("expected lease to be released")
	}
	if outcome.PostLockHealthResult == nil {
		t.Fatal("expected post-lock health result to be recorded")
	}
}

func TestRunnerReadyDryRun(t *testing.T) {
	cfg := baseConfig()
	cfg.DryRun = true
	lease := &fakeLease{}
	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "pre"}}}, {requires: true, results: []detector.Result{{Name: "post"}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}, {result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: lease, err: nil}}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeReady {
		t.Fatalf("expected OutcomeReady, got %s", outcome.Status)
	}
	if !outcome.DryRun {
		t.Fatal("expected DryRun flag to be true")
	}
	if len(outcome.Command) != len(cfg.RebootCommand) {
		t.Fatalf("expected command to be populated, got %v", outcome.Command)
	}
}

func TestRunnerHealthError(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: true}}}
	healthRunner := &fakeHealth{steps: []healthStep{{err: errors.New("boom")}}}
	locker := &fakeLocker{}

	runner, err := NewRunner(cfg, engine, healthRunner, locker)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("expected error from health runner")
	}
}

func TestRunnerDetectorError(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: false, err: errors.New("detector boom")}}}
	healthRunner := &fakeHealth{}
	locker := &fakeLocker{}

	runner, err := NewRunner(cfg, engine, healthRunner, locker)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("expected detector evaluation error")
	}
}

func TestRunnerKillSwitchAfterLock(t *testing.T) {
	cfg := baseConfig()
	lease := &fakeLease{}
	engine := &fakeEngine{steps: []evalStep{{requires: true}, {requires: true}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}, {result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: lease, err: nil}}}
	calls := 0
	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithKillSwitchChecker(func(string) (bool, error) {
		calls++
		return calls > 1, nil
	}))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeKillSwitch {
		t.Fatalf("expected OutcomeKillSwitch, got %s", outcome.Status)
	}
	if !lease.released {
		t.Fatal("expected lease released when kill switch activates post-lock")
	}
}

func TestRunnerEmitsObservabilitySignals(t *testing.T) {
	cfg := baseConfig()
	lease := &fakeLease{}
	engine := &fakeEngine{steps: []evalStep{{requires: true}, {requires: true}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}, {result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: lease}}}

	var events []observability.Event
	var metrics []observability.Metric
	reporter := ReporterFuncs{
		OnEvent: func(_ context.Context, event observability.Event) {
			events = append(events, event)
		},
		OnMetric: func(metric observability.Metric) {
			metrics = append(metrics, metric)
		},
	}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithReporter(reporter))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeReady {
		t.Fatalf("expected ready outcome, got %s", outcome.Status)
	}

	if len(events) == 0 {
		t.Fatal("expected observability events to be recorded")
	}

	var outcomeEventFound bool
	for _, event := range events {
		if event.Event == "run_outcome" {
			outcomeEventFound = true
			if status, ok := event.Fields["status"].(OutcomeStatus); ok {
				if status != OutcomeReady {
					t.Fatalf("unexpected outcome status in event: %v", status)
				}
			} else if statusStr, ok := event.Fields["status"].(string); ok {
				if statusStr != string(OutcomeReady) {
					t.Fatalf("unexpected outcome status string in event: %s", statusStr)
				}
			} else {
				t.Fatalf("status field missing in outcome event: %v", event.Fields)
			}
		}
	}
	if !outcomeEventFound {
		t.Fatalf("expected run_outcome event among %d events", len(events))
	}

	var outcomeMetricFound bool
	for _, metric := range metrics {
		if metric.Name == "orchestration_outcomes_total" {
			outcomeMetricFound = true
			if metric.Labels["status"] != string(OutcomeReady) {
				t.Fatalf("unexpected status label on outcome metric: %v", metric.Labels)
			}
		}
	}
	if !outcomeMetricFound {
		t.Fatalf("expected orchestration_outcomes_total metric among %d metrics", len(metrics))
	}
}
