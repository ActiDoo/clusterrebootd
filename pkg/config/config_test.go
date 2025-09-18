package config

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeValidConfig(t *testing.T) {
	yaml := `node_name: node-1
reboot_required_detectors:
  - type: file
    path: /var/run/reboot-required
health_script: /usr/local/bin/health.sh
health_timeout_sec: 45
check_interval_sec: 75
lock_ttl_sec: 120
etcd_endpoints: ["https://127.0.0.1:2379"]
reboot_command: ["/sbin/shutdown","-r","now"]
`

	cfg, err := decode(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("decode returned error: %v", err)
	}

	if cfg.NodeName != "node-1" {
		t.Fatalf("unexpected node name: %s", cfg.NodeName)
	}
	if len(cfg.RebootRequiredDetectors) != 1 {
		t.Fatalf("expected one detector, got %d", len(cfg.RebootRequiredDetectors))
	}
	if cfg.RebootRequiredDetectors[0].Name == "" {
		t.Fatal("expected detector name to be populated")
	}
	if cfg.HealthTimeoutSec != 45 {
		t.Fatalf("expected health timeout 45, got %d", cfg.HealthTimeoutSec)
	}
	if cfg.BackoffMinSec != 5 {
		t.Fatalf("expected default backoff_min_sec 5, got %d", cfg.BackoffMinSec)
	}
	if cfg.BackoffMaxSec != 60 {
		t.Fatalf("expected default backoff_max_sec 60, got %d", cfg.BackoffMaxSec)
	}
	if cfg.LockTTLSec != 120 {
		t.Fatalf("expected lock TTL 120, got %d", cfg.LockTTLSec)
	}
}

func TestValidateDetectsMissingFields(t *testing.T) {
	yaml := `node_name: ""
reboot_required_detectors: []
health_script: ""
etcd_endpoints: []
`
	_, err := decode(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if len(verr.Problems) == 0 {
		t.Fatal("expected problems to be reported")
	}
}

func TestDetectorValidation(t *testing.T) {
	cfg := Config{
		NodeName: "node-1",
		RebootRequiredDetectors: []DetectorConfig{
			{
				Type: "command",
			},
		},
		HealthScript:     "/bin/true",
		EtcdEndpoints:    []string{"https://127.0.0.1:2379"},
		RebootCommand:    []string{"/sbin/shutdown", "-r", "now"},
		LockTTLSec:       120,
		HealthTimeoutSec: 30,
		CheckIntervalSec: 30,
		BackoffMinSec:    5,
		BackoffMaxSec:    60,
		LockKey:          "/cluster/reboot-coordinator/lock",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation to fail for invalid detector")
	}
}
