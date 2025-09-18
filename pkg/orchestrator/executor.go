package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// CommandExecutor executes the reboot command once prerequisites are satisfied.
type CommandExecutor interface {
	Execute(ctx context.Context, command []string) error
}

// ExecCommandExecutor shells out to the configured command using os/exec.
type ExecCommandExecutor struct {
	Stdout io.Writer
	Stderr io.Writer
}

// NewExecCommandExecutor constructs an ExecCommandExecutor with optional output writers.
// When stdout or stderr are nil, the process inherits os.Stdout/os.Stderr.
func NewExecCommandExecutor(stdout, stderr io.Writer) *ExecCommandExecutor {
	return &ExecCommandExecutor{Stdout: stdout, Stderr: stderr}
}

// Execute runs the provided command, streaming stdout/stderr to the configured writers.
func (e *ExecCommandExecutor) Execute(ctx context.Context, command []string) error {
	if len(command) == 0 {
		return errors.New("reboot command is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if e != nil {
		if e.Stdout != nil {
			cmd.Stdout = e.Stdout
		} else {
			cmd.Stdout = os.Stdout
		}
		if e.Stderr != nil {
			cmd.Stderr = e.Stderr
		} else {
			cmd.Stderr = os.Stderr
		}
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run reboot command %q: %w", strings.Join(command, " "), err)
	}
	return nil
}

var _ CommandExecutor = (*ExecCommandExecutor)(nil)
