package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultConfigPath = "/etc/clusterrebootd/config.yaml"

// Config represents the runtime configuration for the reboot coordinator daemon.
type Config struct {
	NodeName                 string           `yaml:"node_name"`
	RebootRequiredDetectors  []DetectorConfig `yaml:"reboot_required_detectors"`
	HealthScript             string           `yaml:"health_script"`
	HealthTimeoutSec         int              `yaml:"health_timeout_sec"`
	HealthPublishIntervalSec int              `yaml:"health_publish_interval_sec"`
	CheckIntervalSec         int              `yaml:"check_interval_sec"`
	BackoffMinSec            int              `yaml:"backoff_min_sec"`
	BackoffMaxSec            int              `yaml:"backoff_max_sec"`
	MinRebootIntervalSec     int              `yaml:"min_reboot_interval_sec"`
	LockKey                  string           `yaml:"lock_key"`
	LockTTLSec               int              `yaml:"lock_ttl_sec"`
	EtcdEndpoints            []string         `yaml:"etcd_endpoints"`
	EtcdNamespace            string           `yaml:"etcd_namespace"`
	EtcdTLS                  *EtcdTLSConfig   `yaml:"etcd_tls"`
	RebootCommand            []string         `yaml:"reboot_command"`
	ClusterPolicies          ClusterPolicies  `yaml:"cluster_policies"`
	Windows                  WindowsConfig    `yaml:"windows"`
	KillSwitchFile           string           `yaml:"kill_switch_file"`
	Metrics                  MetricsConfig    `yaml:"metrics"`
	DryRun                   bool             `yaml:"dry_run"`
}

// DetectorConfig describes how to determine if a reboot is required for a node.
type DetectorConfig struct {
	Name             string   `yaml:"name"`
	Type             string   `yaml:"type"`
	Path             string   `yaml:"path"`
	Cmd              []string `yaml:"cmd"`
	SuccessExitCodes []int    `yaml:"success_exit_codes"`
	TimeoutSec       int      `yaml:"timeout_sec"`
}

// EtcdTLSConfig configures optional TLS settings for connecting to etcd.
type EtcdTLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CAFile   string `yaml:"ca_file"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	Insecure bool   `yaml:"insecure_skip_verify"`
}

// ClusterPolicies capture the guard rails enforced before a reboot is allowed.
type ClusterPolicies struct {
	MinHealthyFraction       *float64 `yaml:"min_healthy_fraction"`
	MinHealthyAbsolute       *int     `yaml:"min_healthy_absolute"`
	ForbidIfOnlyFallbackLeft bool     `yaml:"forbid_if_only_fallback_left"`
	FallbackNodes            []string `yaml:"fallback_nodes"`
}

// WindowsConfig enumerates optional allow/deny reboot windows.
type WindowsConfig struct {
	Deny  []string `yaml:"deny"`
	Allow []string `yaml:"allow"`
}

// MetricsConfig defines observability exposure options.
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"`
}

// ValidationError aggregates multiple configuration validation failures.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid configuration: %s", strings.Join(e.Problems, "; "))
}

func (e *ValidationError) Is(target error) bool {
	var other *ValidationError
	return errors.As(target, &other)
}

// Load reads, parses, and validates a configuration from disk.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	return decode(f)
}

func decode(r io.Reader) (*Config, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate checks for semantic correctness in the configuration.
func (c *Config) Validate() error {
	problems := make([]string, 0)

	if strings.TrimSpace(c.NodeName) == "" {
		problems = append(problems, "node_name is required")
	}
	if len(c.RebootRequiredDetectors) == 0 {
		problems = append(problems, "at least one reboot_required_detector must be configured")
	}
	for i := range c.RebootRequiredDetectors {
		detProblems := c.RebootRequiredDetectors[i].validate()
		if len(detProblems) > 0 {
			for _, p := range detProblems {
				problems = append(problems, fmt.Sprintf("detector[%d]: %s", i, p))
			}
		}
	}

	if strings.TrimSpace(c.HealthScript) == "" {
		problems = append(problems, "health_script is required")
	}
	if c.HealthTimeoutSec <= 0 {
		problems = append(problems, "health_timeout_sec must be greater than zero")
	}
	if c.HealthPublishIntervalSec <= 0 {
		problems = append(problems, "health_publish_interval_sec must be greater than zero")
	}
	if c.CheckIntervalSec <= 0 {
		problems = append(problems, "check_interval_sec must be greater than zero")
	}
	if c.BackoffMinSec <= 0 {
		problems = append(problems, "backoff_min_sec must be greater than zero")
	}
	if c.BackoffMaxSec <= 0 {
		problems = append(problems, "backoff_max_sec must be greater than zero")
	}
	if c.BackoffMaxSec < c.BackoffMinSec {
		problems = append(problems, "backoff_max_sec must be greater than or equal to backoff_min_sec")
	}
	if c.MinRebootIntervalSec < 0 {
		problems = append(problems, "min_reboot_interval_sec must be non-negative")
	}
	if c.LockTTLSec <= 0 {
		problems = append(problems, "lock_ttl_sec must be greater than zero")
	}
	if c.LockTTLSec <= c.HealthTimeoutSec {
		problems = append(problems, "lock_ttl_sec must be greater than health_timeout_sec to avoid premature lease expiry")
	}
	if strings.TrimSpace(c.LockKey) == "" {
		problems = append(problems, "lock_key is required")
	}
	if len(c.EtcdEndpoints) == 0 {
		problems = append(problems, "etcd_endpoints must contain at least one endpoint")
	}
	if c.EtcdTLS != nil {
		if c.EtcdTLS.Enabled {
			if strings.TrimSpace(c.EtcdTLS.CAFile) == "" {
				problems = append(problems, "etcd_tls.ca_file is required when TLS is enabled")
			}
			if strings.TrimSpace(c.EtcdTLS.CertFile) == "" {
				problems = append(problems, "etcd_tls.cert_file is required when TLS is enabled")
			}
			if strings.TrimSpace(c.EtcdTLS.KeyFile) == "" {
				problems = append(problems, "etcd_tls.key_file is required when TLS is enabled")
			}
		}
	}
	if len(c.RebootCommand) == 0 {
		problems = append(problems, "reboot_command must specify the command to execute")
	}
	if err := c.ClusterPolicies.validate(); err != nil {
		problems = append(problems, err.Problems...)
	}
	if c.Metrics.Enabled && strings.TrimSpace(c.Metrics.Listen) == "" {
		problems = append(problems, "metrics.listen must be set when metrics.enabled is true")
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.HealthTimeoutSec == 0 {
		c.HealthTimeoutSec = 30
	}
	if c.HealthPublishIntervalSec == 0 {
		c.HealthPublishIntervalSec = 15
	}
	if c.CheckIntervalSec == 0 {
		c.CheckIntervalSec = 60
	}
	if c.BackoffMinSec == 0 {
		c.BackoffMinSec = 5
	}
	if c.BackoffMaxSec == 0 {
		c.BackoffMaxSec = 60
	}
	if strings.TrimSpace(c.LockKey) == "" {
		c.LockKey = "/cluster/clusterrebootd/lock"
	}
	if c.LockTTLSec == 0 {
		c.LockTTLSec = 90
	}
	if len(c.RebootCommand) == 0 {
		c.RebootCommand = []string{"/sbin/shutdown", "-r", "now", "coordinated kernel reboot"}
	}
	if c.KillSwitchFile == "" {
		c.KillSwitchFile = "/etc/clusterrebootd/disable"
	}
	if c.Metrics.Listen == "" {
		c.Metrics.Listen = "127.0.0.1:9090"
	}
	for i := range c.RebootRequiredDetectors {
		c.RebootRequiredDetectors[i].applyDefaults(i)
	}
}

// BaseEnvironment returns the static environment variables derived from the configuration.
// The resulting map can be extended with runtime annotations (metrics endpoints, skip flags)
// before injecting it into health scripts or reboot command expansion.
func (c *Config) BaseEnvironment() map[string]string {
	env := map[string]string{
		"RC_NODE_NAME": c.NodeName,
		"RC_DRY_RUN":   strconv.FormatBool(c.DryRun),
	}
	if strings.TrimSpace(c.LockKey) != "" {
		env["RC_LOCK_KEY"] = c.LockKey
	}
	if len(c.EtcdEndpoints) > 0 {
		env["RC_ETCD_ENDPOINTS"] = strings.Join(c.EtcdEndpoints, ",")
	}
	if strings.TrimSpace(c.KillSwitchFile) != "" {
		env["RC_KILL_SWITCH_FILE"] = c.KillSwitchFile
	}
	if c.ClusterPolicies.MinHealthyFraction != nil {
		env["RC_CLUSTER_MIN_HEALTHY_FRACTION"] = strconv.FormatFloat(*c.ClusterPolicies.MinHealthyFraction, 'f', -1, 64)
	}
	if c.ClusterPolicies.MinHealthyAbsolute != nil {
		env["RC_CLUSTER_MIN_HEALTHY_ABSOLUTE"] = strconv.Itoa(*c.ClusterPolicies.MinHealthyAbsolute)
	}
	env["RC_CLUSTER_FORBID_IF_ONLY_FALLBACK_LEFT"] = strconv.FormatBool(c.ClusterPolicies.ForbidIfOnlyFallbackLeft)
	if len(c.ClusterPolicies.FallbackNodes) > 0 {
		env["RC_CLUSTER_FALLBACK_NODES"] = strings.Join(c.ClusterPolicies.FallbackNodes, ",")
	}
	if len(c.Windows.Allow) > 0 {
		env["RC_WINDOWS_ALLOW"] = strings.Join(c.Windows.Allow, ",")
	}
	if len(c.Windows.Deny) > 0 {
		env["RC_WINDOWS_DENY"] = strings.Join(c.Windows.Deny, ",")
	}
	return env
}

func (d *DetectorConfig) applyDefaults(index int) {
	if strings.TrimSpace(d.Name) != "" {
		return
	}
	switch d.Type {
	case "file":
		if d.Path != "" {
			d.Name = fmt.Sprintf("file:%s", d.Path)
		} else {
			d.Name = fmt.Sprintf("file-detector-%d", index)
		}
	case "command":
		if len(d.Cmd) > 0 {
			d.Name = fmt.Sprintf("command:%s", d.Cmd[0])
		} else {
			d.Name = fmt.Sprintf("command-detector-%d", index)
		}
	default:
		d.Name = fmt.Sprintf("detector-%d", index)
	}
}

func (d DetectorConfig) validate() []string {
	problems := make([]string, 0)
	if strings.TrimSpace(d.Type) == "" {
		problems = append(problems, "type is required")
		return problems
	}
	switch d.Type {
	case "file":
		if strings.TrimSpace(d.Path) == "" {
			problems = append(problems, "path is required for file detectors")
		}
	case "command":
		if len(d.Cmd) == 0 {
			problems = append(problems, "cmd must contain at least one element for command detectors")
		}
	default:
		problems = append(problems, fmt.Sprintf("type %q is not supported", d.Type))
	}
	if d.TimeoutSec < 0 {
		problems = append(problems, "timeout_sec must be non-negative")
	}
	return problems
}

func (p *ClusterPolicies) validate() *ValidationError {
	problems := make([]string, 0)
	if p.MinHealthyFraction != nil {
		if *p.MinHealthyFraction <= 0 || *p.MinHealthyFraction > 1 {
			problems = append(problems, "cluster_policies.min_healthy_fraction must be within (0,1]")
		}
	}
	if p.MinHealthyAbsolute != nil {
		if *p.MinHealthyAbsolute < 0 {
			problems = append(problems, "cluster_policies.min_healthy_absolute must be non-negative")
		}
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// HealthTimeout returns the configured health timeout as a duration.
func (c *Config) HealthTimeout() time.Duration {
	return time.Duration(c.HealthTimeoutSec) * time.Second
}

// HealthPublishInterval returns how often the daemon refreshes the cluster health view.
func (c *Config) HealthPublishInterval() time.Duration {
	return time.Duration(c.HealthPublishIntervalSec) * time.Second
}

// LockTTL returns the etcd lock TTL as a duration.
func (c *Config) LockTTL() time.Duration {
	return time.Duration(c.LockTTLSec) * time.Second
}

// BackoffBounds returns the configured exponential backoff window as durations.
func (c *Config) BackoffBounds() (time.Duration, time.Duration) {
	return time.Duration(c.BackoffMinSec) * time.Second, time.Duration(c.BackoffMaxSec) * time.Second
}

// CheckInterval returns how long the orchestrator waits between evaluation passes.
func (c *Config) CheckInterval() time.Duration {
	return time.Duration(c.CheckIntervalSec) * time.Second
}

// RebootCooldownInterval returns the configured minimum spacing between successful reboots.
func (c *Config) RebootCooldownInterval() time.Duration {
	if c == nil {
		return 0
	}
	return time.Duration(c.MinRebootIntervalSec) * time.Second
}
