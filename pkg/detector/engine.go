package detector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type commandEvaluator interface {
	EvaluateCommand(context.Context) (bool, CommandOutput, error)
}

// Engine orchestrates a collection of detectors and aggregates their results.
type Engine struct {
	detectors []Detector
}

// Result captures the outcome of evaluating a single detector.
type Result struct {
	Name           string
	RequiresReboot bool
	Err            error
	Duration       time.Duration
	CommandOutput  *CommandOutput
}

// EvaluationError aggregates per-detector evaluation failures.
type EvaluationError struct {
	Problems []string
}

func (e *EvaluationError) Error() string {
	return fmt.Sprintf("detector evaluation failed: %s", strings.Join(e.Problems, "; "))
}

func (e *EvaluationError) Is(target error) bool {
	var other *EvaluationError
	if !errors.As(target, &other) {
		return false
	}
	return true
}

// NewEngine constructs an Engine from the provided detectors.
func NewEngine(detectors []Detector) (*Engine, error) {
	if len(detectors) == 0 {
		return nil, errors.New("at least one detector must be configured")
	}

	seen := make(map[string]struct{}, len(detectors))
	copySlice := make([]Detector, 0, len(detectors))
	for _, det := range detectors {
		name := strings.TrimSpace(det.Name())
		if name == "" {
			return nil, errors.New("detector name must not be empty")
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate detector name %q", name)
		}
		seen[name] = struct{}{}
		copySlice = append(copySlice, det)
	}

	return &Engine{detectors: copySlice}, nil
}

// Evaluate executes all detectors and returns whether any of them reported a reboot requirement.
func (e *Engine) Evaluate(ctx context.Context) (bool, []Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	results := make([]Result, 0, len(e.detectors))
	problems := make([]string, 0)
	requiresReboot := false

	for _, det := range e.detectors {
		if ctx.Err() != nil {
			return false, results, ctx.Err()
		}

		start := time.Now()
		res := Result{Name: det.Name()}

		if cmdDet, ok := det.(commandEvaluator); ok {
			required, output, err := cmdDet.EvaluateCommand(ctx)
			res.RequiresReboot = required
			res.CommandOutput = &output
			res.Err = err
		} else {
			required, err := det.Check(ctx)
			res.RequiresReboot = required
			res.Err = err
		}
		res.Duration = time.Since(start)
		results = append(results, res)

		if res.Err != nil {
			if errors.Is(res.Err, context.Canceled) || errors.Is(res.Err, context.DeadlineExceeded) {
				return false, results, res.Err
			}
			problems = append(problems, fmt.Sprintf("%s: %v", res.Name, res.Err))
			continue
		}

		if res.RequiresReboot {
			requiresReboot = true
		}
	}

	if len(problems) > 0 {
		return requiresReboot, results, &EvaluationError{Problems: problems}
	}

	return requiresReboot, results, nil
}
