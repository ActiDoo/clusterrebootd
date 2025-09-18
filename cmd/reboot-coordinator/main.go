package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
  run                Start the orchestration loop (use --once for a single pass)
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
	once := fs.Bool("once", false, "execute a single orchestration pass and exit")
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

	tlsConfig, err := buildEtcdTLSConfig(cfg.EtcdTLS)
	if err != nil {
		fmt.Fprintf(stderr, "failed to configure etcd TLS: %v\n", err)
		return exitConfigError
	}

	locker, err := lock.NewEtcdManager(lock.EtcdManagerOptions{
		Endpoints:   cfg.EtcdEndpoints,
		DialTimeout: 5 * time.Second,
		LockKey:     cfg.LockKey,
		Namespace:   cfg.EtcdNamespace,
		TTL:         cfg.LockTTL(),
		TLS:         tlsConfig,
	})
	if err != nil {
		fmt.Fprintf(stderr, "failed to initialise lock manager: %v\n", err)
		return exitRunError
	}
	defer locker.Close()

	runner, err := orchestrator.NewRunner(cfg, engine, healthRunner, locker)
	if err != nil {
		fmt.Fprintf(stderr, "failed to initialise orchestrator: %v\n", err)
		return exitRunError
	}

	fmt.Fprintf(stdout, "starting orchestration for node %s (dry-run=%v)\n", cfg.NodeName, cfg.DryRun)

	if *once {
		outcome, runErr := runner.RunOnce(context.Background())
		if runErr != nil {
			fmt.Fprintf(stderr, "orchestration error: %v\n", runErr)
			return exitRunError
		}
		reportOutcome(stdout, outcome)
		if !outcome.DryRun && outcome.Status == orchestrator.OutcomeReady {
			fmt.Fprintln(stdout, "ready outcome reached; run without --once to execute the reboot command")
		}
		return exitOK
	}

	tracker := &trackingExecutor{delegate: orchestrator.NewExecCommandExecutor(nil, nil)}
	var lastOutcome orchestrator.Outcome
	iteration := 0
	loop, err := orchestrator.NewLoop(cfg, runner, tracker, orchestrator.WithLoopIterationHook(func(outcome orchestrator.Outcome) {
		iteration++
		fmt.Fprintf(stdout, "iteration %d results:\n", iteration)
		reportOutcome(stdout, outcome)
		fmt.Fprintln(stdout)
		lastOutcome = outcome
	}))
	if err != nil {
		fmt.Fprintf(stderr, "failed to initialise orchestration loop: %v\n", err)
		return exitRunError
	}

	if err := loop.Run(context.Background()); err != nil {
		fmt.Fprintf(stderr, "orchestration loop error: %v\n", err)
		return exitRunError
	}

	if lastOutcome.Status == orchestrator.OutcomeReady && !cfg.DryRun {
		if tracker.executed {
			fmt.Fprintln(stdout, "reboot command invoked; system reboot should be in progress")
		} else {
			fmt.Fprintln(stdout, "ready outcome reached but reboot command was not executed")
		}
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

func reportOutcome(stdout io.Writer, outcome orchestrator.Outcome) {
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
	}
}

type trackingExecutor struct {
	delegate orchestrator.CommandExecutor
	executed bool
}

func (t *trackingExecutor) Execute(ctx context.Context, command []string) error {
	if err := t.delegate.Execute(ctx, command); err != nil {
		return err
	}
	t.executed = true
	return nil
}

func buildEtcdTLSConfig(cfg *config.EtcdTLSConfig) (*tls.Config, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load etcd client certificate: %w", err)
	}

	caBytes, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read etcd CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("parse etcd CA file: %s", cfg.CAFile)
	}

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            pool,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.Insecure,
	}

	return tlsConfig, nil
}
