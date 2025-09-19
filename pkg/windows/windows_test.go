package windows

import (
	"testing"
	"time"
)

func TestEvaluatorMatchesDenyWindow(t *testing.T) {
	eval, err := NewEvaluator(nil, []string{"Mon 00:00-Tue 00:00"})
	if err != nil {
		t.Fatalf("failed to parse windows: %v", err)
	}
	if eval == nil {
		t.Fatal("expected evaluator, got nil")
	}

	ts := time.Date(2024, time.March, 4, 12, 0, 0, 0, time.UTC) // Monday
	decision := eval.Evaluate(ts)
	if decision.Allowed {
		t.Fatalf("expected decision to block, got allowed")
	}
	if decision.MatchedDeny == nil {
		t.Fatalf("expected deny match, got nil")
	}
	if decision.MatchedDeny.Expression != "Mon 00:00-Tue 00:00" {
		t.Fatalf("unexpected expression: %q", decision.MatchedDeny.Expression)
	}
}

func TestEvaluatorRequiresAllowMatch(t *testing.T) {
	eval, err := NewEvaluator([]string{"Tue 22:00-23:00"}, nil)
	if err != nil {
		t.Fatalf("failed to parse windows: %v", err)
	}
	if eval == nil {
		t.Fatal("expected evaluator, got nil")
	}

	ts := time.Date(2024, time.March, 4, 10, 0, 0, 0, time.UTC) // Monday
	decision := eval.Evaluate(ts)
	if decision.Allowed {
		t.Fatalf("expected decision to block outside allow, got allowed")
	}
	if !decision.AllowConfigured {
		t.Fatalf("expected allow to be configured")
	}
	if decision.MatchedAllow != nil {
		t.Fatalf("expected no allow match, got %q", decision.MatchedAllow.Expression)
	}
}

func TestEvaluatorMatchesAllowWindow(t *testing.T) {
	eval, err := NewEvaluator([]string{"Tue 22:00-23:00"}, nil)
	if err != nil {
		t.Fatalf("failed to parse windows: %v", err)
	}

	ts := time.Date(2024, time.March, 5, 22, 30, 0, 0, time.UTC) // Tuesday
	decision := eval.Evaluate(ts)
	if !decision.Allowed {
		t.Fatalf("expected decision to allow, got blocked")
	}
	if decision.MatchedAllow == nil {
		t.Fatalf("expected allow match, got nil")
	}
}

func TestEvaluatorCrossMidnight(t *testing.T) {
	eval, err := NewEvaluator(nil, []string{"* 23:00-06:00"})
	if err != nil {
		t.Fatalf("failed to parse windows: %v", err)
	}

	ts := time.Date(2024, time.March, 3, 23, 30, 0, 0, time.UTC) // Sunday
	if decision := eval.Evaluate(ts); decision.Allowed {
		t.Fatalf("expected block during overnight window")
	}

	ts = time.Date(2024, time.March, 4, 7, 0, 0, 0, time.UTC) // Monday 07:00
	if decision := eval.Evaluate(ts); !decision.Allowed {
		t.Fatalf("expected allowance after window ends")
	}
}

func TestEvaluatorRejectsInvalidExpressions(t *testing.T) {
	if _, err := NewEvaluator([]string{"not-a-window"}, nil); err == nil {
		t.Fatalf("expected error for invalid expression")
	}
}
