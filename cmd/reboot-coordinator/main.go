package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
	"github.com/clusterrebootd/clusterrebootd/pkg/detector"
	"github.com/clusterrebootd/clusterrebootd/pkg/health"
	"github.com/clusterrebootd/clusterrebootd/pkg/lock"
	"github.com/clusterrebootd/clusterrebootd/pkg/orchestrator"
	"github.com/clusterrebootd/clusterrebootd/pkg/version"
)

const (
	exitOK             = 0
	exitUsage          = 64
	exitConfigError    = 65
	exitNotImplemented = 66
	exitDetectorError  = 67
	exitRunError       = 68
)

func main() {
	exitCode := run(os.Args[1:])
	os.Exit(exitCode)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}

	switch args[0] {
	case "run":
		return commandRun(args[1:])
	case "validate-config":
		return commandValidate(args[1:])
	case "simulate":
		return commandSimulate(args[1:])
	case "status":
		fmt.Fprintln(os.Stderr, "status command is not implemented yet")
		return exitNotImplemented
	case "version":
		fmt.Println(version.Version)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return exitUsage
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: reboot-coordinator <command> [options]
Commands:
  run                Execute a single orchestration pass
  validate-config    Validate the configuration file
  simulate           Validate detectors and show configuration summary
  status             Display current coordinator status
  version            Print build version
`)
}

func commandRun(args []string) int {
	return commandRunWithWriters(args, os.Stdout, os.Stderr)
}

func commandRunWithWriters(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultConfigPath, "path to configuration file")
	dryRun := fs.Bool("dry-run", false, "enable dry-run mode")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "failed to load configuration: %v\n", err)
		return exitConfigError
	}
	if *dryRun {
		cfg.DryRun = true
	}

	detectors, err := detector.NewAll(cfg.RebootRequiredDetectors)
	if err != nil {
		fmt.Fprintf(stderr, "failed to construct detectors: %v\n", err)
		return exitConfigError
	}

	engine, err := detector.NewEngine(detectors)
	if err != nil {
		fmt.Fprintf(stderr, "failed to initialise detector engine: %v\n", err)
		return exitConfigError
	}

	baseEnv := map[string]string{
		"RC_NODE_NAME": cfg.NodeName,
		"RC_DRY_RUN":   strconv.FormatBool(cfg.DryRun),
	}
	if cfg.LockKey != "" {
		baseEnv["RC_LOCK_KEY"] = cfg.LockKey
	}
	if len(cfg.EtcdEndpoints) > 0 {
		baseEnv["RC_ETCD_ENDPOINTS"] = strings.Join(cfg.EtcdEndpoints, ",")
	}
	if cfg.KillSwitchFile != "" {
		baseEnv["RC_KILL_SWITCH_FILE"] = cfg.KillSwitchFile
	}

	healthRunner, err := health.NewScriptRunner(cfg.HealthScript, cfg.HealthTimeout(), baseEnv)
	if err != nil {
		fmt.Fprintf(stderr, "failed to construct health runner: %v\n", err)
		return exitConfigError
	}

	locker := lock.NewNoopManager()

	runner, err := orchestrator.NewRunner(cfg, engine, healthRunner, locker)
	if err != nil {
		fmt.Fprintf(stderr, "failed to initialise orchestrator: %v\n", err)
		return exitRunError
	}

	fmt.Fprintf(stdout, "starting orchestration for node %s (dry-run=%v)\n", cfg.NodeName, cfg.DryRun)

	outcome, runErr := runner.RunOnce(context.Background())
	if runErr != nil {
		fmt.Fprintf(stderr, "orchestration error: %v\n", runErr)
		return exitRunError
	}

	fmt.Fprintln(stdout, "pre-lock detector evaluations:")
	writeDetectorResults(stdout, outcome.DetectorResults)
	if outcome.PreLockHealthResult != nil {
		writeHealthResult(stdout, "pre-lock health", outcome.PreLockHealthResult)
	}

	if outcome.LockAcquired {
		fmt.Fprintln(stdout, "lock acquired")
	}

	if len(outcome.PostLockDetectorResults) > 0 {
		fmt.Fprintln(stdout, "post-lock detector evaluations:")
		writeDetectorResults(stdout, outcome.PostLockDetectorResults)
	}
	if outcome.PostLockHealthResult != nil {
		writeHealthResult(stdout, "post-lock health", outcome.PostLockHealthResult)
	}

	fmt.Fprintf(stdout, "outcome: %s - %s\n", outcome.Status, outcome.Message)
	if len(outcome.Command) > 0 {
		fmt.Fprintf(stdout, "planned reboot command: %s\n", strings.Join(outcome.Command, " "))
	}
	if outcome.DryRun {
		fmt.Fprintln(stdout, "dry-run enabled: reboot command not executed")
	} else if outcome.Status == orchestrator.OutcomeReady {
		fmt.Fprintln(stdout, "reboot command execution not yet implemented; operator action required")
		return exitNotImplemented
	}

	return exitOK
}

func commandValidate(args []string) int {
	return commandValidateWithWriters(args, os.Stdout, os.Stderr)
}

func commandValidateWithWriters(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultConfigPath, "path to configuration file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if _, err := config.Load(*configPath); err != nil {
		fmt.Fprintf(stderr, "configuration invalid: %v\n", err)
		return exitConfigError
	}

	fmt.Fprintf(stdout, "configuration at %s is valid\n", *configPath)
	return exitOK
}

func commandSimulate(args []string) int {
	return commandSimulateWithWriters(args, os.Stdout, os.Stderr)
}

func commandSimulateWithWriters(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultConfigPath, "path to configuration file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "failed to load configuration: %v\n", err)
		return exitConfigError
	}

	detectors, err := detector.NewAll(cfg.RebootRequiredDetectors)
	if err != nil {
		fmt.Fprintf(stderr, "failed to construct detectors: %v\n", err)
		return exitConfigError
	}

	engine, err := detector.NewEngine(detectors)
	if err != nil {
		fmt.Fprintf(stderr, "failed to initialise detector engine: %v\n", err)
		return exitConfigError
	}

	requiresReboot, results, evalErr := engine.Evaluate(context.Background())

	names := make([]string, 0, len(results))
	for _, res := range results {
		names = append(names, res.Name)
	}

	fmt.Fprintf(stdout, "node %s configuration summary:\n", cfg.NodeName)
	fmt.Fprintf(stdout, "  detectors: %s\n", strings.Join(names, ", "))
	fmt.Fprintf(stdout, "  health script: %s\n", cfg.HealthScript)
	fmt.Fprintf(stdout, "  etcd endpoints: %s\n", strings.Join(cfg.EtcdEndpoints, ", "))
	fmt.Fprintf(stdout, "  reboot command: %s\n", strings.Join(cfg.RebootCommand, " "))
	fmt.Fprintln(stdout, "detector evaluations:")
	writeDetectorResults(stdout, results)

	fmt.Fprintf(stdout, "overall reboot required: %v\n", requiresReboot)

	if evalErr != nil {
		fmt.Fprintf(stderr, "detector evaluation encountered errors: %v\n", evalErr)
		return exitDetectorError
	}

	fmt.Fprintln(stdout, "no reboot actions performed in simulation mode")
	return exitOK
}

func writeDetectorResults(w io.Writer, results []detector.Result) {
	for _, res := range results {
		status := "clear"
		if res.Err != nil {
			status = fmt.Sprintf("error: %v", res.Err)
		} else if res.RequiresReboot {
			status = "reboot-required"
		}

		fmt.Fprintf(w, "  - %s => %s (duration %s)\n", res.Name, status, res.Duration.Round(time.Millisecond))

		if res.CommandOutput != nil {
			fmt.Fprintf(w, "      exit code: %d\n", res.CommandOutput.ExitCode)
			if out := strings.TrimSpace(res.CommandOutput.Stdout); out != "" {
				fmt.Fprintf(w, "      stdout: %s\n", out)
			}
			if errText := strings.TrimSpace(res.CommandOutput.Stderr); errText != "" {
				fmt.Fprintf(w, "      stderr: %s\n", errText)
			}
		}
	}
}

func writeHealthResult(w io.Writer, label string, res *health.Result) {
	fmt.Fprintf(w, "%s: exit=%d duration=%s\n", label, res.ExitCode, res.Duration.Round(time.Millisecond))
	if out := strings.TrimSpace(res.Stdout); out != "" {
		fmt.Fprintf(w, "  stdout: %s\n", out)
	}
	if errText := strings.TrimSpace(res.Stderr); errText != "" {
		fmt.Fprintf(w, "  stderr: %s\n", errText)
	}
}
