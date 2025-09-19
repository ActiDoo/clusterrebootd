package packaging_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/goreleaser/nfpm/v2"
	_ "github.com/goreleaser/nfpm/v2/deb"
	_ "github.com/goreleaser/nfpm/v2/rpm"

	"github.com/clusterrebootd/clusterrebootd/internal/testutil"
)

func TestPackagesInstallInContainers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container smoke tests in short mode")
	}

	runtime, err := testutil.FindContainerRuntime()
	if err != nil {
		t.Skipf("skipping container smoke tests: %v", err)
	}

	packages := buildSmokeTestPackages(t)

	type testCase struct {
		name      string
		image     string
		format    string
		mountPath string
		script    string
		timeout   time.Duration
	}

	cases := []testCase{
		{
			name:      "debian-12",
			image:     "debian:12-slim",
			format:    "deb",
			mountPath: "/tmp/reboot-coordinator.deb",
			script: `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates
dpkg -i /tmp/reboot-coordinator.deb || apt-get install -fy
dpkg -s reboot-coordinator >/dev/null
/usr/bin/reboot-coordinator version
test -x /usr/bin/reboot-coordinator
test -f /etc/reboot-coordinator/config.yaml
test -f /lib/systemd/system/reboot-coordinator.service
test -f /usr/lib/tmpfiles.d/reboot-coordinator.conf
`,
			timeout: 4 * time.Minute,
		},
		{
			name:      "ubuntu-22.04",
			image:     "ubuntu:22.04",
			format:    "deb",
			mountPath: "/tmp/reboot-coordinator.deb",
			script: `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates
dpkg -i /tmp/reboot-coordinator.deb || apt-get install -fy
dpkg -s reboot-coordinator >/dev/null
/usr/bin/reboot-coordinator version
test -x /usr/bin/reboot-coordinator
test -f /etc/reboot-coordinator/config.yaml
test -f /lib/systemd/system/reboot-coordinator.service
test -f /usr/lib/tmpfiles.d/reboot-coordinator.conf
`,
			timeout: 4 * time.Minute,
		},
		{
			name:      "rockylinux-9",
			image:     "rockylinux:9",
			format:    "rpm",
			mountPath: "/tmp/reboot-coordinator.rpm",
			script: `set -euo pipefail
dnf install -y --setopt=install_weak_deps=False --nogpgcheck /tmp/reboot-coordinator.rpm
rpm -q reboot-coordinator
/usr/bin/reboot-coordinator version
test -x /usr/bin/reboot-coordinator
test -f /etc/reboot-coordinator/config.yaml
test -f /lib/systemd/system/reboot-coordinator.service
test -f /usr/lib/tmpfiles.d/reboot-coordinator.conf
`,
			timeout: 5 * time.Minute,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()

			mount := testutil.ContainerMount{Source: packages[tc.format], Target: tc.mountPath, ReadOnly: true}
			output, err := runtime.Run(ctx, testutil.ContainerRunOptions{
				Image:  tc.image,
				Cmd:    []string{"bash", "-lc", tc.script},
				Mounts: []testutil.ContainerMount{mount},
			})
			if err != nil {
				t.Fatalf("container smoke test for %s failed: %v\n%s", tc.name, err, output)
			}
		})
	}
}

func buildSmokeTestPackages(t testing.TB) map[string]string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := os.MkdirAll("dist", 0o755); err != nil {
		t.Fatalf("failed to create dist directory: %v", err)
	}

	binaryPath := filepath.Join("dist", "reboot-coordinator")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", binaryPath, "./cmd/reboot-coordinator")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build reboot-coordinator binary: %v\n%s", err, output)
	}

	t.Cleanup(func() {
		_ = os.RemoveAll("dist")
	})

	version := "0.0.0-smoke"
	pkgDir := t.TempDir()

	results := map[string]string{
		"deb": buildPackage(t, pkgDir, "deb", "amd64", version),
		"rpm": buildPackage(t, pkgDir, "rpm", "x86_64", version),
	}

	return results
}

func buildPackage(t testing.TB, outputDir, format, arch, version string) string {
	t.Helper()

	mapping := func(key string) string {
		switch key {
		case "ARCH":
			return arch
		case "VERSION":
			return version
		default:
			return os.Getenv(key)
		}
	}

	cfg, err := nfpm.ParseFileWithEnvMapping("packaging/nfpm.yaml", mapping)
	if err != nil {
		t.Fatalf("failed to parse nfpm configuration: %v", err)
	}

	info, err := cfg.Get(format)
	if err != nil {
		t.Fatalf("failed to resolve nfpm configuration for %s: %v", format, err)
	}

	info = nfpm.WithDefaults(info)

	packager, err := nfpm.Get(format)
	if err != nil {
		t.Fatalf("failed to resolve nfpm packager for %s: %v", format, err)
	}

	fileName := packager.ConventionalFileName(info)
	target := filepath.Join(outputDir, fileName)
	info.Target = target

	file, err := os.Create(target)
	if err != nil {
		t.Fatalf("failed to create package %s: %v", target, err)
	}
	defer file.Close()

	if err := packager.Package(info, file); err != nil {
		t.Fatalf("failed to build %s package: %v", format, err)
	}

	if err := file.Close(); err != nil {
		t.Fatalf("failed to close package file %s: %v", target, err)
	}

	abs, err := filepath.Abs(target)
	if err != nil {
		t.Fatalf("failed to resolve absolute path for %s: %v", target, err)
	}

	return abs
}
