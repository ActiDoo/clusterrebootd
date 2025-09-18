package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
