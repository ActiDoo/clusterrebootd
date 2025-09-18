package detector

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
)

type stubDetector struct {
	name    string
	require bool
	err     error
	delay   time.Duration
}

func (s *stubDetector) Name() string { return s.name }

func (s *stubDetector) Check(ctx context.Context) (bool, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return s.require, s.err
}

func TestNewEngineValidation(t *testing.T) {
	if _, err := NewEngine(nil); err == nil {
		t.Fatal("expected error for empty detector slice")
	}

	det1 := &stubDetector{name: "det-1"}
	det2 := &stubDetector{name: "det-1"}
	if _, err := NewEngine([]Detector{det1, det2}); err == nil {
		t.Fatal("expected error for duplicate detector names")
	}

	det3 := &stubDetector{name: "   "}
	if _, err := NewEngine([]Detector{det3}); err == nil {
		t.Fatal("expected error for empty detector name")
	}
}

func TestEngineEvaluateAggregatesResults(t *testing.T) {
	engine, err := NewEngine([]Detector{
		&stubDetector{name: "det-1", delay: 10 * time.Millisecond},
		&stubDetector{name: "det-2", require: true},
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	requires, results, evalErr := engine.Evaluate(context.Background())
	if evalErr != nil {
		t.Fatalf("unexpected evaluation error: %v", evalErr)
	}
	if !requires {
		t.Fatal("expected aggregate to report reboot required")
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Duration < 10*time.Millisecond {
		t.Fatalf("expected detector duration to include delay, got %s", results[0].Duration)
	}
	if results[1].Err != nil {
		t.Fatalf("unexpected error in result: %v", results[1].Err)
	}
}

func TestEngineEvaluateReportsErrors(t *testing.T) {
	failing := errors.New("boom")
	engine, err := NewEngine([]Detector{
		&stubDetector{name: "ok"},
		&stubDetector{name: "fail", err: failing},
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	requires, results, evalErr := engine.Evaluate(context.Background())
	if evalErr == nil {
		t.Fatal("expected evaluation error")
	}
	var multi *EvaluationError
	if !errors.As(evalErr, &multi) {
		t.Fatalf("expected EvaluationError, got %T", evalErr)
	}
	if requires {
		t.Fatal("aggregate reboot flag should be false when only success detector returns false")
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[1].Err == nil {
		t.Fatal("expected result error to be captured")
	}
	if !strings.Contains(multi.Error(), "fail: boom") {
		t.Fatalf("unexpected aggregated error: %v", multi)
	}
}

func TestEngineEvaluateCapturesCommandOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows test environment")
	}
	detCfg := config.DetectorConfig{
		Type: "command",
		Cmd:  []string{"/bin/sh", "-c", "printf ok"},
		Name: "cmd",
	}
	det, err := NewFromConfig(detCfg)
	if err != nil {
		t.Fatalf("failed to create command detector: %v", err)
	}

	engine, err := NewEngine([]Detector{det})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	requires, results, evalErr := engine.Evaluate(context.Background())
	if evalErr != nil {
		t.Fatalf("unexpected evaluation error: %v", evalErr)
	}
	if requires {
		t.Fatal("expected command to report no reboot required")
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].CommandOutput == nil {
		t.Fatal("expected command output to be captured")
	}
	if results[0].CommandOutput.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", results[0].CommandOutput.ExitCode)
	}
	if results[0].CommandOutput.Stdout != "ok" {
		t.Fatalf("unexpected stdout: %q", results[0].CommandOutput.Stdout)
	}
}
