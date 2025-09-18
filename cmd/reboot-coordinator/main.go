package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
	"github.com/clusterrebootd/clusterrebootd/pkg/detector"
	"github.com/clusterrebootd/clusterrebootd/pkg/version"
)

const (
	exitOK             = 0
	exitUsage          = 64
	exitConfigError    = 65
	exitNotImplemented = 66
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
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", config.DefaultConfigPath, "path to configuration file")
	dryRun := fs.Bool("dry-run", false, "enable dry-run mode")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load configuration: %v\n", err)
		return exitConfigError
	}
	if *dryRun {
		cfg.DryRun = true
	}

	if _, err := detector.NewAll(cfg.RebootRequiredDetectors); err != nil {
		fmt.Fprintf(os.Stderr, "failed to construct detectors: %v\n", err)
		return exitConfigError
	}

	fmt.Fprintf(os.Stdout, "configuration for node %s loaded (dry-run=%v); daemon mode not yet implemented\n", cfg.NodeName, cfg.DryRun)
	return exitNotImplemented
}

func commandValidate(args []string) int {
	fs := flag.NewFlagSet("validate-config", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", config.DefaultConfigPath, "path to configuration file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if _, err := config.Load(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "configuration invalid: %v\n", err)
		return exitConfigError
	}

	fmt.Fprintf(os.Stdout, "configuration at %s is valid\n", *configPath)
	return exitOK
}

func commandSimulate(args []string) int {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", config.DefaultConfigPath, "path to configuration file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load configuration: %v\n", err)
		return exitConfigError
	}

	detectors, err := detector.NewAll(cfg.RebootRequiredDetectors)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to construct detectors: %v\n", err)
		return exitConfigError
	}

	names := make([]string, 0, len(detectors))
	for _, det := range detectors {
		names = append(names, det.Name())
	}

	fmt.Fprintf(os.Stdout, "node %s configuration summary:\n", cfg.NodeName)
	fmt.Fprintf(os.Stdout, "  detectors: %s\n", strings.Join(names, ", "))
	fmt.Fprintf(os.Stdout, "  health script: %s\n", cfg.HealthScript)
	fmt.Fprintf(os.Stdout, "  etcd endpoints: %s\n", strings.Join(cfg.EtcdEndpoints, ", "))
	fmt.Fprintf(os.Stdout, "  reboot command: %s\n", strings.Join(cfg.RebootCommand, " "))
	fmt.Fprintln(os.Stdout, "no reboot actions performed in simulation mode")
	return exitOK
}
