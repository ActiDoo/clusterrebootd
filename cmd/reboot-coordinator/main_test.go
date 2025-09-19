package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/clusterrebootd/clusterrebootd/internal/testutil"
	"github.com/clusterrebootd/clusterrebootd/pkg/config"
	"github.com/clusterrebootd/clusterrebootd/pkg/lock"
	"github.com/clusterrebootd/clusterrebootd/pkg/orchestrator"
)

func TestCommandSimulateWithFileDetector(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file paths and /bin/true not available on Windows test environment")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	marker := filepath.Join(dir, "reboot-required")

	configData := fmt.Sprintf(`
node_name: node-a
reboot_required_detectors:
  - type: file
    path: %s
health_script: /bin/true
etcd_endpoints:
  - 127.0.0.1:2379
`, marker)

	if err := os.WriteFile(configPath, []byte(configData), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := commandSimulateWithWriters([]string{"--config", configPath}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK, got %d (stderr: %s)", exitCode, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "detector evaluations:") {
		t.Fatalf("expected detector evaluations section, got: %s", output)
	}
	if !strings.Contains(output, "clear") {
		t.Fatalf("expected clear status in output, got: %s", output)
	}
	if !strings.Contains(output, "overall reboot required: false") {
		t.Fatalf("expected overall reboot required false, got: %s", output)
	}

	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		t.Fatalf("failed to create marker: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = commandSimulateWithWriters([]string{"--config", configPath}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK after marker creation, got %d (stderr: %s)", exitCode, stderr.String())
	}
	output = stdout.String()
	if !strings.Contains(output, "reboot-required") {
		t.Fatalf("expected reboot-required status, got: %s", output)
	}
	if !strings.Contains(output, "overall reboot required: true") {
		t.Fatalf("expected overall reboot required true, got: %s", output)
	}
}

func TestCommandRunWithoutReboot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file paths and /bin/true not available on Windows test environment")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	marker := filepath.Join(dir, "reboot-required")
	killSwitch := filepath.Join(dir, "kill-switch")

	cluster := testutil.StartEmbeddedEtcd(t)
	endpoint := cluster.Endpoints[0]

	configData := fmt.Sprintf(`
node_name: node-a
reboot_required_detectors:
  - type: file
    path: %s
health_script: /bin/true
etcd_endpoints:
  - %s
kill_switch_file: %s
lock_key: /cluster/reboot-coordinator/lock
lock_ttl_sec: 120
`, marker, endpoint, killSwitch)

	if err := os.WriteFile(configPath, []byte(configData), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := commandRunWithWriters([]string{"--config", configPath, "--once"}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK, got %d (stderr: %s)", exitCode, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, fmt.Sprintf("outcome: %s", orchestrator.OutcomeNoAction)) {
		t.Fatalf("expected no_action outcome, got: %s", output)
	}
	if strings.Contains(output, "planned reboot command") {
		t.Fatalf("unexpected reboot command planning in output: %s", output)
	}
}

func TestCommandRunDryRunReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file paths and /bin/true not available on Windows test environment")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	marker := filepath.Join(dir, "reboot-required")
	killSwitch := filepath.Join(dir, "kill-switch")

	cluster := testutil.StartEmbeddedEtcd(t)
	endpoint := cluster.Endpoints[0]

	configData := fmt.Sprintf(`
node_name: node-a
reboot_required_detectors:
  - type: file
    path: %s
health_script: /bin/true
etcd_endpoints:
  - %s
kill_switch_file: %s
lock_key: /cluster/reboot-coordinator/lock
lock_ttl_sec: 120
`, marker, endpoint, killSwitch)

	if err := os.WriteFile(configPath, []byte(configData), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		t.Fatalf("failed to create marker: %v", err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := commandRunWithWriters([]string{"--config", configPath, "--dry-run", "--once"}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK for dry-run, got %d (stderr: %s)", exitCode, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, fmt.Sprintf("outcome: %s", orchestrator.OutcomeReady)) {
		t.Fatalf("expected ready outcome, got: %s", output)
	}
	if !strings.Contains(output, "dry-run enabled") {
		t.Fatalf("expected dry-run notice in output, got: %s", output)
	}
	if !strings.Contains(output, "planned reboot command") {
		t.Fatalf("expected planned reboot command, got: %s", output)
	}
}

func TestCommandRunWithMetricsEnabled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file paths and /bin/true not available on Windows test environment")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	marker := filepath.Join(dir, "reboot-required")
	killSwitch := filepath.Join(dir, "kill-switch")

	cluster := testutil.StartEmbeddedEtcd(t)
	endpoint := cluster.Endpoints[0]

	configData := fmt.Sprintf(`
node_name: node-a
reboot_required_detectors:
  - type: file
    path: %s
health_script: /bin/true
etcd_endpoints:
  - %s
kill_switch_file: %s
lock_key: /cluster/reboot-coordinator/lock
lock_ttl_sec: 120
metrics:
  enabled: true
  listen: 127.0.0.1:0
`, marker, endpoint, killSwitch)

	if err := os.WriteFile(configPath, []byte(configData), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := commandRunWithWriters([]string{"--config", configPath, "--dry-run", "--once"}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK for metrics-enabled run, got %d (stderr: %s)", exitCode, stderr.String())
	}

	if !strings.Contains(stderr.String(), "metrics server listening on") {
		t.Fatalf("expected metrics server startup message, stderr: %s", stderr.String())
	}
}

func TestCommandStatusNoReboot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file paths and /bin/true not available on Windows test environment")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	marker := filepath.Join(dir, "reboot-required")
	killSwitch := filepath.Join(dir, "kill-switch")

	cluster := testutil.StartEmbeddedEtcd(t)
	endpoint := cluster.Endpoints[0]

	configData := fmt.Sprintf(`
node_name: node-a
reboot_required_detectors:
  - type: file
    path: %s
health_script: /bin/true
etcd_endpoints:
  - %s
kill_switch_file: %s
lock_key: /cluster/reboot-coordinator/lock
lock_ttl_sec: 120
`, marker, endpoint, killSwitch)

	if err := os.WriteFile(configPath, []byte(configData), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := commandStatusWithWriters([]string{"--config", configPath}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK, got %d (stderr: %s)", exitCode, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "status evaluation for node node-a") {
		t.Fatalf("expected status header in output, got: %s", output)
	}
	if !strings.Contains(output, fmt.Sprintf("outcome: %s", orchestrator.OutcomeNoAction)) {
		t.Fatalf("expected no_action outcome, got: %s", output)
	}
}

func TestCommandStatusReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file paths and /bin/true not available on Windows test environment")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	marker := filepath.Join(dir, "reboot-required")
	killSwitch := filepath.Join(dir, "kill-switch")

	cluster := testutil.StartEmbeddedEtcd(t)
	endpoint := cluster.Endpoints[0]

	configData := fmt.Sprintf(`
node_name: node-a
reboot_required_detectors:
  - type: file
    path: %s
health_script: /bin/true
etcd_endpoints:
  - %s
kill_switch_file: %s
lock_key: /cluster/reboot-coordinator/lock
lock_ttl_sec: 120
`, marker, endpoint, killSwitch)

	if err := os.WriteFile(configPath, []byte(configData), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		t.Fatalf("failed to create marker: %v", err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := commandStatusWithWriters([]string{"--config", configPath}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK, got %d (stderr: %s)", exitCode, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, fmt.Sprintf("outcome: %s", orchestrator.OutcomeReady)) {
		t.Fatalf("expected ready outcome, got: %s", output)
	}
	if !strings.Contains(output, "dry-run enabled") {
		t.Fatalf("expected dry-run notice in output, got: %s", output)
	}
}

func TestCommandStatusLockUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file paths and /bin/true not available on Windows test environment")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	marker := filepath.Join(dir, "reboot-required")
	killSwitch := filepath.Join(dir, "kill-switch")

	cluster := testutil.StartEmbeddedEtcd(t)
	endpoint := cluster.Endpoints[0]

	configData := fmt.Sprintf(`
node_name: node-a
reboot_required_detectors:
  - type: file
    path: %s
health_script: /bin/true
etcd_endpoints:
  - %s
kill_switch_file: %s
lock_key: /cluster/reboot-coordinator/lock
lock_ttl_sec: 120
`, marker, endpoint, killSwitch)

	if err := os.WriteFile(configPath, []byte(configData), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		t.Fatalf("failed to create marker: %v", err)
	}

	manager, err := lock.NewEtcdManager(lock.EtcdManagerOptions{
		Endpoints: cluster.Endpoints,
		LockKey:   "/cluster/reboot-coordinator/lock",
		TTL:       120 * time.Second,
		NodeName:  "test-node",
		ProcessID: 1337,
	})
	if err != nil {
		t.Fatalf("failed to create lock manager: %v", err)
	}
	defer manager.Close()
	lease, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatalf("failed to acquire lock for setup: %v", err)
	}
	defer lease.Release(context.Background())

	var stdout, stderr bytes.Buffer
	exitCode := commandStatusWithWriters([]string{"--config", configPath}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK, got %d (stderr: %s)", exitCode, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, fmt.Sprintf("outcome: %s", orchestrator.OutcomeLockUnavailable)) {
		t.Fatalf("expected lock_unavailable outcome, got: %s", output)
	}
}

func TestCommandStatusSkipHealth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file paths and /bin/true not available on Windows test environment")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	marker := filepath.Join(dir, "reboot-required")
	missingHealth := filepath.Join(dir, "missing-health.sh")

	cluster := testutil.StartEmbeddedEtcd(t)
	endpoint := cluster.Endpoints[0]

	configData := fmt.Sprintf(`
node_name: node-a
reboot_required_detectors:
  - type: file
    path: %s
health_script: %s
etcd_endpoints:
  - %s
lock_key: /cluster/reboot-coordinator/lock
lock_ttl_sec: 120
`, marker, missingHealth, endpoint)

	if err := os.WriteFile(configPath, []byte(configData), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		t.Fatalf("failed to create marker: %v", err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := commandStatusWithWriters([]string{"--config", configPath, "--skip-health"}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK, got %d (stderr: %s)", exitCode, stderr.String())
	}

	if !strings.Contains(stderr.String(), "skipping health script execution (--skip-health)") {
		t.Fatalf("expected health skip notice in stderr, got: %s", stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, fmt.Sprintf("outcome: %s", orchestrator.OutcomeReady)) {
		t.Fatalf("expected ready outcome with skip health, got: %s", output)
	}
	if !strings.Contains(output, "health script skipped") {
		t.Fatalf("expected skip health message in output, got: %s", output)
	}
}

func TestCommandStatusSkipLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file paths and /bin/true not available on Windows test environment")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	marker := filepath.Join(dir, "reboot-required")

	configData := fmt.Sprintf(`
node_name: node-a
reboot_required_detectors:
  - type: file
    path: %s
health_script: /bin/true
etcd_endpoints:
  - 127.0.0.1:12379
lock_key: /cluster/reboot-coordinator/lock
lock_ttl_sec: 120
`, marker)

	if err := os.WriteFile(configPath, []byte(configData), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		t.Fatalf("failed to create marker: %v", err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := commandStatusWithWriters([]string{"--config", configPath, "--skip-lock"}, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("expected exitOK, got %d (stderr: %s)", exitCode, stderr.String())
	}

	if !strings.Contains(stderr.String(), "skipping lock acquisition (--skip-lock)") {
		t.Fatalf("expected lock skip notice in stderr, got: %s", stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, fmt.Sprintf("outcome: %s", orchestrator.OutcomeLockSkipped)) {
		t.Fatalf("expected lock_skipped outcome, got: %s", output)
	}
	if !strings.Contains(output, "lock acquisition skipped") {
		t.Fatalf("expected lock skip message in output, got: %s", output)
	}
	if !strings.Contains(output, "planned reboot command") {
		t.Fatalf("expected planned reboot command in skip-lock output, got: %s", output)
	}
}

func TestBuildBaseEnvironmentIncludesPolicyContext(t *testing.T) {
	minFrac := 0.6
	minAbs := 3
	cfg := &config.Config{
		NodeName:       "node-a",
		DryRun:         true,
		LockKey:        "/cluster/lock",
		EtcdEndpoints:  []string{"10.0.0.10:2379", "10.0.0.11:2379"},
		KillSwitchFile: "/etc/reboot-coordinator/disable",
		ClusterPolicies: config.ClusterPolicies{
			MinHealthyFraction:       &minFrac,
			MinHealthyAbsolute:       &minAbs,
			ForbidIfOnlyFallbackLeft: true,
			FallbackNodes:            []string{"control-plane-a", "control-plane-b"},
		},
		Windows: config.WindowsConfig{
			Allow: []string{"02:00-05:00"},
			Deny:  []string{"sat-sun"},
		},
	}

	env := buildBaseEnvironment(cfg)

	if got := env["RC_NODE_NAME"]; got != cfg.NodeName {
		t.Fatalf("expected RC_NODE_NAME %q, got %q", cfg.NodeName, got)
	}
	if got := env["RC_DRY_RUN"]; got != "true" {
		t.Fatalf("expected RC_DRY_RUN true, got %q", got)
	}
	if got := env["RC_ETCD_ENDPOINTS"]; got != "10.0.0.10:2379,10.0.0.11:2379" {
		t.Fatalf("unexpected RC_ETCD_ENDPOINTS: %q", got)
	}
	if got := env["RC_CLUSTER_MIN_HEALTHY_FRACTION"]; got != "0.6" {
		t.Fatalf("unexpected RC_CLUSTER_MIN_HEALTHY_FRACTION: %q", got)
	}
	if got := env["RC_CLUSTER_MIN_HEALTHY_ABSOLUTE"]; got != "3" {
		t.Fatalf("unexpected RC_CLUSTER_MIN_HEALTHY_ABSOLUTE: %q", got)
	}
	if got := env["RC_CLUSTER_FORBID_IF_ONLY_FALLBACK_LEFT"]; got != "true" {
		t.Fatalf("unexpected RC_CLUSTER_FORBID_IF_ONLY_FALLBACK_LEFT: %q", got)
	}
	if got := env["RC_CLUSTER_FALLBACK_NODES"]; got != "control-plane-a,control-plane-b" {
		t.Fatalf("unexpected RC_CLUSTER_FALLBACK_NODES: %q", got)
	}
	if got := env["RC_WINDOWS_ALLOW"]; got != "02:00-05:00" {
		t.Fatalf("unexpected RC_WINDOWS_ALLOW: %q", got)
	}
	if got := env["RC_WINDOWS_DENY"]; got != "sat-sun" {
		t.Fatalf("unexpected RC_WINDOWS_DENY: %q", got)
	}
}

func TestBuildBaseEnvironmentOmitsUnsetPolicyContext(t *testing.T) {
	cfg := &config.Config{}

	env := buildBaseEnvironment(cfg)

	if _, ok := env["RC_CLUSTER_MIN_HEALTHY_FRACTION"]; ok {
		t.Fatalf("expected RC_CLUSTER_MIN_HEALTHY_FRACTION to be absent")
	}
	if _, ok := env["RC_CLUSTER_MIN_HEALTHY_ABSOLUTE"]; ok {
		t.Fatalf("expected RC_CLUSTER_MIN_HEALTHY_ABSOLUTE to be absent")
	}
	if _, ok := env["RC_CLUSTER_FORBID_IF_ONLY_FALLBACK_LEFT"]; ok {
		t.Fatalf("expected RC_CLUSTER_FORBID_IF_ONLY_FALLBACK_LEFT to be absent")
	}
	if _, ok := env["RC_CLUSTER_FALLBACK_NODES"]; ok {
		t.Fatalf("expected RC_CLUSTER_FALLBACK_NODES to be absent")
	}
	if _, ok := env["RC_WINDOWS_ALLOW"]; ok {
		t.Fatalf("expected RC_WINDOWS_ALLOW to be absent")
	}
	if _, ok := env["RC_WINDOWS_DENY"]; ok {
		t.Fatalf("expected RC_WINDOWS_DENY to be absent")
	}
}
