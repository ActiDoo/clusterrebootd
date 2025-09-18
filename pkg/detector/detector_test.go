package detector

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/clusterrebootd/clusterrebootd/pkg/config"
)

func TestFileDetector(t *testing.T) {
	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "reboot-required")

	detCfg := config.DetectorConfig{Type: "file", Path: marker, Name: "file"}
	det, err := NewFromConfig(detCfg)
	if err != nil {
		t.Fatalf("failed to create detector: %v", err)
	}

	needs, err := det.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if needs {
		t.Fatal("expected detector to report false when file is absent")
	}

	if err := os.WriteFile(marker, []byte("reboot"), 0o644); err != nil {
		t.Fatalf("failed to create marker: %v", err)
	}

	needs, err = det.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !needs {
		t.Fatal("expected detector to report true when file exists")
	}
}

func TestCommandDetectorDefaultExitCodes(t *testing.T) {
	detCfg := config.DetectorConfig{Type: "command", Cmd: []string{"/bin/sh", "-c", "exit 0"}, Name: "cmd"}
	det, err := NewFromConfig(detCfg)
	if err != nil {
		t.Fatalf("failed to create detector: %v", err)
	}

	needs, err := det.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if needs {
		t.Fatal("expected exit 0 to report no reboot required")
	}

	detCfg.Cmd = []string{"/bin/sh", "-c", "exit 5"}
	det, err = NewFromConfig(detCfg)
	if err != nil {
		t.Fatalf("failed to create detector: %v", err)
	}
	needs, err = det.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !needs {
		t.Fatal("expected non-zero exit code to require reboot")
	}
}

func TestCommandDetectorCustomExitCodes(t *testing.T) {
	detCfg := config.DetectorConfig{
		Type:             "command",
		Cmd:              []string{"/bin/sh", "-c", "exit 4"},
		SuccessExitCodes: []int{4},
		Name:             "cmd-custom",
	}
	det, err := NewFromConfig(detCfg)
	if err != nil {
		t.Fatalf("failed to create detector: %v", err)
	}

	needs, err := det.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !needs {
		t.Fatal("expected configured exit code to trigger reboot")
	}

	detCfg.Cmd = []string{"/bin/sh", "-c", "exit 0"}
	det, err = NewFromConfig(detCfg)
	if err != nil {
		t.Fatalf("failed to create detector: %v", err)
	}
	needs, err = det.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if needs {
		t.Fatal("exit 0 should not trigger reboot when success_exit_codes is [4]")
	}
}

func TestCommandDetectorTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep command not available on Windows test environment")
	}
	detCfg := config.DetectorConfig{
		Type:       "command",
		Cmd:        []string{"/bin/sh", "-c", "sleep 2"},
		TimeoutSec: 1,
		Name:       "cmd-timeout",
	}
	det, err := NewFromConfig(detCfg)
	if err != nil {
		t.Fatalf("failed to create detector: %v", err)
	}

	start := time.Now()
	_, err = det.Check(context.Background())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout message, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout should cancel execution promptly; took %s", time.Since(start))
	}
}

func TestUnsupportedDetectorType(t *testing.T) {
	_, err := NewFromConfig(config.DetectorConfig{Type: "unknown"})
	if err == nil {
		t.Fatal("expected error for unknown detector type")
	}
	if !strings.Contains(err.Error(), "unsupported detector type") {
		t.Fatalf("unexpected error: %v", err)
	}
}
