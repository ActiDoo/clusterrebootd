package detector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
)

// Detector represents a reboot requirement probe.
type Detector interface {
	Name() string
	Check(ctx context.Context) (bool, error)
}

// NewFromConfig instantiates a detector based on the provided configuration.
func NewFromConfig(cfg config.DetectorConfig) (Detector, error) {
	switch cfg.Type {
	case "file":
		return &FileDetector{name: cfg.Name, path: cfg.Path}, nil
	case "command":
		return newCommandDetector(cfg)
	default:
		return nil, fmt.Errorf("unsupported detector type %q", cfg.Type)
	}
}

// NewAll constructs a slice of detectors from configuration.
func NewAll(cfgs []config.DetectorConfig) ([]Detector, error) {
	detectors := make([]Detector, 0, len(cfgs))
	for _, cfg := range cfgs {
		detector, err := NewFromConfig(cfg)
		if err != nil {
			return nil, err
		}
		detectors = append(detectors, detector)
	}
	return detectors, nil
}

// FileDetector reports a reboot requirement based on the presence of a file on disk.
type FileDetector struct {
	name string
	path string
}

func (d *FileDetector) Name() string { return d.name }

func (d *FileDetector) Check(ctx context.Context) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	if strings.TrimSpace(d.path) == "" {
		return false, errors.New("file detector path must not be empty")
	}

	_, err := os.Stat(d.path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", d.path, err)
}

type commandDetector struct {
	name      string
	command   []string
	exitCodes map[int]struct{}
	timeout   time.Duration
}

func newCommandDetector(cfg config.DetectorConfig) (Detector, error) {
	if len(cfg.Cmd) == 0 {
		return nil, errors.New("command detector requires cmd to be set")
	}
	det := &commandDetector{
		name:      cfg.Name,
		command:   append([]string(nil), cfg.Cmd...),
		exitCodes: make(map[int]struct{}),
	}
	if cfg.TimeoutSec > 0 {
		det.timeout = time.Duration(cfg.TimeoutSec) * time.Second
	}
	for _, code := range cfg.SuccessExitCodes {
		det.exitCodes[code] = struct{}{}
	}
	return det, nil
}

func (d *commandDetector) Name() string { return d.name }

func (d *commandDetector) Check(ctx context.Context) (bool, error) {
	needsReboot, _, err := d.EvaluateCommand(ctx)
	return needsReboot, err
}

// CommandOutput summarises execution results for richer diagnostics.
type CommandOutput struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

func (d *commandDetector) execute(ctx context.Context) (CommandOutput, error) {
	execCtx := ctx
	var cancel context.CancelFunc
	if d.timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, d.timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(execCtx, d.command[0], d.command[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if execCtx.Err() != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return CommandOutput{}, fmt.Errorf("command detector %s timed out after %s", d.name, d.timeout)
		}
		return CommandOutput{}, execCtx.Err()
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return CommandOutput{}, fmt.Errorf("command detector %s failed: %w", d.name, err)
		}
	}

	return CommandOutput{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}

// EvaluateCommand executes the command and returns a bool and captured output.
func (d *commandDetector) EvaluateCommand(ctx context.Context) (bool, CommandOutput, error) {
	output, err := d.execute(ctx)
	if err != nil {
		return false, output, err
	}
	if len(d.exitCodes) == 0 {
		return output.ExitCode != 0, output, nil
	}
	_, ok := d.exitCodes[output.ExitCode]
	return ok, output, nil
}

// Ensure commandDetector implements Detector.
var _ Detector = (*commandDetector)(nil)
var _ Detector = (*FileDetector)(nil)
