package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
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
	OutcomeReady           OutcomeStatus = "ready"
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
	PostLockHealthResult    *health.Result
	Command                 []string
}

// Runner executes the reboot orchestration logic once.
type Runner struct {
	cfg            *config.Config
	detectors      DetectorEvaluator
	health         HealthRunner
	locker         lock.Manager
	windows        windows.Evaluator
	killSwitchPath string
	checkKill      func(string) (bool, error)
	sleep          func(time.Duration)
	rnd            *rand.Rand
	maxLockTries   int
	reporter       Reporter
	lockEnabled    bool
	lockSkipReason string
	now            func() time.Time
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
		cfg:            cfg,
		detectors:      detectors,
		health:         healthRunner,
		locker:         locker,
		killSwitchPath: cfg.KillSwitchFile,
		checkKill:      defaultKillSwitchCheck,
		sleep:          time.Sleep,
		rnd:            rand.New(rand.NewSource(time.Now().UnixNano())),
		maxLockTries:   5,
		reporter:       NoopReporter{},
		lockEnabled:    true,
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
		out.Status = OutcomeNoAction
		out.Message = "no detectors require a reboot"
		return out, nil
	}

	preHealth, healthErr := r.runHealthWithObservability(ctx, "pre-lock", false, 0)
	out.PreLockHealthResult = &preHealth
	if healthErr != nil {
		return out, fmt.Errorf("pre-lock health check: %w", healthErr)
	}
	if preHealth.ExitCode != 0 {
		out.Status = OutcomeHealthBlocked
		out.Message = fmt.Sprintf("health script blocked reboot before lock (exit %d)", preHealth.ExitCode)
		return out, nil
	}

	if !r.lockEnabled {
		out.Status = OutcomeLockSkipped
		msg := r.lockSkipReason
		if msg == "" {
			msg = "lock acquisition disabled"
		}
		out.Message = msg
		out.DryRun = r.cfg.DryRun
		if len(r.cfg.RebootCommand) > 0 {
			out.Command = append([]string(nil), r.cfg.RebootCommand...)
		}
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

	defer r.releaseLease(lease, &err)

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

	postHealth, healthErr := r.runHealthWithObservability(ctx, "post-lock", true, attempts)
	out.PostLockHealthResult = &postHealth
	if healthErr != nil {
		return out, fmt.Errorf("post-lock health check: %w", healthErr)
	}
	if postHealth.ExitCode != 0 {
		out.Status = OutcomeHealthBlocked
		out.Message = fmt.Sprintf("health script blocked reboot under lock (exit %d)", postHealth.ExitCode)
		return out, nil
	}

	out.Status = OutcomeReady
	out.Message = "reboot prerequisites satisfied"
	out.DryRun = r.cfg.DryRun
	out.Command = append([]string(nil), r.cfg.RebootCommand...)

	return out, nil
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

func (r *Runner) releaseLease(lease lock.Lease, errPtr *error) {
	if lease == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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

	if releaseErr != nil && errPtr != nil && *errPtr == nil {
		*errPtr = fmt.Errorf("release lock: %w", releaseErr)
	}
}

func (r *Runner) recordOutcome(ctx context.Context, out Outcome) {
	if out.Status == "" {
		return
	}

	level := observability.LevelInfo
	switch out.Status {
	case OutcomeKillSwitch, OutcomeHealthBlocked, OutcomeWindowDenied, OutcomeWindowOutside, OutcomeLockUnavailable, OutcomeLockSkipped:
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
