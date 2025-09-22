package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clusterrebootd/clusterrebootd/internal/testutil"
	"github.com/clusterrebootd/clusterrebootd/pkg/clusterhealth"
	"github.com/clusterrebootd/clusterrebootd/pkg/config"
	"github.com/clusterrebootd/clusterrebootd/pkg/cooldown"
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

type fakeCooldown struct {
	mu        sync.Mutex
	status    cooldown.Status
	statusErr error
	startErr  error
	calls     int
	lastDur   time.Duration
	clearErr  error
	clears    int
}

func (f *fakeCooldown) Status(ctx context.Context) (cooldown.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.statusErr
}

func (f *fakeCooldown) Start(ctx context.Context, duration time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if duration == 0 {
		f.clears++
		return f.clearErr
	}
	f.calls++
	f.lastDur = duration
	return f.startErr
}

func (f *fakeCooldown) Close() error { return nil }

type clusterHealthReport struct {
	healthy bool
	stage   string
	reason  string
}

type fakeClusterHealthManager struct {
	mu          sync.Mutex
	records     []clusterhealth.Record
	listErr     error
	reportErr   error
	reports     []clusterHealthReport
	statusCalls int
}

func (f *fakeClusterHealthManager) Status(ctx context.Context) ([]clusterhealth.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	copied := make([]clusterhealth.Record, len(f.records))
	copy(copied, f.records)
	return copied, f.listErr
}

func (f *fakeClusterHealthManager) ReportHealthy(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, clusterHealthReport{healthy: true})
	return f.reportErr
}

func (f *fakeClusterHealthManager) ReportUnhealthy(ctx context.Context, report clusterhealth.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, clusterHealthReport{healthy: false, stage: report.Stage, reason: report.Reason})
	return f.reportErr
}

type executorEntry struct {
	release func(context.Context) error
	allow   chan struct{}
}

type trackingExecutor struct {
	mu          sync.Mutex
	entries     map[string]*executorEntry
	activeNode  string
	started     chan string
	completed   chan string
	invocations []string
}

func newTrackingExecutor(expected int) *trackingExecutor {
	if expected <= 0 {
		expected = 1
	}
	return &trackingExecutor{
		entries:   make(map[string]*executorEntry, expected),
		started:   make(chan string, expected),
		completed: make(chan string, expected),
	}
}

func (e *trackingExecutor) Execute(ctx context.Context, command []string) error {
	if len(command) == 0 {
		return errors.New("reboot command is empty")
	}
	node := command[len(command)-1]

	e.mu.Lock()
	if e.activeNode != "" {
		active := e.activeNode
		e.mu.Unlock()
		return fmt.Errorf("command already running for %s while starting %s", active, node)
	}
	entry := e.ensureEntryLocked(node)
	allowCh := entry.allow
	e.activeNode = node
	e.invocations = append(e.invocations, node)
	e.mu.Unlock()

	defer e.finish(node)

	if err := e.signalStart(ctx, node); err != nil {
		return err
	}
	if err := e.waitForAllowance(ctx, allowCh); err != nil {
		return err
	}

	e.mu.Lock()
	release := entry.release
	e.mu.Unlock()
	if release != nil {
		if err := release(context.Background()); err != nil {
			return err
		}
	}

	select {
	case e.completed <- node:
	default:
	}

	return nil
}

func (e *trackingExecutor) ensureEntryLocked(node string) *executorEntry {
	entry, ok := e.entries[node]
	if !ok || entry == nil {
		entry = &executorEntry{allow: make(chan struct{})}
		e.entries[node] = entry
		return entry
	}
	if entry.allow == nil {
		entry.allow = make(chan struct{})
	}
	return entry
}

func (e *trackingExecutor) signalStart(ctx context.Context, node string) error {
	select {
	case e.started <- node:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *trackingExecutor) waitForAllowance(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *trackingExecutor) finish(node string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeNode == node {
		e.activeNode = ""
	}
	delete(e.entries, node)
}

func (e *trackingExecutor) Register(node string, release func(context.Context) error) {
	e.mu.Lock()
	entry := e.ensureEntryLocked(node)
	entry.release = release
	e.mu.Unlock()
}

func (e *trackingExecutor) Allow(node string) {
	e.mu.Lock()
	entry := e.ensureEntryLocked(node)
	allowCh := entry.allow
	e.mu.Unlock()

	select {
	case <-allowCh:
	default:
		close(allowCh)
	}
}

func (e *trackingExecutor) WaitForStart(timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = time.Second
	}
	select {
	case node := <-e.started:
		return node, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out waiting for reboot command start after %s", timeout)
	}
}

func (e *trackingExecutor) WaitForCompletion(timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = time.Second
	}
	select {
	case node := <-e.completed:
		return node, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out waiting for reboot command completion after %s", timeout)
	}
}

func (e *trackingExecutor) Invocations() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.invocations))
	copy(out, e.invocations)
	return out
}

var _ CommandExecutor = (*trackingExecutor)(nil)

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
	if healthRunner.calls != 1 {
		t.Fatalf("expected health script to run once for monitoring, got %d calls", healthRunner.calls)
	}
	if len(healthRunner.lastEnvs) != 1 {
		t.Fatalf("expected one health invocation environment, got %d", len(healthRunner.lastEnvs))
	}
	if phase := healthRunner.lastEnvs[0]["RC_PHASE"]; phase != monitorHealthPhase {
		t.Fatalf("expected RC_PHASE %q, got %q", monitorHealthPhase, phase)
	}
	if locker.calls != 0 {
		t.Fatalf("expected no lock attempts, got %d", locker.calls)
	}
}

func TestRunnerPublishesHealthyStatusWhenIdle(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: false, results: []detector.Result{{Name: "stub"}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{}
	manager := &fakeClusterHealthManager{}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithClusterHealthManager(manager))
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
	if len(manager.reports) != 1 {
		t.Fatalf("expected one cluster health report, got %d", len(manager.reports))
	}
	if !manager.reports[0].healthy {
		t.Fatalf("expected healthy report to clear markers, got %+v", manager.reports[0])
	}
	if healthRunner.calls != 1 {
		t.Fatalf("expected health script to run once, got %d", healthRunner.calls)
	}
	if outcome.PreLockHealthResult == nil {
		t.Fatal("expected monitoring health result to be recorded")
	}
	if outcome.PreLockHealthPhase != monitorHealthPhase {
		t.Fatalf("expected monitoring phase %q, got %q", monitorHealthPhase, outcome.PreLockHealthPhase)
	}
}

func TestRunnerPublishesUnhealthyStatusWhenIdle(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: false, results: []detector.Result{{Name: "stub"}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 7}}}}
	locker := &fakeLocker{}
	manager := &fakeClusterHealthManager{}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithClusterHealthManager(manager))
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
	if !strings.Contains(outcome.Message, "monitoring") {
		t.Fatalf("expected monitoring message, got %q", outcome.Message)
	}
	if len(manager.reports) != 1 {
		t.Fatalf("expected one cluster health report, got %d", len(manager.reports))
	}
	report := manager.reports[0]
	if report.healthy {
		t.Fatalf("expected unhealthy report, got healthy")
	}
	if report.stage != monitorHealthPhase {
		t.Fatalf("expected stage %q, got %q", monitorHealthPhase, report.stage)
	}
	if report.reason != "monitor exit 7" {
		t.Fatalf("unexpected reason %q", report.reason)
	}
	if locker.calls != 0 {
		t.Fatalf("expected no lock attempts, got %d", locker.calls)
	}
	if outcome.PreLockHealthResult == nil {
		t.Fatal("expected monitoring health result to be recorded")
	}
	if outcome.PreLockHealthResult.ExitCode != 7 {
		t.Fatalf("expected monitoring exit code 7, got %d", outcome.PreLockHealthResult.ExitCode)
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
	if lease.released {
		t.Fatal("expected lease to remain held until explicitly released")
	}
	if err := outcome.ReleaseLock(context.Background()); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	if !lease.released {
		t.Fatal("expected lease to be released after ReleaseLock")
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

func TestRunnerBlocksWhenClusterHealthReportsOthersUnhealthy(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "stub"}}}}}
	healthRunner := &fakeHealth{}
	locker := &fakeLocker{}
	manager := &fakeClusterHealthManager{records: []clusterhealth.Record{{
		Node:       "node-b",
		Healthy:    false,
		Stage:      "pre-lock",
		Reason:     "exit 2",
		ReportedAt: time.Now(),
	}}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithClusterHealthManager(manager))
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
	if len(outcome.ClusterUnhealthy) != 1 || outcome.ClusterUnhealthy[0].Node != "node-b" {
		t.Fatalf("expected outcome to record node-b as unhealthy, got %#v", outcome.ClusterUnhealthy)
	}
	if healthRunner.calls != 0 {
		t.Fatalf("expected health script not to run, got %d calls", healthRunner.calls)
	}
	if !strings.Contains(outcome.Message, "node-b") {
		t.Fatalf("expected message to mention node-b, got %q", outcome.Message)
	}
	if len(manager.reports) != 0 {
		t.Fatalf("expected no cluster health reports, got %d", len(manager.reports))
	}
}

func TestRunnerBlocksWhenMinHealthyAbsoluteViolated(t *testing.T) {
	cfg := baseConfig()
	minAbs := 3
	cfg.ClusterPolicies.MinHealthyAbsolute = &minAbs
	cfg.ClusterPolicies.MinHealthyFraction = nil

	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "stub"}}}}}
	healthRunner := &fakeHealth{}
	locker := &fakeLocker{}
	manager := &fakeClusterHealthManager{records: []clusterhealth.Record{
		{
			Node:    "node-b",
			Healthy: true,
		},
		{
			Node:       "node-c",
			Healthy:    false,
			Stage:      "monitor",
			Reason:     "exit 2",
			ReportedAt: time.Now(),
		},
	}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithClusterHealthManager(manager))
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
	if !strings.Contains(outcome.Message, "healthy nodes 2 below minimum 3") {
		t.Fatalf("expected message to mention min healthy absolute violation, got %q", outcome.Message)
	}
	if len(outcome.ClusterUnhealthy) != 1 || outcome.ClusterUnhealthy[0].Node != "node-c" {
		t.Fatalf("expected node-c to be reported as unhealthy, got %#v", outcome.ClusterUnhealthy)
	}
}

func TestRunnerBlocksWhenMinHealthyFractionViolated(t *testing.T) {
	cfg := baseConfig()
	fraction := 0.75
	cfg.ClusterPolicies.MinHealthyFraction = &fraction
	cfg.ClusterPolicies.MinHealthyAbsolute = nil

	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "stub"}}}}}
	healthRunner := &fakeHealth{}
	locker := &fakeLocker{}
	manager := &fakeClusterHealthManager{records: []clusterhealth.Record{
		{
			Node:    "node-b",
			Healthy: true,
		},
		{
			Node:       "node-c",
			Healthy:    false,
			Stage:      "monitor",
			Reason:     "exit 1",
			ReportedAt: time.Now(),
		},
		{
			Node:       "node-d",
			Healthy:    false,
			Stage:      "monitor",
			Reason:     "exit 1",
			ReportedAt: time.Now(),
		},
	}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithClusterHealthManager(manager))
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
	if !strings.Contains(outcome.Message, "0.75") {
		t.Fatalf("expected message to mention fraction requirement, got %q", outcome.Message)
	}
	if len(outcome.ClusterUnhealthy) != 2 {
		t.Fatalf("expected two unhealthy nodes, got %d", len(outcome.ClusterUnhealthy))
	}
}

func TestRunnerBlocksWhenOnlyFallbackNodesRemainHealthy(t *testing.T) {
	cfg := baseConfig()
	cfg.ClusterPolicies.MinHealthyFraction = nil
	cfg.ClusterPolicies.MinHealthyAbsolute = nil
	cfg.ClusterPolicies.ForbidIfOnlyFallbackLeft = true
	cfg.ClusterPolicies.FallbackNodes = []string{cfg.NodeName, "node-b"}

	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "stub"}}}}}
	healthRunner := &fakeHealth{}
	locker := &fakeLocker{}
	manager := &fakeClusterHealthManager{records: []clusterhealth.Record{
		{
			Node:    "node-b",
			Healthy: true,
		},
	}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithClusterHealthManager(manager))
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
	if !strings.Contains(outcome.Message, "only fallback nodes remain healthy") {
		t.Fatalf("expected fallback violation message, got %q", outcome.Message)
	}
	if len(outcome.ClusterUnhealthy) != 0 {
		t.Fatalf("expected no unhealthy nodes recorded, got %#v", outcome.ClusterUnhealthy)
	}
	if healthRunner.calls != 0 {
		t.Fatalf("expected health runner not to execute, got %d calls", healthRunner.calls)
	}
}

func TestRunnerIgnoresOwnClusterHealthRecord(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "stub"}}}, {requires: true, results: []detector.Result{{Name: "post"}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}, {result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: &fakeLease{}}}}
	manager := &fakeClusterHealthManager{records: []clusterhealth.Record{{
		Node:    cfg.NodeName,
		Healthy: false,
		Stage:   "pre-lock",
		Reason:  "exit 1",
	}}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithClusterHealthManager(manager))
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
	if manager.statusCalls < 2 {
		t.Fatalf("expected cluster health inspection at least twice, got %d", manager.statusCalls)
	}
	if len(manager.reports) == 0 {
		t.Fatalf("expected healthy reports to be recorded")
	}
	for _, rep := range manager.reports {
		if !rep.healthy {
			t.Fatalf("expected only healthy reports, saw %+v", rep)
		}
	}
}

func TestRunnerReportsClusterHealthOnScriptFailure(t *testing.T) {
	cfg := baseConfig()
	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "stub"}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 3}}}}
	locker := &fakeLocker{}
	manager := &fakeClusterHealthManager{}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithClusterHealthManager(manager))
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
	if len(manager.reports) != 1 {
		t.Fatalf("expected one cluster health report, got %d", len(manager.reports))
	}
	report := manager.reports[0]
	if report.healthy {
		t.Fatalf("expected unhealthy report, got healthy")
	}
	if report.stage != "pre-lock" || report.reason != "pre-lock exit 3" {
		t.Fatalf("unexpected report contents: %+v", report)
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

func TestRunnerEtcdLockPreventsConcurrentReadyNodes(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	lockKey := "/cluster/reboot"
	nodeNames := []string{"node-a", "node-b"}
	executor := newTrackingExecutor(len(nodeNames))
	const lockHoldBeforeAllow = 150 * time.Millisecond

	type nodeHarness struct {
		name   string
		cfg    *config.Config
		runner *Runner
	}

	harnesses := make([]nodeHarness, 0, len(nodeNames))

	for i, name := range nodeNames {
		cfg := baseConfig()
		cfg.NodeName = name
		cfg.EtcdEndpoints = append([]string(nil), cluster.Endpoints...)
		cfg.LockKey = lockKey
		cfg.MinRebootIntervalSec = 0
		cfg.RebootCommand = []string{"reboot-now", "${RC_NODE_NAME}"}

		manager, err := lock.NewEtcdManager(lock.EtcdManagerOptions{
			Endpoints: cfg.EtcdEndpoints,
			LockKey:   cfg.LockKey,
			TTL:       cfg.LockTTL(),
			NodeName:  cfg.NodeName,
			ProcessID: (i + 1) * 1001,
		})
		if err != nil {
			t.Fatalf("failed to create etcd manager for %s: %v", name, err)
		}
		mgr := manager
		t.Cleanup(func() {
			_ = mgr.Close()
		})

		engine := &fakeEngine{steps: []evalStep{
			{requires: true, results: []detector.Result{{Name: name + "-pre"}}},
			{requires: true, results: []detector.Result{{Name: name + "-retry"}}},
			{requires: true, results: []detector.Result{{Name: name + "-post"}}},
		}}
		healthRunner := &fakeHealth{steps: []healthStep{
			{result: health.Result{ExitCode: 0}},
			{result: health.Result{ExitCode: 0}},
			{result: health.Result{ExitCode: 0}},
		}}

		runner, err := NewRunner(cfg, engine, healthRunner, manager, WithMaxLockAttempts(1), WithSleepFunc(func(time.Duration) {}))
		if err != nil {
			t.Fatalf("failed to create runner for %s: %v", name, err)
		}

		harnesses = append(harnesses, nodeHarness{name: name, cfg: cfg, runner: runner})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blockedCounts := make(map[string]int)
	readyCounts := make(map[string]int)
	var countsMu sync.Mutex

	type loopResult struct {
		name string
		err  error
	}

	results := make(chan loopResult, len(harnesses))

	for _, harness := range harnesses {
		harness := harness
		loop, err := NewLoop(harness.cfg, harness.runner, executor,
			WithLoopSleepFunc(func(time.Duration) {}),
			WithLoopInterval(10*time.Millisecond),
			WithLoopIterationHook(func(out Outcome) {
				switch out.Status {
				case OutcomeReady:
					if !out.LockAcquired {
						panic(fmt.Sprintf("expected %s to hold the lock when ready", harness.name))
					}
					if out.lockRelease == nil {
						panic(fmt.Sprintf("expected %s ready outcome to expose lock release", harness.name))
					}
					if out.cooldownStart != nil || out.cooldownClear != nil {
						panic(fmt.Sprintf("expected %s ready outcome to omit cooldown handlers when interval disabled", harness.name))
					}
					executor.Register(harness.name, out.lockRelease)
					countsMu.Lock()
					readyCounts[harness.name]++
					countsMu.Unlock()
				case OutcomeLockUnavailable:
					countsMu.Lock()
					blockedCounts[harness.name]++
					countsMu.Unlock()
				}
			}))
		if err != nil {
			t.Fatalf("failed to create loop for %s: %v", harness.name, err)
		}
		go func(h nodeHarness, l *Loop) {
			err := l.Run(ctx)
			results <- loopResult{name: h.name, err: err}
		}(harness, loop)
	}

	first, err := executor.WaitForStart(5 * time.Second)
	if err != nil {
		t.Fatalf("waiting for first reboot command: %v", err)
	}
	// Hold the lock briefly so the waiting node attempts acquisition and records contention.
	time.Sleep(lockHoldBeforeAllow)
	executor.Allow(first)
	if completed, err := executor.WaitForCompletion(5 * time.Second); err != nil {
		t.Fatalf("waiting for %s completion: %v", first, err)
	} else if completed != first {
		t.Fatalf("expected completion for %s, got %s", first, completed)
	}

	second, err := executor.WaitForStart(5 * time.Second)
	if err != nil {
		t.Fatalf("waiting for second reboot command: %v", err)
	}
	if second == first {
		t.Fatalf("expected distinct nodes to execute sequentially, both were %s", second)
	}
	time.Sleep(lockHoldBeforeAllow)
	executor.Allow(second)
	if completed, err := executor.WaitForCompletion(5 * time.Second); err != nil {
		t.Fatalf("waiting for %s completion: %v", second, err)
	} else if completed != second {
		t.Fatalf("expected completion for %s, got %s", second, completed)
	}

	select {
	case extra := <-executor.started:
		t.Fatalf("unexpected additional reboot execution by %s", extra)
	case <-time.After(100 * time.Millisecond):
	}

	for range harnesses {
		res := <-results
		if res.err != nil {
			t.Fatalf("loop for %s returned error: %v", res.name, res.err)
		}
	}

	invocations := executor.Invocations()
	if len(invocations) != len(harnesses) {
		t.Fatalf("expected %d reboot invocations, got %d (%v)", len(harnesses), len(invocations), invocations)
	}
	seen := make(map[string]struct{}, len(invocations))
	for _, name := range invocations {
		if _, ok := seen[name]; ok {
			t.Fatalf("node %s executed reboot command multiple times (%v)", name, invocations)
		}
		seen[name] = struct{}{}
	}

	countsMu.Lock()
	readySnapshot := make(map[string]int, len(readyCounts))
	blockedSnapshot := make(map[string]int, len(blockedCounts))
	for k, v := range readyCounts {
		readySnapshot[k] = v
	}
	for k, v := range blockedCounts {
		blockedSnapshot[k] = v
	}
	countsMu.Unlock()

	for _, harness := range harnesses {
		if readySnapshot[harness.name] != 1 {
			t.Fatalf("expected %s to reach ready exactly once, got %d", harness.name, readySnapshot[harness.name])
		}
	}
	for _, harness := range harnesses {
		if harness.name == first {
			continue
		}
		if blockedSnapshot[harness.name] == 0 {
			t.Fatalf("expected %s to observe lock contention before rebooting", harness.name)
		}
	}
}

func TestRunnerEtcdLockSerializesThreeNodes(t *testing.T) {
	cluster := testutil.StartEmbeddedEtcd(t)

	lockKey := "/cluster/reboot"

	nodeNames := []string{"node-a", "node-b", "node-c"}
	executor := newTrackingExecutor(len(nodeNames))
	const lockHoldBeforeAllow = 150 * time.Millisecond

	type nodeHarness struct {
		name   string
		cfg    *config.Config
		runner *Runner
	}

	harnesses := make([]nodeHarness, 0, len(nodeNames))

	for i, name := range nodeNames {
		cfg := baseConfig()
		cfg.NodeName = name
		cfg.EtcdEndpoints = append([]string(nil), cluster.Endpoints...)
		cfg.LockKey = lockKey
		cfg.MinRebootIntervalSec = 0
		cfg.RebootCommand = []string{"reboot-now", "${RC_NODE_NAME}"}

		manager, err := lock.NewEtcdManager(lock.EtcdManagerOptions{
			Endpoints: cfg.EtcdEndpoints,
			LockKey:   cfg.LockKey,
			TTL:       cfg.LockTTL(),
			NodeName:  cfg.NodeName,
			ProcessID: (i + 1) * 2001,
		})
		if err != nil {
			t.Fatalf("failed to create etcd manager for %s: %v", name, err)
		}
		mgr := manager
		t.Cleanup(func() {
			_ = mgr.Close()
		})

		engine := &fakeEngine{steps: []evalStep{
			{requires: true, results: []detector.Result{{Name: name + "-pre"}}},
			{requires: true, results: []detector.Result{{Name: name + "-retry"}}},
			{requires: true, results: []detector.Result{{Name: name + "-post"}}},
		}}
		healthRunner := &fakeHealth{steps: []healthStep{
			{result: health.Result{ExitCode: 0}},
			{result: health.Result{ExitCode: 0}},
			{result: health.Result{ExitCode: 0}},
		}}

		runner, err := NewRunner(cfg, engine, healthRunner, manager, WithMaxLockAttempts(1), WithSleepFunc(func(time.Duration) {}))
		if err != nil {
			t.Fatalf("failed to create runner for %s: %v", name, err)
		}

		harnesses = append(harnesses, nodeHarness{name: name, cfg: cfg, runner: runner})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blockedCounts := make(map[string]int)
	readyCounts := make(map[string]int)
	var countsMu sync.Mutex

	type loopResult struct {
		name string
		err  error
	}

	results := make(chan loopResult, len(harnesses))

	for _, harness := range harnesses {
		harness := harness
		loop, err := NewLoop(harness.cfg, harness.runner, executor,
			WithLoopSleepFunc(func(time.Duration) {}),
			WithLoopInterval(10*time.Millisecond),
			WithLoopIterationHook(func(out Outcome) {
				switch out.Status {
				case OutcomeReady:
					if !out.LockAcquired {
						panic(fmt.Sprintf("expected %s to hold the lock when ready", harness.name))
					}
					if out.lockRelease == nil {
						panic(fmt.Sprintf("expected %s ready outcome to expose lock release", harness.name))
					}
					if out.cooldownStart != nil || out.cooldownClear != nil {
						panic(fmt.Sprintf("expected %s ready outcome to omit cooldown handlers when interval disabled", harness.name))
					}
					executor.Register(harness.name, out.lockRelease)
					countsMu.Lock()
					readyCounts[harness.name]++
					countsMu.Unlock()
				case OutcomeLockUnavailable:
					countsMu.Lock()
					blockedCounts[harness.name]++
					countsMu.Unlock()
				}
			}))
		if err != nil {
			t.Fatalf("failed to create loop for %s: %v", harness.name, err)
		}
		go func(h nodeHarness, l *Loop) {
			err := l.Run(ctx)
			results <- loopResult{name: h.name, err: err}
		}(harness, loop)
	}

	order := make([]string, 0, len(harnesses))
	seen := make(map[string]struct{}, len(harnesses))
	for range harnesses {
		node, err := executor.WaitForStart(5 * time.Second)
		if err != nil {
			t.Fatalf("waiting for reboot command: %v", err)
		}
		if _, dup := seen[node]; dup {
			t.Fatalf("node %s attempted multiple reboot executions", node)
		}
		seen[node] = struct{}{}
		order = append(order, node)

		if len(order) < len(harnesses) {
			// Hold the lock briefly so peers attempt acquisition and record contention
			// before the current node completes its simulated reboot.
			time.Sleep(lockHoldBeforeAllow)
		}

		executor.Allow(node)
		if completed, err := executor.WaitForCompletion(5 * time.Second); err != nil {
			t.Fatalf("waiting for %s completion: %v", node, err)
		} else if completed != node {
			t.Fatalf("expected completion for %s, got %s", node, completed)
		}
	}

	select {
	case extra := <-executor.started:
		t.Fatalf("unexpected additional reboot execution by %s", extra)
	case <-time.After(100 * time.Millisecond):
	}

	for range harnesses {
		res := <-results
		if res.err != nil {
			t.Fatalf("loop for %s returned error: %v", res.name, res.err)
		}
	}

	invocations := executor.Invocations()
	if len(invocations) != len(harnesses) {
		t.Fatalf("expected %d reboot invocations, got %d (%v)", len(harnesses), len(invocations), invocations)
	}
	for i, name := range invocations {
		if i >= len(order) || name != order[i] {
			t.Fatalf("expected invocation order %v, got %v", order, invocations)
		}
	}

	countsMu.Lock()
	readySnapshot := make(map[string]int, len(readyCounts))
	blockedSnapshot := make(map[string]int, len(blockedCounts))
	for k, v := range readyCounts {
		readySnapshot[k] = v
	}
	for k, v := range blockedCounts {
		blockedSnapshot[k] = v
	}
	countsMu.Unlock()

	for _, harness := range harnesses {
		if readySnapshot[harness.name] != 1 {
			t.Fatalf("expected %s to reach ready exactly once, got %d", harness.name, readySnapshot[harness.name])
		}
	}

	if len(order) != len(harnesses) {
		t.Fatalf("expected %d nodes to execute, got order %v", len(harnesses), order)
	}
	first := order[0]
	for _, harness := range harnesses {
		if harness.name == first {
			continue
		}
		if blockedSnapshot[harness.name] == 0 {
			t.Fatalf("expected %s to observe lock contention before rebooting", harness.name)
		}
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

func TestRunnerExpandsRebootCommandPlaceholders(t *testing.T) {
	cfg := baseConfig()
	cfg.RebootCommand = []string{"/sbin/shutdown", "-r", "now", "coordinated reboot for ${RC_NODE_NAME}"}
	lease := &fakeLease{}
	engine := &fakeEngine{steps: []evalStep{{requires: true}, {requires: true}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}, {result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: lease}}}

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
	if len(outcome.Command) != len(cfg.RebootCommand) {
		t.Fatalf("expected command length %d, got %d", len(cfg.RebootCommand), len(outcome.Command))
	}
	want := "coordinated reboot for " + cfg.NodeName
	if got := outcome.Command[len(outcome.Command)-1]; got != want {
		t.Fatalf("expected expanded command argument %q, got %q", want, got)
	}
	if cfg.RebootCommand[len(cfg.RebootCommand)-1] != "coordinated reboot for ${RC_NODE_NAME}" {
		t.Fatalf("expected original reboot command to remain unchanged, got %q", cfg.RebootCommand[len(cfg.RebootCommand)-1])
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

func TestRunnerCooldownBlocksReboot(t *testing.T) {
	cfg := baseConfig()
	cfg.MinRebootIntervalSec = 300

	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "stub", RequiresReboot: true}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}}}
	locker := &fakeLocker{}
	cool := &fakeCooldown{status: cooldown.Status{Active: true, Node: "node-b", Remaining: 90 * time.Second}}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithCooldownManager(cool))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	outcome, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != OutcomeCooldownActive {
		t.Fatalf("expected cooldown outcome, got %s", outcome.Status)
	}
	if !strings.Contains(outcome.Message, "cooldown") {
		t.Fatalf("expected cooldown message, got %q", outcome.Message)
	}
	if locker.calls != 0 {
		t.Fatalf("expected lock not to be acquired, got %d attempts", locker.calls)
	}
}

func TestRunnerReadyConfiguresCooldownHandlers(t *testing.T) {
	cfg := baseConfig()
	cfg.MinRebootIntervalSec = 120

	engine := &fakeEngine{steps: []evalStep{{requires: true, results: []detector.Result{{Name: "stub", RequiresReboot: true}}}, {requires: true, results: []detector.Result{{Name: "stub", RequiresReboot: true}}}}}
	healthRunner := &fakeHealth{steps: []healthStep{{result: health.Result{ExitCode: 0}}, {result: health.Result{ExitCode: 0}}}}
	lease := &fakeLease{}
	locker := &fakeLocker{outcomes: []acquireOutcome{{lease: lease}}}
	cool := &fakeCooldown{}

	runner, err := NewRunner(cfg, engine, healthRunner, locker, WithCooldownManager(cool))
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
	if outcome.cooldownStart == nil {
		t.Fatal("expected cooldownStart to be configured")
	}
	if outcome.cooldownClear == nil {
		t.Fatal("expected cooldownClear to be configured")
	}

	if err := outcome.startCooldown(context.Background()); err != nil {
		t.Fatalf("failed to start cooldown: %v", err)
	}
	if !outcome.cooldownStarted {
		t.Fatal("expected cooldownStarted flag to be true")
	}
	cool.mu.Lock()
	calls := cool.calls
	duration := cool.lastDur
	cool.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected cooldown Start to be called once, got %d", calls)
	}
	if duration != cfg.RebootCooldownInterval() {
		t.Fatalf("expected cooldown duration %s, got %s", cfg.RebootCooldownInterval(), duration)
	}

	if err := outcome.clearCooldown(context.Background()); err != nil {
		t.Fatalf("failed to clear cooldown: %v", err)
	}
	cool.mu.Lock()
	clears := cool.clears
	cool.mu.Unlock()
	if clears != 1 {
		t.Fatalf("expected cooldown clear to be invoked once, got %d", clears)
	}
}
