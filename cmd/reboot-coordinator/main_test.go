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
