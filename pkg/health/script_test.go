package health

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestScriptRunnerSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows test environment")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "health.sh")
	script := "#!/bin/sh\necho ok\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	runner, err := NewScriptRunner(scriptPath, time.Second, map[string]string{"NODE_NAME": "node-1"})
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	result, err := runner.Run(context.Background(), map[string]string{"EXTRA": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "ok" {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestScriptRunnerNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows test environment")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "health.sh")
	script := "#!/bin/sh\necho fail >&2\nexit 5\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	runner, err := NewScriptRunner(scriptPath, time.Second, nil)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	result, err := runner.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 5 {
		t.Fatalf("expected exit code 5, got %d", result.ExitCode)
	}
	if strings.TrimSpace(result.Stderr) != "fail" {
		t.Fatalf("unexpected stderr: %q", result.Stderr)
	}
}

func TestScriptRunnerTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows test environment")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "health.sh")
	script := "#!/bin/sh\nsleep 2\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	runner, err := NewScriptRunner(scriptPath, time.Second, nil)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	start := time.Now()
	_, err = runner.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout did not trigger promptly: %s", time.Since(start))
	}
}

func TestScriptRunnerRejectsRelativePaths(t *testing.T) {
	_, err := NewScriptRunner("health.sh", time.Second, nil)
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("unexpected error: %v", err)
	}
}
