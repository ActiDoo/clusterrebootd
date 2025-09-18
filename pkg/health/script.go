package health

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ScriptRunner executes an external health script enforcing timeouts and environment injection.
type ScriptRunner struct {
	path    string
	timeout time.Duration
	env     map[string]string
}

// Result captures the outcome of executing the health script.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// NewScriptRunner constructs a runner for the provided script path.
func NewScriptRunner(path string, timeout time.Duration, baseEnv map[string]string) (*ScriptRunner, error) {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return nil, errors.New("health script path must not be empty")
	}
	if !filepath.IsAbs(cleaned) {
		return nil, fmt.Errorf("health script path must be absolute: %s", path)
	}
	envCopy := make(map[string]string, len(baseEnv))
	for k, v := range baseEnv {
		envCopy[k] = v
	}
	return &ScriptRunner{path: cleaned, timeout: timeout, env: envCopy}, nil
}

// Run executes the health script, combining the runner environment with extraEnv.
func (r *ScriptRunner) Run(ctx context.Context, extraEnv map[string]string) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	execCtx := ctx
	var cancel context.CancelFunc
	if r.timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(execCtx, r.path)
	cmd.Env = append(os.Environ(), formatEnv(r.env)...)
	cmd.Env = append(cmd.Env, formatEnv(extraEnv)...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	result := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
	}

	if execCtx.Err() != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return result, fmt.Errorf("health script timed out after %s", r.timeout)
		}
		return result, execCtx.Err()
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("health script execution failed: %w", err)
	}

	result.ExitCode = 0
	return result, nil
}

func formatEnv(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	formatted := make([]string, 0, len(values))
	for k, v := range values {
		formatted = append(formatted, fmt.Sprintf("%s=%s", k, v))
	}
	return formatted
}

// Path returns the configured script path.
func (r *ScriptRunner) Path() string {
	return r.path
}

// Timeout returns the configured timeout duration.
func (r *ScriptRunner) Timeout() time.Duration {
	return r.timeout
}
