package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
	"github.com/clusterrebootd/clusterrebootd/pkg/detector"
	"github.com/clusterrebootd/clusterrebootd/pkg/version"
)

const (
	exitOK             = 0
	exitUsage          = 64
	exitConfigError    = 65
	exitNotImplemented = 66
	exitDetectorError  = 67
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
  run                Start the coordinator daemon (currently a placeholder)
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

	if _, err := detector.NewAll(cfg.RebootRequiredDetectors); err != nil {
		fmt.Fprintf(stderr, "failed to construct detectors: %v\n", err)
		return exitConfigError
	}

	fmt.Fprintf(stdout, "configuration for node %s loaded (dry-run=%v); daemon mode not yet implemented\n", cfg.NodeName, cfg.DryRun)
	return exitNotImplemented
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

	for _, res := range results {
		status := "clear"
		if res.Err != nil {
			status = fmt.Sprintf("error: %v", res.Err)
		} else if res.RequiresReboot {
			status = "reboot-required"
		}

		fmt.Fprintf(stdout, "  - %s => %s (duration %s)\n", res.Name, status, res.Duration.Round(time.Millisecond))

		if res.CommandOutput != nil {
			fmt.Fprintf(stdout, "      exit code: %d\n", res.CommandOutput.ExitCode)
			if out := strings.TrimSpace(res.CommandOutput.Stdout); out != "" {
				fmt.Fprintf(stdout, "      stdout: %s\n", out)
			}
			if errText := strings.TrimSpace(res.CommandOutput.Stderr); errText != "" {
				fmt.Fprintf(stdout, "      stderr: %s\n", errText)
			}
		}
	}

	fmt.Fprintf(stdout, "overall reboot required: %v\n", requiresReboot)

	if evalErr != nil {
		fmt.Fprintf(stderr, "detector evaluation encountered errors: %v\n", evalErr)
		return exitDetectorError
	}

	fmt.Fprintln(stdout, "no reboot actions performed in simulation mode")
	return exitOK
}
