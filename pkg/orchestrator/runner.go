package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/clusterhealth"
	"github.com/clusterrebootd/clusterrebootd/pkg/config"
	"github.com/clusterrebootd/clusterrebootd/pkg/cooldown"
	"github.com/clusterrebootd/clusterrebootd/pkg/detector"
	"github.com/clusterrebootd/clusterrebootd/pkg/health"
	"github.com/clusterrebootd/clusterrebootd/pkg/lock"
	"github.com/clusterrebootd/clusterrebootd/pkg/observability"
	"github.com/clusterrebootd/clusterrebootd/pkg/windows"
)

// DetectorEvaluator abstracts the detector engine for orchestration.
type DetectorEvaluator interface {
	Evaluate(ctx context.Context) (bool, []detector.Result, error)
}

// HealthRunner captures the health script execution contract.
type HealthRunner interface {
	Run(ctx context.Context, extraEnv map[string]string) (health.Result, error)
}

// OutcomeStatus represents the final decision of a single orchestration pass.
type OutcomeStatus string

const (
	OutcomeNoAction        OutcomeStatus = "no_action"
	OutcomeKillSwitch      OutcomeStatus = "kill_switch_active"
	OutcomeHealthBlocked   OutcomeStatus = "health_blocked"
	OutcomeWindowDenied    OutcomeStatus = "window_denied"
	OutcomeWindowOutside   OutcomeStatus = "window_outside_allow"
	OutcomeLockUnavailable OutcomeStatus = "lock_unavailable"
	OutcomeLockSkipped     OutcomeStatus = "lock_skipped"
	OutcomeRecheckCleared  OutcomeStatus = "recheck_cleared"
	OutcomeCooldownActive  OutcomeStatus = "cooldown_active"
	OutcomeReady           OutcomeStatus = "ready"
)

const (
	monitorHealthPhase   = "monitor"
	heartbeatHealthPhase = "heartbeat"
)

// Outcome summarises the steps performed during RunOnce.
type Outcome struct {
	Status                  OutcomeStatus
	Message                 string
	DryRun                  bool
	LockAcquired            bool
	DetectorResults         []detector.Result
	PostLockDetectorResults []detector.Result
	PreLockHealthResult     *health.Result
	PreLockHealthPhase      string
	PostLockHealthResult    *health.Result
	PostLockHealthPhase     string
	Command                 []string
	ClusterUnhealthy        []clusterhealth.Record
	lockRelease             func(context.Context) error
	cooldownStart           func(context.Context) error
	cooldownClear           func(context.Context) error
	cooldownStarted         bool
}

// ReleaseLock releases the distributed lock associated with this outcome.
//
// It is primarily used by callers that evaluate a single pass (`run --once`
// or diagnostics tooling) to ensure the mutex is released when no reboot will
// be triggered.  Outcomes returned from the long-running loop will keep the
// lock held until the reboot command executes or an error occurs, so callers
// should only invoke this method when they explicitly want to give up the
// lease.
func (o *Outcome) ReleaseLock(ctx context.Context) error {
	if o == nil || o.lockRelease == nil {
		return nil
	}
	releaseFn := o.lockRelease
	o.lockRelease = nil
	return releaseFn(ctx)
}

func (o *Outcome) startCooldown(ctx context.Context) error {
	if o == nil || o.cooldownStart == nil {
		return nil
	}
	startFn := o.cooldownStart
	o.cooldownStart = nil
	if err := startFn(ctx); err != nil {
		return err
	}
	o.cooldownStarted = true
	return nil
}

func (o *Outcome) clearCooldown(ctx context.Context) error {
	if o == nil || o.cooldownClear == nil {
		return nil
	}
	clearFn := o.cooldownClear
	o.cooldownClear = nil
	return clearFn(ctx)
}

// Runner executes the reboot orchestration logic once.
type Runner struct {
	cfg             *config.Config
	detectors       DetectorEvaluator
	health          HealthRunner
	locker          lock.Manager
	cooldown        cooldown.Manager
	windows         windows.Evaluator
	killSwitchPath  string
	checkKill       func(string) (bool, error)
	sleep           func(time.Duration)
	rnd             *rand.Rand
	maxLockTries    int
	reporter        Reporter
	lockEnabled     bool
	lockSkipReason  string
	now             func() time.Time
	cooldownWindow  time.Duration
	commandEnv      map[string]string
	clusterHealth   clusterhealth.Manager
	healthReporting bool
	healthMu        *sync.Mutex
}

// Option configures a Runner.
type Option func(*Runner)

// WithKillSwitchChecker overrides the function used to check the kill switch file.
func WithKillSwitchChecker(fn func(string) (bool, error)) Option {
	return func(r *Runner) {
		r.checkKill = fn
	}
}

// WithSleepFunc overrides the sleep function used for lock backoff.
func WithSleepFunc(fn func(time.Duration)) Option {
	return func(r *Runner) {
		r.sleep = fn
	}
}

// WithRandSource injects a deterministic random source (useful for tests).
func WithRandSource(src rand.Source) Option {
	return func(r *Runner) {
		r.rnd = rand.New(src)
	}
}

// WithMaxLockAttempts configures how many times the runner retries lock acquisition.
func WithMaxLockAttempts(n int) Option {
	return func(r *Runner) {
		if n > 0 {
			r.maxLockTries = n
		}
	}
}

// WithReporter attaches an observability reporter to the runner.
func WithReporter(rep Reporter) Option {
	return func(r *Runner) {
		if rep != nil {
			r.reporter = rep
		}
	}
}

// WithClusterHealthManager wires a cluster health coordinator into the runner.
func WithClusterHealthManager(manager clusterhealth.Manager) Option {
	return func(r *Runner) {
		if manager == nil {
			r.clusterHealth = nil
			return
		}
		val := reflect.ValueOf(manager)
		if val.Kind() == reflect.Ptr && val.IsNil() {
			r.clusterHealth = nil
			return
		}
		r.clusterHealth = manager
	}
}

// WithClusterHealthReporting toggles whether cluster health updates should be
// published for the local node.  Reporting is enabled by default.
func WithClusterHealthReporting(enabled bool) Option {
	return func(r *Runner) {
		r.healthReporting = enabled
	}
}

// WithCooldownManager wires a cooldown coordinator into the runner.
func WithCooldownManager(manager cooldown.Manager) Option {
	return func(r *Runner) {
		r.cooldown = manager
	}
}

// WithLockAcquisition configures whether the runner should attempt to acquire the distributed lock.
// When disabled, the runner skips lock acquisition and post-lock checks, returning an OutcomeLockSkipped
// result annotated with the provided reason (when supplied).
func WithLockAcquisition(enabled bool, reason string) Option {
	return func(r *Runner) {
		r.lockEnabled = enabled
		if !enabled {
			r.lockSkipReason = strings.TrimSpace(reason)
		} else {
			r.lockSkipReason = ""
		}
	}
}

// WithCommandEnvironment overrides the environment used to expand reboot command arguments.
// The provided map is copied to avoid accidental mutations by callers.
func WithCommandEnvironment(env map[string]string) Option {
	return func(r *Runner) {
		r.commandEnv = cloneEnv(env)
	}
}

// WithHealthMutex allows callers to share a mutex across health script executions.
// When provided, both the orchestrator runner and any auxiliary loops (such as
// the heartbeat publisher) serialise health script invocations to avoid
// overlapping runs.
func WithHealthMutex(mu *sync.Mutex) Option {
	return func(r *Runner) {
		if mu != nil {
			r.healthMu = mu
		}
	}
}

// WithTimeSource injects a custom time source, enabling deterministic tests.
func WithTimeSource(fn func() time.Time) Option {
	return func(r *Runner) {
		if fn != nil {
			r.now = fn
		}
	}
}

// NewRunner constructs a Runner with the provided dependencies.
func NewRunner(cfg *config.Config, detectors DetectorEvaluator, healthRunner HealthRunner, locker lock.Manager, opts ...Option) (*Runner, error) {
	if cfg == nil {
		return nil, errors.New("config must not be nil")
	}
	if detectors == nil {
		return nil, errors.New("detector evaluator must not be nil")
	}
	if healthRunner == nil {
		return nil, errors.New("health runner must not be nil")
	}
	if locker == nil {
		return nil, errors.New("lock manager must not be nil")
	}

	runner := &Runner{
		cfg:             cfg,
		detectors:       detectors,
		health:          healthRunner,
		locker:          locker,
		killSwitchPath:  cfg.KillSwitchFile,
		checkKill:       defaultKillSwitchCheck,
		sleep:           time.Sleep,
		rnd:             rand.New(rand.NewSource(time.Now().UnixNano())),
		maxLockTries:    5,
		reporter:        NoopReporter{},
		lockEnabled:     true,
		cooldownWindow:  cfg.RebootCooldownInterval(),
		commandEnv:      cfg.BaseEnvironment(),
		healthReporting: true,
		healthMu:        &sync.Mutex{},
	}

	for _, opt := range opts {
		opt(runner)
	}

	if runner.rnd == nil {
		runner.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if runner.sleep == nil {
		runner.sleep = time.Sleep
	}
	if runner.checkKill == nil {
		runner.checkKill = defaultKillSwitchCheck
	}
	if runner.maxLockTries <= 0 {
		runner.maxLockTries = 5
	}
	if runner.reporter == nil {
		runner.reporter = NoopReporter{}
	}
	if runner.now == nil {
		runner.now = time.Now
	}
	if runner.cooldownWindow > 0 && runner.cooldown == nil {
		return nil, errors.New("cooldown interval configured but no cooldown manager provided")
	}
	if runner.commandEnv == nil {
		runner.commandEnv = cfg.BaseEnvironment()
	}
	if runner.healthMu == nil {
		runner.healthMu = &sync.Mutex{}
	}

	windowsEval, err := windows.NewEvaluator(cfg.Windows.Allow, cfg.Windows.Deny)
	if err != nil {
		return nil, fmt.Errorf("parse windows: %w", err)
	}
	runner.windows = windowsEval

	return runner, nil
}

// RunOnce executes the orchestration flow and returns the resulting outcome.
func (r *Runner) RunOnce(ctx context.Context) (out Outcome, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	defer func() {
		if err == nil && out.Status != "" {
			r.recordOutcome(ctx, out)
		}
	}()

	killActive, checkErr := r.checkKill(r.killSwitchPath)
	r.recordKillSwitch(ctx, "pre-lock", killActive, checkErr)
	if checkErr != nil {
		return out, fmt.Errorf("check kill switch: %w", checkErr)
	}
	if killActive {
		out.Status = OutcomeKillSwitch
		out.Message = fmt.Sprintf("kill switch %s present", r.killSwitchPath)
		return out, nil
	}

	if r.windows != nil {
		decision := r.windows.Evaluate(r.now())
		r.recordWindowDecision(ctx, decision)
		if !decision.Allowed {
			if decision.MatchedDeny != nil {
				out.Status = OutcomeWindowDenied
				out.Message = fmt.Sprintf("blocked by deny window %q", decision.MatchedDeny.Expression)
			} else {
				out.Status = OutcomeWindowOutside
				out.Message = "outside configured allow windows"
			}
			return out, nil
		}
	}

	requires, results, evalErr := r.evaluateDetectorsWithObservability(ctx, "pre-lock")
	out.DetectorResults = results
	if evalErr != nil {
		return out, fmt.Errorf("detector evaluation: %w", evalErr)
	}
	if !requires {
		monitorResult, monitorErr := r.runHealthAndUpdate(ctx, monitorHealthPhase, false, 0)
		out.PreLockHealthResult = &monitorResult
		out.PreLockHealthPhase = monitorHealthPhase
		if monitorErr != nil {
			return out, monitorErr
		}
		if monitorResult.ExitCode != 0 {
			out.Status = OutcomeHealthBlocked
			out.Message = fmt.Sprintf("health script reported unhealthy during monitoring (exit %d)", monitorResult.ExitCode)
			return out, nil
		}
		out.Status = OutcomeNoAction
		out.Message = "no detectors require a reboot"
		return out, nil
	}

	records, clusterErr := r.inspectClusterHealth(ctx, "pre-lock")
	if clusterErr != nil {
		return out, fmt.Errorf("check cluster health before lock: %w", clusterErr)
	}
	if blocked, message, blockers := r.evaluateClusterPolicies(records); blocked {
		out.Status = OutcomeHealthBlocked
		out.ClusterUnhealthy = cloneClusterRecords(blockers)
		out.Message = message
		return out, nil
	}

	preHealth, healthErr := r.runHealthAndUpdate(ctx, "pre-lock", false, 0)
	out.PreLockHealthResult = &preHealth
	out.PreLockHealthPhase = "pre-lock"
	if healthErr != nil {
		return out, healthErr
	}
	if preHealth.ExitCode != 0 {
		out.Status = OutcomeHealthBlocked
		out.Message = fmt.Sprintf("health script blocked reboot before lock (exit %d)", preHealth.ExitCode)
		return out, nil
	}

	if r.cooldownWindow > 0 && r.cooldown != nil {
		status, cdErr := r.cooldown.Status(ctx)
		r.recordCooldownStatus(ctx, "pre-lock", status, cdErr)
		if cdErr != nil {
			return out, fmt.Errorf("check reboot cooldown: %w", cdErr)
		}
		if status.Active {
			out.Status = OutcomeCooldownActive
			remaining := status.Remaining
			if remaining <= 0 && !status.ExpiresAt.IsZero() {
				remaining = time.Until(status.ExpiresAt)
			}
			if remaining < 0 {
				remaining = 0
			}
			details := []string{}
			if remaining > 0 {
				details = append(details, fmt.Sprintf("remaining %s", formatDuration(remaining)))
			}
			if status.Node != "" {
				details = append(details, fmt.Sprintf("started by %s", status.Node))
			}
			if len(details) > 0 {
				out.Message = fmt.Sprintf("reboot cooldown active (%s)", strings.Join(details, ", "))
			} else {
				out.Message = "reboot cooldown active"
			}
			return out, nil
		}
	}

	if !r.lockEnabled {
		out.Status = OutcomeLockSkipped
		msg := r.lockSkipReason
		if msg == "" {
			msg = "lock acquisition disabled"
		}
		out.Message = msg
		out.DryRun = r.cfg.DryRun
		out.Command = r.expandCommand(r.cfg.RebootCommand)
		return out, nil
	}

	lease, acquired, attempts, err := r.acquireLock(ctx)
	if err != nil {
		return out, err
	}
	if !acquired {
		out.Status = OutcomeLockUnavailable
		out.Message = fmt.Sprintf("failed to acquire lock after %d attempts", r.maxLockTries)
		return out, nil
	}
	out.LockAcquired = true

	defer func() {
		if lease != nil {
			releaseErr := r.releaseLease(context.Background(), lease)
			if err == nil && releaseErr != nil {
				err = releaseErr
			}
		}
	}()

	killActive, checkErr = r.checkKill(r.killSwitchPath)
	r.recordKillSwitch(ctx, "post-lock", killActive, checkErr)
	if checkErr != nil {
		return out, fmt.Errorf("check kill switch after lock: %w", checkErr)
	}
	if killActive {
		out.Status = OutcomeKillSwitch
		out.Message = fmt.Sprintf("kill switch %s present after lock", r.killSwitchPath)
		return out, nil
	}

	records, clusterErr = r.inspectClusterHealth(ctx, "post-lock")
	if clusterErr != nil {
		return out, fmt.Errorf("check cluster health under lock: %w", clusterErr)
	}
	if blocked, message, blockers := r.evaluateClusterPolicies(records); blocked {
		out.Status = OutcomeHealthBlocked
		out.ClusterUnhealthy = cloneClusterRecords(blockers)
		out.Message = message
		return out, nil
	}

	requires, postResults, evalErr := r.evaluateDetectorsWithObservability(ctx, "post-lock")
	out.PostLockDetectorResults = postResults
	if evalErr != nil {
		return out, fmt.Errorf("post-lock detector evaluation: %w", evalErr)
	}
	if !requires {
		out.Status = OutcomeRecheckCleared
		out.Message = "detectors cleared after lock acquisition"
		return out, nil
	}

	postHealth, healthErr := r.runHealthAndUpdate(ctx, "post-lock", true, attempts)
	out.PostLockHealthResult = &postHealth
	out.PostLockHealthPhase = "post-lock"
	if healthErr != nil {
		return out, healthErr
	}
	if postHealth.ExitCode != 0 {
		out.Status = OutcomeHealthBlocked
		out.Message = fmt.Sprintf("health script blocked reboot under lock (exit %d)", postHealth.ExitCode)
		return out, nil
	}

	out.Status = OutcomeReady
	out.Message = "reboot prerequisites satisfied"
	out.DryRun = r.cfg.DryRun
	out.Command = r.expandCommand(r.cfg.RebootCommand)

	if !out.DryRun && len(out.Command) > 0 {
		leaseRef := lease
		out.lockRelease = func(ctx context.Context) error {
			if ctx == nil {
				ctx = context.Background()
			}
			return r.releaseLease(ctx, leaseRef)
		}
		lease = nil

		if r.cooldown != nil && r.cooldownWindow > 0 {
			interval := r.cooldownWindow
			out.cooldownStart = func(ctx context.Context) error {
				if ctx == nil {
					ctx = context.Background()
				}
				err := r.cooldown.Start(ctx, interval)
				r.recordCooldownActivation(ctx, interval, err)
				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return err
					}
					return fmt.Errorf("start reboot cooldown: %w", err)
				}
				return nil
			}
			out.cooldownClear = func(ctx context.Context) error {
				if ctx == nil {
					ctx = context.Background()
				}
				err := r.cooldown.Start(ctx, 0)
				r.recordCooldownClear(ctx, err)
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					return fmt.Errorf("clear reboot cooldown: %w", err)
				}
				return err
			}
		}
	}

	return out, nil
}

func cloneEnv(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (r *Runner) expandCommand(command []string) []string {
	if len(command) == 0 {
		return nil
	}
	expanded := make([]string, len(command))
	for i, arg := range command {
		expanded[i] = expandWithEnv(arg, r.commandEnv)
	}
	return expanded
}

func expandWithEnv(input string, env map[string]string) string {
	if !strings.Contains(input, "$") {
		return input
	}
	return os.Expand(input, func(key string) string {
		if env != nil {
			if val, ok := env[key]; ok {
				return val
			}
		}
		return os.Getenv(key)
	})
}

func (r *Runner) acquireLock(ctx context.Context) (lock.Lease, bool, int, error) {
	minBackoff, maxBackoff := r.cfg.BackoffBounds()
	if minBackoff <= 0 {
		minBackoff = time.Second
	}
	if maxBackoff < minBackoff {
		maxBackoff = minBackoff
	}

	start := time.Now()
	var lastDelay time.Duration

	for attempt := 0; attempt < r.maxLockTries; attempt++ {
		attemptStart := time.Now()
		lease, err := r.locker.Acquire(ctx)
		duration := time.Since(attemptStart)

		switch {
		case err == nil:
			r.recordLockAttempt(ctx, attempt, duration, "success", nil)
			r.recordLockAcquired(ctx, attempt, time.Since(start))
			return lease, true, attempt + 1, nil
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			r.recordLockAttempt(ctx, attempt, duration, "canceled", err)
			return nil, false, attempt + 1, err
		case errors.Is(err, lock.ErrNotAcquired):
			r.recordLockAttempt(ctx, attempt, duration, "contended", err)
			if attempt == r.maxLockTries-1 {
				r.recordLockFailure(ctx, attempt+1)
				return nil, false, attempt + 1, nil
			}
		default:
			r.recordLockAttempt(ctx, attempt, duration, "error", err)
			return nil, false, attempt + 1, fmt.Errorf("acquire lock: %w", err)
		}

		delay := r.nextBackoffDelay(attempt, minBackoff, maxBackoff)
		lastDelay = delay
		r.recordLockBackoff(ctx, attempt, delay)
		if err := r.sleepWithContext(ctx, delay); err != nil {
			return nil, false, attempt + 1, err
		}
	}
	r.recordLockFailure(ctx, r.maxLockTries)
	return nil, false, r.maxLockTries, fmt.Errorf("failed to acquire lock after %d attempts (last delay %s)", r.maxLockTries, lastDelay)
}

func (r *Runner) nextBackoffDelay(attempt int, min, max time.Duration) time.Duration {
	multiplier := time.Duration(1 << attempt)
	if multiplier <= 0 {
		multiplier = 1
	}
	base := min * multiplier
	if base < min {
		base = min
	}
	if base > max {
		base = max
	}
	if base <= min {
		return min
	}
	jitterRange := base - min
	if jitterRange <= 0 {
		return base
	}
	jitter := time.Duration(r.rnd.Int63n(int64(jitterRange) + 1))
	return min + jitter
}

func (r *Runner) sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	done := make(chan struct{})
	go func() {
		r.sleep(d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func defaultKillSwitchCheck(path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *Runner) evaluateDetectorsWithObservability(ctx context.Context, phase string) (bool, []detector.Result, error) {
	start := time.Now()
	requires, results, err := r.detectors.Evaluate(ctx)
	r.recordDetectorEvaluation(ctx, phase, time.Since(start), requires, results, err)
	return requires, results, err
}

func (r *Runner) runHealthWithObservability(ctx context.Context, phase string, lockHeld bool, lockAttempts int) (health.Result, error) {
	if r.healthMu != nil {
		r.healthMu.Lock()
		defer r.healthMu.Unlock()
	}
	if lockAttempts < 0 {
		lockAttempts = 0
	}
	env := map[string]string{
		"RC_PHASE":         phase,
		"RC_NODE_NAME":     r.cfg.NodeName,
		"RC_LOCK_ENABLED":  strconv.FormatBool(r.lockEnabled),
		"RC_LOCK_HELD":     strconv.FormatBool(lockHeld),
		"RC_LOCK_ATTEMPTS": strconv.Itoa(lockAttempts),
	}
	start := time.Now()
	res, err := r.health.Run(ctx, env)
	r.recordHealth(ctx, phase, time.Since(start), res, err)
	return res, err
}

func (r *Runner) runHealthAndUpdate(ctx context.Context, phase string, lockHeld bool, lockAttempts int) (health.Result, error) {
	res, err := r.runHealthWithObservability(ctx, phase, lockHeld, lockAttempts)
	if err != nil {
		return res, fmt.Errorf("%s health check: %w", phase, err)
	}
	if err := r.updateClusterHealth(ctx, phase, res.ExitCode == 0, res.ExitCode); err != nil {
		return res, fmt.Errorf("record %s cluster health: %w", phase, err)
	}
	return res, nil
}

// HealthPublishingEnabled reports whether the runner is configured to publish
// cluster health status updates for the local node.
func (r *Runner) HealthPublishingEnabled() bool {
	if r == nil {
		return false
	}
	return r.clusterHealth != nil && r.healthReporting
}

// PublishHeartbeat executes the health script using the heartbeat phase and
// updates the cluster health manager with the result.  Callers should skip the
// invocation entirely when HealthPublishingEnabled reports false.
func (r *Runner) PublishHeartbeat(ctx context.Context) error {
	if !r.HealthPublishingEnabled() {
		return nil
	}
	_, err := r.runHealthAndUpdate(ctx, heartbeatHealthPhase, false, 0)
	return err
}

func filterClusterRecords(records []clusterhealth.Record, exclude string) []clusterhealth.Record {
	if len(records) == 0 {
		return nil
	}
	filtered := make([]clusterhealth.Record, 0, len(records))
	for _, rec := range records {
		if exclude != "" && rec.Node == exclude {
			continue
		}
		if rec.Healthy {
			continue
		}
		filtered = append(filtered, rec)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func cloneClusterRecords(records []clusterhealth.Record) []clusterhealth.Record {
	if len(records) == 0 {
		return nil
	}
	cloned := make([]clusterhealth.Record, len(records))
	copy(cloned, records)
	return cloned
}

func formatClusterHealthMessage(records []clusterhealth.Record) string {
	if len(records) == 0 {
		return "cluster health blocked by unhealthy nodes"
	}
	parts := make([]string, 0, len(records))
	for _, rec := range records {
		details := rec.Node
		extras := make([]string, 0, 2)
		if rec.Stage != "" {
			extras = append(extras, rec.Stage)
		}
		if rec.Reason != "" {
			extras = append(extras, rec.Reason)
		}
		if len(extras) > 0 {
			details = fmt.Sprintf("%s (%s)", details, strings.Join(extras, ", "))
		}
		parts = append(parts, details)
	}
	return "cluster health blocked by unhealthy nodes: " + strings.Join(parts, ", ")
}

func (r *Runner) inspectClusterHealth(ctx context.Context, phase string) ([]clusterhealth.Record, error) {
	attempted := r.clusterHealth != nil
	var (
		records []clusterhealth.Record
		err     error
	)
	if attempted {
		records, err = r.clusterHealth.Status(ctx)
	}
	r.recordClusterHealthStatus(ctx, phase, attempted, records, err)
	if !attempted {
		return nil, nil
	}
	return records, err
}

func (r *Runner) evaluateClusterPolicies(records []clusterhealth.Record) (bool, string, []clusterhealth.Record) {
	unhealthy := filterClusterRecords(records, r.cfg.NodeName)
	reasons := make([]string, 0, 3)
	if len(unhealthy) > 0 {
		reasons = append(reasons, formatClusterHealthMessage(unhealthy))
	}

	policies := r.cfg.ClusterPolicies
	fallbackSet := make(map[string]struct{}, len(policies.FallbackNodes))
	for _, node := range policies.FallbackNodes {
		trimmed := strings.TrimSpace(node)
		if trimmed == "" {
			continue
		}
		fallbackSet[trimmed] = struct{}{}
	}

	healthyCount := 0
	totalCount := 0
	healthyNonFallback := 0
	seen := make(map[string]struct{}, len(records)+1)

	nodeName := strings.TrimSpace(r.cfg.NodeName)
	if nodeName != "" {
		seen[nodeName] = struct{}{}
		totalCount++
		healthyCount++
		if _, isFallback := fallbackSet[nodeName]; !isFallback {
			healthyNonFallback++
		}
	}

	for _, rec := range records {
		node := strings.TrimSpace(rec.Node)
		if node == "" {
			continue
		}
		if _, ok := seen[node]; ok {
			continue
		}
		seen[node] = struct{}{}
		totalCount++
		if rec.Healthy {
			healthyCount++
			if _, isFallback := fallbackSet[node]; !isFallback {
				healthyNonFallback++
			}
		}
	}

	if policies.MinHealthyAbsolute != nil {
		minAbs := *policies.MinHealthyAbsolute
		if healthyCount < minAbs {
			reasons = append(reasons, fmt.Sprintf("healthy nodes %d below minimum %d", healthyCount, minAbs))
		}
	}

	if policies.MinHealthyFraction != nil && totalCount > 0 {
		fraction := *policies.MinHealthyFraction
		required := int(math.Ceil(fraction * float64(totalCount)))
		if healthyCount < required {
			reasons = append(reasons, fmt.Sprintf("healthy nodes %d below %.2f requirement (%d of %d)", healthyCount,
				fraction, required, totalCount))
		}
	}

	if policies.ForbidIfOnlyFallbackLeft && len(fallbackSet) > 0 && healthyCount > 0 && healthyNonFallback == 0 {
		reasons = append(reasons, "only fallback nodes remain healthy")
	}

	if len(reasons) == 0 {
		return false, "", nil
	}
	return true, strings.Join(reasons, "; "), unhealthy
}

func (r *Runner) updateClusterHealth(ctx context.Context, phase string, healthy bool, exitCode int) error {
	reason := ""
	if !healthy {
		reason = fmt.Sprintf("%s exit %d", phase, exitCode)
	}
	attempted := r.clusterHealth != nil && r.healthReporting
	var err error
	if attempted {
		if healthy {
			err = r.clusterHealth.ReportHealthy(ctx)
		} else {
			err = r.clusterHealth.ReportUnhealthy(ctx, clusterhealth.Report{Stage: phase, Reason: reason})
		}
	}
	r.recordClusterHealthUpdate(ctx, phase, healthy, reason, attempted, err)
	if err != nil {
		return err
	}
	return nil
}

func (r *Runner) recordClusterHealthStatus(ctx context.Context, phase string, attempted bool, records []clusterhealth.Record, statusErr error) {
	if r.reporter == nil {
		return
	}
	result := "healthy"
	level := observability.LevelInfo
	fields := map[string]interface{}{
		"phase": phase,
	}
	if !attempted {
		result = "skipped"
		fields["skipped"] = true
	} else {
		total := len(records)
		unhealthy := 0
		healthy := 0
		unhealthyRecords := make([]clusterhealth.Record, 0, total)
		for _, rec := range records {
			if rec.Healthy {
				healthy++
				continue
			}
			unhealthy++
			unhealthyRecords = append(unhealthyRecords, rec)
		}
		fields["total_nodes"] = total
		fields["healthy_count"] = healthy
		fields["unhealthy_count"] = unhealthy
		if unhealthy > 0 {
			result = "unhealthy"
			level = observability.LevelWarn
			fields["unhealthy_nodes"] = summarizeClusterRecords(unhealthyRecords)
		}
	}
	if statusErr != nil {
		result = "error"
		level = observability.LevelError
		fields["error"] = statusErr.Error()
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "cluster_health_checks_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"phase": phase, "result": result},
		Description: "Number of cluster health inspections grouped by phase and result.",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "cluster_health_status",
		Fields: fields,
	})
}

func (r *Runner) recordClusterHealthUpdate(ctx context.Context, phase string, healthy bool, reason string, attempted bool, updateErr error) {
	if r.reporter == nil {
		return
	}
	status := "healthy"
	if !healthy {
		status = "unhealthy"
	}
	result := "success"
	level := observability.LevelInfo
	fields := map[string]interface{}{
		"phase":  phase,
		"status": status,
	}
	if reason != "" {
		fields["reason"] = reason
	}
	if !attempted {
		result = "skipped"
		fields["skipped"] = true
	}
	if updateErr != nil {
		result = "error"
		level = observability.LevelError
		fields["error"] = updateErr.Error()
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "cluster_health_updates_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"phase": phase, "status": status, "result": result},
		Description: "Number of cluster health update attempts grouped by phase, status, and result.",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "cluster_health_update",
		Fields: fields,
	})
}

func summarizeClusterRecords(records []clusterhealth.Record) []map[string]interface{} {
	if len(records) == 0 {
		return nil
	}
	summaries := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		entry := map[string]interface{}{
			"node": rec.Node,
		}
		if rec.Stage != "" {
			entry["stage"] = rec.Stage
		}
		if rec.Reason != "" {
			entry["reason"] = rec.Reason
		}
		if !rec.ReportedAt.IsZero() {
			entry["reported_at"] = rec.ReportedAt.UTC().Format(time.RFC3339Nano)
		}
		summaries = append(summaries, entry)
	}
	return summaries
}

func (r *Runner) recordCooldownStatus(ctx context.Context, phase string, status cooldown.Status, statusErr error) {
	if r.reporter == nil {
		return
	}
	result := "inactive"
	level := observability.LevelInfo
	fields := map[string]interface{}{
		"phase":  phase,
		"active": status.Active,
	}
	if statusErr != nil {
		result = "error"
		level = observability.LevelError
		fields["error"] = statusErr.Error()
	} else if status.Active {
		result = "active"
		level = observability.LevelWarn
		if status.Node != "" {
			fields["node"] = status.Node
		}
		if !status.StartedAt.IsZero() {
			fields["started_at"] = status.StartedAt.UTC().Format(time.RFC3339Nano)
		}
		if status.Remaining > 0 {
			fields["remaining_ms"] = status.Remaining.Milliseconds()
		}
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "cooldown_checks_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"phase": phase, "result": result},
		Description: "Number of reboot cooldown inspections grouped by phase and result.",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "cooldown_status",
		Fields: fields,
	})
}

func (r *Runner) recordCooldownActivation(ctx context.Context, duration time.Duration, activationErr error) {
	if r.reporter == nil {
		return
	}
	result := "success"
	level := observability.LevelInfo
	fields := map[string]interface{}{
		"duration_ms": duration.Milliseconds(),
	}
	if activationErr != nil {
		result = "error"
		level = observability.LevelError
		fields["error"] = activationErr.Error()
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "cooldown_activations_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"result": result},
		Description: "Number of reboot cooldown activations grouped by result.",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "cooldown_started",
		Fields: fields,
	})
}

func (r *Runner) recordCooldownClear(ctx context.Context, clearErr error) {
	if r.reporter == nil {
		return
	}
	result := "success"
	level := observability.LevelInfo
	fields := map[string]interface{}{}
	if clearErr != nil {
		result = "error"
		level = observability.LevelError
		fields["error"] = clearErr.Error()
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "cooldown_clears_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"result": result},
		Description: "Number of reboot cooldown clear attempts grouped by result.",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "cooldown_cleared",
		Fields: fields,
	})
}

func (r *Runner) recordWindowDecision(ctx context.Context, decision windows.Decision) {
	result := "allowed"
	level := observability.LevelInfo
	fields := map[string]interface{}{
		"allowed": decision.Allowed,
	}
	if decision.AllowConfigured {
		fields["allow_configured"] = true
	}
	if decision.MatchedDeny != nil {
		result = "deny"
		level = observability.LevelWarn
		fields["matched_expression"] = decision.MatchedDeny.Expression
	} else if decision.AllowConfigured {
		if decision.MatchedAllow != nil {
			fields["matched_expression"] = decision.MatchedAllow.Expression
		} else {
			result = "outside_allow"
			level = observability.LevelWarn
		}
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "window_evaluations_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"result": result},
		Description: "Number of maintenance window evaluations grouped by result.",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "maintenance_window",
		Fields: fields,
	})
}

func (r *Runner) recordKillSwitch(ctx context.Context, stage string, active bool, checkErr error) {
	result := "inactive"
	level := observability.LevelInfo
	fields := map[string]interface{}{
		"stage":  stage,
		"active": active,
	}

	if checkErr != nil {
		result = "error"
		level = observability.LevelError
		fields["error"] = checkErr.Error()
	} else if active {
		result = "active"
		level = observability.LevelWarn
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "kill_switch_checks_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"stage": stage, "result": result},
		Description: "Number of kill switch evaluations grouped by stage and result.",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "kill_switch",
		Fields: fields,
	})
}

func (r *Runner) recordDetectorEvaluation(ctx context.Context, phase string, duration time.Duration, requires bool, results []detector.Result, evalErr error) {
	outcome := "clear"
	level := observability.LevelInfo
	fields := map[string]interface{}{
		"phase":              phase,
		"duration_ms":        duration.Milliseconds(),
		"requires_reboot":    requires,
		"detector_summaries": summariseDetectorResults(results),
	}

	if evalErr != nil {
		outcome = "error"
		level = observability.LevelError
		fields["error"] = evalErr.Error()
	} else if requires {
		outcome = "requires"
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "detector_evaluations_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"phase": phase, "result": outcome},
		Description: "Number of detector evaluations grouped by phase and result.",
	})
	r.reporter.RecordMetric(observability.Metric{
		Name:        "detector_evaluation_seconds",
		Type:        observability.MetricHistogram,
		Value:       duration.Seconds(),
		Labels:      map[string]string{"phase": phase, "result": outcome},
		Description: "Latency of detector evaluations.",
		Unit:        "seconds",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "detectors_evaluated",
		Fields: fields,
	})
}

func summariseDetectorResults(results []detector.Result) []map[string]interface{} {
	summaries := make([]map[string]interface{}, 0, len(results))
	for _, res := range results {
		summary := map[string]interface{}{
			"name":        res.Name,
			"requires":    res.RequiresReboot,
			"duration_ms": res.Duration.Milliseconds(),
		}
		if res.Err != nil {
			summary["error"] = res.Err.Error()
		}
		if res.CommandOutput != nil {
			summary["exit_code"] = res.CommandOutput.ExitCode
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func (r *Runner) recordHealth(ctx context.Context, phase string, duration time.Duration, res health.Result, runErr error) {
	status := "pass"
	level := observability.LevelInfo
	fields := map[string]interface{}{
		"phase":       phase,
		"duration_ms": duration.Milliseconds(),
	}

	if runErr != nil {
		status = "error"
		level = observability.LevelError
		fields["error"] = runErr.Error()
	} else {
		fields["exit_code"] = res.ExitCode
		if res.ExitCode != 0 {
			status = "blocked"
			level = observability.LevelWarn
		}
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "health_checks_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"phase": phase, "result": status},
		Description: "Number of health script executions grouped by phase and result.",
	})
	r.reporter.RecordMetric(observability.Metric{
		Name:        "health_check_seconds",
		Type:        observability.MetricHistogram,
		Value:       duration.Seconds(),
		Labels:      map[string]string{"phase": phase, "result": status},
		Description: "Execution time of the health script.",
		Unit:        "seconds",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "health_check",
		Fields: fields,
	})
}

func (r *Runner) recordLockAttempt(ctx context.Context, attempt int, duration time.Duration, result string, attemptErr error) {
	labels := map[string]string{"result": result}
	fields := map[string]interface{}{
		"attempt":     attempt + 1,
		"result":      result,
		"duration_ms": duration.Milliseconds(),
	}
	level := observability.LevelInfo

	switch result {
	case "contended":
		level = observability.LevelWarn
	case "error", "canceled":
		level = observability.LevelError
	}

	if attemptErr != nil {
		fields["error"] = attemptErr.Error()
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "lock_attempts_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      labels,
		Description: "Number of lock acquisition attempts grouped by result.",
	})
	r.reporter.RecordMetric(observability.Metric{
		Name:        "lock_acquire_seconds",
		Type:        observability.MetricHistogram,
		Value:       duration.Seconds(),
		Labels:      labels,
		Description: "Duration of individual lock acquisition attempts.",
		Unit:        "seconds",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "lock_attempt",
		Fields: fields,
	})
}

func (r *Runner) recordLockBackoff(ctx context.Context, attempt int, delay time.Duration) {
	r.reporter.RecordEvent(ctx, observability.Event{
		Level: observability.LevelInfo,
		Node:  r.cfg.NodeName,
		Event: "lock_backoff",
		Fields: map[string]interface{}{
			"attempt":  attempt + 1,
			"delay_ms": delay.Milliseconds(),
		},
	})
}

func (r *Runner) recordLockAcquired(ctx context.Context, attempt int, wait time.Duration) {
	r.reporter.RecordEvent(ctx, observability.Event{
		Level: observability.LevelInfo,
		Node:  r.cfg.NodeName,
		Event: "lock_acquired",
		Fields: map[string]interface{}{
			"attempt": attempt + 1,
			"wait_ms": wait.Milliseconds(),
		},
	})
}

func (r *Runner) recordLockFailure(ctx context.Context, attempts int) {
	r.reporter.RecordEvent(ctx, observability.Event{
		Level: observability.LevelWarn,
		Node:  r.cfg.NodeName,
		Event: "lock_failed",
		Fields: map[string]interface{}{
			"attempts": attempts,
		},
	})
}

func (r *Runner) releaseLease(ctx context.Context, lease lock.Lease) error {
	if lease == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	releaseCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	start := time.Now()
	releaseErr := lease.Release(releaseCtx)
	duration := time.Since(start)

	result := "success"
	level := observability.LevelInfo
	if releaseErr != nil {
		result = "error"
		level = observability.LevelWarn
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "lock_release_seconds",
		Type:        observability.MetricHistogram,
		Value:       duration.Seconds(),
		Labels:      map[string]string{"result": result},
		Description: "Duration of lock release operations.",
		Unit:        "seconds",
	})

	r.reporter.RecordEvent(context.Background(), observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "lock_released",
		Fields: map[string]interface{}{"result": result, "duration_ms": duration.Milliseconds()},
	})

	if releaseErr != nil {
		if errors.Is(releaseErr, context.Canceled) || errors.Is(releaseErr, context.DeadlineExceeded) {
			return releaseErr
		}
		return fmt.Errorf("release lock: %w", releaseErr)
	}

	return nil
}

func (r *Runner) recordOutcome(ctx context.Context, out Outcome) {
	if out.Status == "" {
		return
	}

	level := observability.LevelInfo
	switch out.Status {
	case OutcomeKillSwitch, OutcomeHealthBlocked, OutcomeWindowDenied, OutcomeWindowOutside, OutcomeLockUnavailable, OutcomeLockSkipped, OutcomeCooldownActive:
		level = observability.LevelWarn
	}

	fields := map[string]interface{}{
		"status":        out.Status,
		"dry_run":       out.DryRun,
		"lock_acquired": out.LockAcquired,
	}
	if out.Message != "" {
		fields["message"] = out.Message
	}
	if len(out.Command) > 0 {
		fields["command"] = strings.Join(out.Command, " ")
	}
	if len(out.ClusterUnhealthy) > 0 {
		fields["cluster_unhealthy"] = summarizeClusterRecords(out.ClusterUnhealthy)
	}

	r.reporter.RecordMetric(observability.Metric{
		Name:        "orchestration_outcomes_total",
		Type:        observability.MetricCounter,
		Value:       1,
		Labels:      map[string]string{"status": string(out.Status)},
		Description: "Number of orchestration passes grouped by outcome status.",
	})

	r.reporter.RecordEvent(ctx, observability.Event{
		Level:  level,
		Node:   r.cfg.NodeName,
		Event:  "run_outcome",
		Fields: fields,
	})
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d >= time.Minute {
		minutes := d / time.Minute
		seconds := (d % time.Minute) / time.Second
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	}
	if d >= time.Second {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}
