package packaging_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type packagingConfig struct {
	DryRun                  bool     `yaml:"dry_run"`
	NodeName                string   `yaml:"node_name"`
	HealthScript            string   `yaml:"health_script"`
	HealthTimeoutSec        int      `yaml:"health_timeout_sec"`
	CheckIntervalSec        int      `yaml:"check_interval_sec"`
	BackoffMinSec           int      `yaml:"backoff_min_sec"`
	BackoffMaxSec           int      `yaml:"backoff_max_sec"`
	LockTTLSec              int      `yaml:"lock_ttl_sec"`
	LockKey                 string   `yaml:"lock_key"`
	EtcdEndpoints           []string `yaml:"etcd_endpoints"`
	EtcdNamespace           string   `yaml:"etcd_namespace"`
	RebootRequiredDetectors []struct {
		Name string `yaml:"name"`
		Type string `yaml:"type"`
	} `yaml:"reboot_required_detectors"`
	KillSwitchFile  string   `yaml:"kill_switch_file"`
	RebootCommand   []string `yaml:"reboot_command"`
	ClusterPolicies struct {
		MinHealthyFraction       *float64 `yaml:"min_healthy_fraction"`
		MinHealthyAbsolute       *int     `yaml:"min_healthy_absolute"`
		ForbidIfOnlyFallbackLeft bool     `yaml:"forbid_if_only_fallback_left"`
		FallbackNodes            []string `yaml:"fallback_nodes"`
	} `yaml:"cluster_policies"`
	Windows struct {
		Deny  []string `yaml:"deny"`
		Allow []string `yaml:"allow"`
	} `yaml:"windows"`
	Metrics struct {
		Enabled bool   `yaml:"enabled"`
		Listen  string `yaml:"listen"`
	} `yaml:"metrics"`
	EtcdTLS struct {
		Enabled            bool   `yaml:"enabled"`
		CAFile             string `yaml:"ca_file"`
		CertFile           string `yaml:"cert_file"`
		KeyFile            string `yaml:"key_file"`
		InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
	} `yaml:"etcd_tls"`
}

type nfpmFileInfo struct {
	Mode string `yaml:"mode"`
}

type nfpmContent struct {
	Src      string       `yaml:"src"`
	Dst      string       `yaml:"dst"`
	Type     string       `yaml:"type"`
	FileInfo nfpmFileInfo `yaml:"file_info"`
}

type nfpmConfig struct {
	Name        string        `yaml:"name"`
	Arch        string        `yaml:"arch"`
	Platform    string        `yaml:"platform"`
	Version     string        `yaml:"version"`
	Section     string        `yaml:"section"`
	Priority    string        `yaml:"priority"`
	Description string        `yaml:"description"`
	License     string        `yaml:"license"`
	Homepage    string        `yaml:"homepage"`
	Maintainer  string        `yaml:"maintainer"`
	Contents    []nfpmContent `yaml:"contents"`
	Overrides   struct {
		Deb struct {
			Depends    []string      `yaml:"depends"`
			Recommends []string      `yaml:"recommends"`
			Contents   []nfpmContent `yaml:"contents"`
			Scripts    struct {
				Preinstall  string `yaml:"preinstall"`
				Postinstall string `yaml:"postinstall"`
				Prerm       string `yaml:"prerm"`
				Postrm      string `yaml:"postrm"`
			} `yaml:"scripts"`
		} `yaml:"deb"`
		Rpm struct {
			Depends []string `yaml:"depends"`
			Scripts struct {
				Preinstall  string `yaml:"preinstall"`
				Postinstall string `yaml:"postinstall"`
				Preremove   string `yaml:"preremove"`
				Postremove  string `yaml:"postremove"`
			} `yaml:"scripts"`
		} `yaml:"rpm"`
	} `yaml:"overrides"`
}

func readPackagingFile(t testing.TB, rel string) []byte {
	t.Helper()
	path := filepath.Clean(rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", rel, err)
	}
	return data
}

func decodeYAMLStrict(t testing.TB, data []byte, out any) {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		t.Fatalf("failed to decode yaml: %v", err)
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != nil && err != io.EOF {
		t.Fatalf("unexpected additional YAML document: %v", err)
	}
}

func TestConfigTemplateHasSafeDefaults(t *testing.T) {
	data := readPackagingFile(t, "config.yaml")

	var cfg packagingConfig
	decodeYAMLStrict(t, data, &cfg)

	if !cfg.DryRun {
		t.Fatalf("expected dry_run to default to true")
	}
	if cfg.NodeName != "" {
		t.Fatalf("expected node_name to be empty, got %q", cfg.NodeName)
	}
	if cfg.HealthScript != "" {
		t.Fatalf("expected health_script to be empty, got %q", cfg.HealthScript)
	}
	if len(cfg.RebootRequiredDetectors) != 0 {
		t.Fatalf("expected reboot_required_detectors to be empty, got %d entries", len(cfg.RebootRequiredDetectors))
	}
	if cfg.HealthTimeoutSec <= 0 {
		t.Fatalf("expected positive health_timeout_sec, got %d", cfg.HealthTimeoutSec)
	}
	if cfg.CheckIntervalSec <= 0 {
		t.Fatalf("expected positive check_interval_sec, got %d", cfg.CheckIntervalSec)
	}
	if cfg.BackoffMinSec <= 0 || cfg.BackoffMaxSec <= 0 {
		t.Fatalf("expected positive backoff bounds, got min=%d max=%d", cfg.BackoffMinSec, cfg.BackoffMaxSec)
	}
	if cfg.BackoffMaxSec < cfg.BackoffMinSec {
		t.Fatalf("expected backoff_max_sec to be >= backoff_min_sec, got min=%d max=%d", cfg.BackoffMinSec, cfg.BackoffMaxSec)
	}
	if cfg.LockTTLSec <= cfg.HealthTimeoutSec {
		t.Fatalf("expected lock_ttl_sec to exceed health_timeout_sec, got lock=%d health=%d", cfg.LockTTLSec, cfg.HealthTimeoutSec)
	}
	if cfg.LockKey != "" {
		t.Fatalf("expected lock_key to default to empty string for operator override, got %q", cfg.LockKey)
	}
	if len(cfg.EtcdEndpoints) != 0 {
		t.Fatalf("expected etcd_endpoints to be empty, got %v", cfg.EtcdEndpoints)
	}
	if cfg.KillSwitchFile != "/etc/reboot-coordinator/disable" {
		t.Fatalf("unexpected kill_switch_file: %q", cfg.KillSwitchFile)
	}
	if cfg.Metrics.Enabled {
		t.Fatalf("expected metrics.enabled to default to false")
	}
	if cfg.Metrics.Listen != "127.0.0.1:9090" {
		t.Fatalf("unexpected metrics.listen default: %q", cfg.Metrics.Listen)
	}
	if len(cfg.RebootCommand) != 4 {
		t.Fatalf("expected reboot_command to contain 4 elements, got %v", cfg.RebootCommand)
	}
	expectedCommand := []string{"/sbin/shutdown", "-r", "now", "coordinated reboot for ${RC_NODE_NAME}"}
	for i, value := range expectedCommand {
		if cfg.RebootCommand[i] != value {
			t.Fatalf("unexpected reboot_command[%d]: got %q want %q", i, cfg.RebootCommand[i], value)
		}
	}
	if cfg.ClusterPolicies.MinHealthyFraction != nil {
		t.Fatalf("expected cluster_policies.min_healthy_fraction to be null")
	}
	if cfg.ClusterPolicies.MinHealthyAbsolute != nil {
		t.Fatalf("expected cluster_policies.min_healthy_absolute to be null")
	}
	if cfg.ClusterPolicies.ForbidIfOnlyFallbackLeft {
		t.Fatalf("expected forbid_if_only_fallback_left to default to false")
	}
	if len(cfg.ClusterPolicies.FallbackNodes) != 0 {
		t.Fatalf("expected fallback_nodes to be empty, got %v", cfg.ClusterPolicies.FallbackNodes)
	}
	if len(cfg.Windows.Deny) != 0 || len(cfg.Windows.Allow) != 0 {
		t.Fatalf("expected reboot windows to be empty, got deny=%v allow=%v", cfg.Windows.Deny, cfg.Windows.Allow)
	}
	if cfg.EtcdTLS.Enabled {
		t.Fatalf("expected etcd_tls.enabled to default to false")
	}
	if cfg.EtcdTLS.CAFile != "" || cfg.EtcdTLS.CertFile != "" || cfg.EtcdTLS.KeyFile != "" || cfg.EtcdTLS.InsecureSkipVerify {
		t.Fatalf("expected etcd_tls credentials to be empty by default")
	}
}

func TestSystemdUnitMatchesBlueprint(t *testing.T) {
	data := readPackagingFile(t, filepath.Join("systemd", "reboot-coordinator.service"))
	content := string(data)

	expectedSnippets := []string{
		"Description=Cluster Reboot Coordinator",
		"Documentation=https://github.com/clusterrebootd/clusterrebootd",
		"After=network-online.target",
		"Wants=network-online.target",
		"StartLimitIntervalSec=60",
		"StartLimitBurst=5",
		"ConditionPathExists=!/etc/reboot-coordinator/disable",
		"ExecStart=/usr/bin/reboot-coordinator run --config /etc/reboot-coordinator/config.yaml",
		"Restart=always",
		"RestartSec=5",
		"RuntimeDirectory=reboot-coordinator",
		"RuntimeDirectoryMode=0750",
		"WantedBy=multi-user.target",
	}

	for _, snippet := range expectedSnippets {
		if !strings.Contains(content, snippet) {
			t.Fatalf("expected systemd unit to contain %q", snippet)
		}
	}
}

func TestTmpfilesConfigurationReservesRuntimeDirectory(t *testing.T) {
	data := readPackagingFile(t, filepath.Join("tmpfiles", "reboot-coordinator.conf"))
	content := string(data)
	if !strings.Contains(content, "d /run/reboot-coordinator 0750 root root -") {
		t.Fatalf("expected tmpfiles configuration to create /run/reboot-coordinator, got: %s", content)
	}
}

func TestMaintainerScriptsAreDefensive(t *testing.T) {
	scripts := []string{
		filepath.Join("scripts", "deb", "preinst"),
		filepath.Join("scripts", "deb", "postinst"),
		filepath.Join("scripts", "deb", "prerm"),
		filepath.Join("scripts", "deb", "postrm"),
		filepath.Join("scripts", "rpm", "preinstall.sh"),
		filepath.Join("scripts", "rpm", "postinstall.sh"),
		filepath.Join("scripts", "rpm", "preremove.sh"),
		filepath.Join("scripts", "rpm", "postremove.sh"),
	}

	systemdGuarded := map[string]bool{
		filepath.Join("scripts", "deb", "postinst"):       true,
		filepath.Join("scripts", "deb", "prerm"):          true,
		filepath.Join("scripts", "deb", "postrm"):         true,
		filepath.Join("scripts", "rpm", "postinstall.sh"): true,
		filepath.Join("scripts", "rpm", "preremove.sh"):   true,
		filepath.Join("scripts", "rpm", "postremove.sh"):  true,
	}

	for _, script := range scripts {
		data := string(readPackagingFile(t, script))
		if !strings.Contains(data, "set -eu") {
			t.Fatalf("expected %s to enable strict shell flags", script)
		}
		if systemdGuarded[script] && !strings.Contains(data, "systemd_active") {
			t.Fatalf("expected %s to guard systemctl invocations with systemd_active()", script)
		}
	}

	postinst := string(readPackagingFile(t, filepath.Join("scripts", "deb", "postinst")))
	if !strings.Contains(postinst, "systemd-tmpfiles --create") {
		t.Fatalf("expected deb postinst to apply tmpfiles configuration")
	}
	if !strings.Contains(postinst, "reboot-coordinator validate-config") {
		t.Fatalf("expected deb postinst to instruct operators to validate the configuration")
	}

	rpmPostinstall := string(readPackagingFile(t, filepath.Join("scripts", "rpm", "postinstall.sh")))
	if !strings.Contains(rpmPostinstall, "systemd-tmpfiles --create") {
		t.Fatalf("expected rpm postinstall to apply tmpfiles configuration")
	}
	if !strings.Contains(rpmPostinstall, "reboot-coordinator validate-config") {
		t.Fatalf("expected rpm postinstall to instruct operators to validate the configuration")
	}
}

func TestNFPMConfigurationMatchesBlueprint(t *testing.T) {
	data := readPackagingFile(t, "nfpm.yaml")

	var cfg nfpmConfig
	decodeYAMLStrict(t, data, &cfg)

	if cfg.Name != "reboot-coordinator" {
		t.Fatalf("unexpected package name %q", cfg.Name)
	}
	if cfg.Arch != "${ARCH}" {
		t.Fatalf("expected arch placeholder to be ${ARCH}, got %q", cfg.Arch)
	}
	if cfg.Platform != "linux" {
		t.Fatalf("unexpected platform %q", cfg.Platform)
	}
	if !strings.Contains(cfg.Description, "Cluster Reboot Coordinator") {
		t.Fatalf("expected package description to mention the Cluster Reboot Coordinator")
	}

	contentByDest := make(map[string]nfpmContent, len(cfg.Contents))
	for _, entry := range cfg.Contents {
		contentByDest[entry.Dst] = entry
	}

	binary := contentByDest["/usr/bin/reboot-coordinator"]
	if binary.Src != "./dist/reboot-coordinator" {
		t.Fatalf("unexpected binary source %q", binary.Src)
	}
	if binary.FileInfo.Mode != "0755" {
		t.Fatalf("expected binary mode 0755, got %q", binary.FileInfo.Mode)
	}

	configEntry := contentByDest["/etc/reboot-coordinator/config.yaml"]
	if configEntry.Src != "./packaging/config.yaml" {
		t.Fatalf("unexpected config source %q", configEntry.Src)
	}
	if configEntry.Type != "config" {
		t.Fatalf("expected config.yaml to be marked as a config file, got type %q", configEntry.Type)
	}
	if configEntry.FileInfo.Mode != "0640" {
		t.Fatalf("expected config file mode 0640, got %q", configEntry.FileInfo.Mode)
	}

	if _, ok := contentByDest["/lib/systemd/system/reboot-coordinator.service"]; !ok {
		t.Fatalf("expected systemd unit to be packaged")
	}
	if entry := contentByDest["/usr/lib/tmpfiles.d/reboot-coordinator.conf"]; entry.Src != "./packaging/tmpfiles/reboot-coordinator.conf" {
		t.Fatalf("unexpected tmpfiles source %q", entry.Src)
	}
	if entry := contentByDest["/usr/share/licenses/reboot-coordinator/LICENSE"]; entry.Src != "./LICENSE" {
		t.Fatalf("expected license to be copied from repository root, got %q", entry.Src)
	}
	if entry := contentByDest["/usr/share/doc/reboot-coordinator/PACKAGING_BLUEPRINT.md"]; entry.Src != "./docs/PACKAGING_BLUEPRINT.md" {
		t.Fatalf("expected packaging blueprint to be shipped as documentation, got %q", entry.Src)
	}

	debContent := make(map[string]nfpmContent, len(cfg.Overrides.Deb.Contents))
	for _, entry := range cfg.Overrides.Deb.Contents {
		debContent[entry.Dst] = entry
	}
	if entry, ok := debContent["/usr/share/doc/reboot-coordinator/README.Debian"]; !ok || entry.Src != "./packaging/docs/README.Debian" {
		t.Fatalf("expected Debian README to be packaged, got %+v", entry)
	}

	if !contains(cfg.Overrides.Deb.Depends, "systemd") {
		t.Fatalf("expected Debian package to depend on systemd")
	}
	if !contains(cfg.Overrides.Deb.Recommends, "ca-certificates") {
		t.Fatalf("expected Debian package to recommend ca-certificates")
	}
	if cfg.Overrides.Deb.Scripts.Preinstall != "./packaging/scripts/deb/preinst" {
		t.Fatalf("unexpected Debian preinst script %q", cfg.Overrides.Deb.Scripts.Preinstall)
	}
	if cfg.Overrides.Deb.Scripts.Postinstall != "./packaging/scripts/deb/postinst" {
		t.Fatalf("unexpected Debian postinst script %q", cfg.Overrides.Deb.Scripts.Postinstall)
	}
	if cfg.Overrides.Deb.Scripts.Prerm != "./packaging/scripts/deb/prerm" {
		t.Fatalf("unexpected Debian prerm script %q", cfg.Overrides.Deb.Scripts.Prerm)
	}
	if cfg.Overrides.Deb.Scripts.Postrm != "./packaging/scripts/deb/postrm" {
		t.Fatalf("unexpected Debian postrm script %q", cfg.Overrides.Deb.Scripts.Postrm)
	}

	if !contains(cfg.Overrides.Rpm.Depends, "systemd") {
		t.Fatalf("expected RPM package to depend on systemd")
	}
	if cfg.Overrides.Rpm.Scripts.Preinstall != "./packaging/scripts/rpm/preinstall.sh" {
		t.Fatalf("unexpected RPM preinstall script %q", cfg.Overrides.Rpm.Scripts.Preinstall)
	}
	if cfg.Overrides.Rpm.Scripts.Postinstall != "./packaging/scripts/rpm/postinstall.sh" {
		t.Fatalf("unexpected RPM postinstall script %q", cfg.Overrides.Rpm.Scripts.Postinstall)
	}
	if cfg.Overrides.Rpm.Scripts.Preremove != "./packaging/scripts/rpm/preremove.sh" {
		t.Fatalf("unexpected RPM preremove script %q", cfg.Overrides.Rpm.Scripts.Preremove)
	}
	if cfg.Overrides.Rpm.Scripts.Postremove != "./packaging/scripts/rpm/postremove.sh" {
		t.Fatalf("unexpected RPM postremove script %q", cfg.Overrides.Rpm.Scripts.Postremove)
	}
}

func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
