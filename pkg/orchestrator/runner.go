package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
	"github.com/clusterrebootd/clusterrebootd/pkg/detector"
	"github.com/clusterrebootd/clusterrebootd/pkg/health"
	"github.com/clusterrebootd/clusterrebootd/pkg/lock"
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
	OutcomeLockUnavailable OutcomeStatus = "lock_unavailable"
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
	killSwitchPath string
	checkKill      func(string) (bool, error)
	sleep          func(time.Duration)
	rnd            *rand.Rand
	maxLockTries   int
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

	return runner, nil
}

// RunOnce executes the orchestration flow and returns the resulting outcome.
func (r *Runner) RunOnce(ctx context.Context) (out Outcome, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	killActive, err := r.checkKill(r.killSwitchPath)
	if err != nil {
		return out, fmt.Errorf("check kill switch: %w", err)
	}
	if killActive {
		out.Status = OutcomeKillSwitch
		out.Message = fmt.Sprintf("kill switch %s present", r.killSwitchPath)
		return out, nil
	}

	requires, results, err := r.detectors.Evaluate(ctx)
	out.DetectorResults = results
	if err != nil {
		return out, fmt.Errorf("detector evaluation: %w", err)
	}
	if !requires {
		out.Status = OutcomeNoAction
		out.Message = "no detectors require a reboot"
		return out, nil
	}

	preHealth, err := r.health.Run(ctx, map[string]string{
		"RC_PHASE":     "pre-lock",
		"RC_NODE_NAME": r.cfg.NodeName,
	})
	out.PreLockHealthResult = &preHealth
	if err != nil {
		return out, fmt.Errorf("pre-lock health check: %w", err)
	}
	if preHealth.ExitCode != 0 {
		out.Status = OutcomeHealthBlocked
		out.Message = fmt.Sprintf("health script blocked reboot before lock (exit %d)", preHealth.ExitCode)
		return out, nil
	}

	lease, acquired, err := r.acquireLock(ctx)
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
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if releaseErr := lease.Release(releaseCtx); releaseErr != nil && err == nil {
			err = fmt.Errorf("release lock: %w", releaseErr)
		}
	}()

	killActive, err = r.checkKill(r.killSwitchPath)
	if err != nil {
		return out, fmt.Errorf("check kill switch after lock: %w", err)
	}
	if killActive {
		out.Status = OutcomeKillSwitch
		out.Message = fmt.Sprintf("kill switch %s present after lock", r.killSwitchPath)
		return out, nil
	}

	requires, postResults, err := r.detectors.Evaluate(ctx)
	out.PostLockDetectorResults = postResults
	if err != nil {
		return out, fmt.Errorf("post-lock detector evaluation: %w", err)
	}
	if !requires {
		out.Status = OutcomeRecheckCleared
		out.Message = "detectors cleared after lock acquisition"
		return out, nil
	}

	postHealth, err := r.health.Run(ctx, map[string]string{
		"RC_PHASE":     "post-lock",
		"RC_NODE_NAME": r.cfg.NodeName,
	})
	out.PostLockHealthResult = &postHealth
	if err != nil {
		return out, fmt.Errorf("post-lock health check: %w", err)
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

	return out, err
}

func (r *Runner) acquireLock(ctx context.Context) (lock.Lease, bool, error) {
	minBackoff, maxBackoff := r.cfg.BackoffBounds()
	if minBackoff <= 0 {
		minBackoff = time.Second
	}
	if maxBackoff < minBackoff {
		maxBackoff = minBackoff
	}

	var lastDelay time.Duration
	for attempt := 0; attempt < r.maxLockTries; attempt++ {
		lease, err := r.locker.Acquire(ctx)
		if err == nil {
			return lease, true, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
		if !errors.Is(err, lock.ErrNotAcquired) {
			return nil, false, fmt.Errorf("acquire lock: %w", err)
		}
		if attempt == r.maxLockTries-1 {
			return nil, false, nil
		}
		delay := r.nextBackoffDelay(attempt, minBackoff, maxBackoff)
		lastDelay = delay
		if err := r.sleepWithContext(ctx, delay); err != nil {
			return nil, false, err
		}
	}
	return nil, false, fmt.Errorf("failed to acquire lock after %d attempts (last delay %s)", r.maxLockTries, lastDelay)
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
