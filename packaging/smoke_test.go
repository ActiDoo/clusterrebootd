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

	cgroupMount := testutil.ContainerMount{Source: "/sys/fs/cgroup", Target: "/sys/fs/cgroup"}
	if _, err := os.Stat(cgroupMount.Source); err != nil {
		t.Skipf("skipping container smoke tests: cgroup filesystem not accessible: %v", err)
	}

	systemdExtraArgs := []string{"--tmpfs", "/run", "--tmpfs", "/run/lock"}
	switch runtime.Name() {
	case "docker", "podman":
		systemdExtraArgs = append([]string{"--cgroupns=host"}, systemdExtraArgs...)
	}

	type testCase struct {
		name       string
		image      string
		format     string
		mountPath  string
		script     string
		timeout    time.Duration
		privileged bool
		extraArgs  []string
		mounts     []testutil.ContainerMount
	}

	cases := []testCase{
		{
			name:      "debian-12",
			image:     "debian:12-slim",
			format:    "deb",
			mountPath: "/tmp/clusterrebootd.deb",
			script: `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates systemd dbus util-linux
dpkg -i /tmp/clusterrebootd.deb || apt-get install -fy
dpkg -s clusterrebootd >/dev/null
/usr/bin/clusterrebootd version
test -x /usr/bin/clusterrebootd
test -f /etc/clusterrebootd/config.yaml
test -f /lib/systemd/system/clusterrebootd.service
test -f /usr/lib/tmpfiles.d/clusterrebootd.conf
mkdir -p /etc/systemd/system/clusterrebootd.service.d
cat <<'EOF' >/etc/systemd/system/clusterrebootd.service.d/test-smoke.conf
[Service]
Type=oneshot
ExecStart=
ExecStart=/usr/bin/clusterrebootd version
EOF
mkdir -p /run/systemd/system
unshare --fork --pid --mount-proc bash -c '
  export container=docker
  exec /lib/systemd/systemd
' &
sd_pid=$!
for i in $(seq 1 50); do
  if [ -S /run/systemd/private ]; then
    break
  fi
  sleep 0.1
done
if [ ! -S /run/systemd/private ]; then
  echo "systemd did not start" >&2
  kill "$sd_pid"
  wait "$sd_pid" || true
  exit 1
fi
nsenter --target "$sd_pid" --mount --uts --ipc --net --pid bash -lc '
  set -euo pipefail
  systemctl daemon-reload
  systemctl start clusterrebootd.service
  test "$(systemctl show -p Result --value clusterrebootd.service)" = "success"
'
kill "$sd_pid"
wait "$sd_pid" || true
`,
			timeout:    4 * time.Minute,
			privileged: true,
			extraArgs:  append([]string(nil), systemdExtraArgs...),
			mounts:     []testutil.ContainerMount{cgroupMount},
		},
		{
			name:      "ubuntu-22.04",
			image:     "ubuntu:22.04",
			format:    "deb",
			mountPath: "/tmp/clusterrebootd.deb",
			script: `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates systemd dbus util-linux
dpkg -i /tmp/clusterrebootd.deb || apt-get install -fy
dpkg -s clusterrebootd >/dev/null
/usr/bin/clusterrebootd version
test -x /usr/bin/clusterrebootd
test -f /etc/clusterrebootd/config.yaml
test -f /lib/systemd/system/clusterrebootd.service
test -f /usr/lib/tmpfiles.d/clusterrebootd.conf
mkdir -p /etc/systemd/system/clusterrebootd.service.d
cat <<'EOF' >/etc/systemd/system/clusterrebootd.service.d/test-smoke.conf
[Service]
Type=oneshot
ExecStart=
ExecStart=/usr/bin/clusterrebootd version
EOF
mkdir -p /run/systemd/system
unshare --fork --pid --mount-proc bash -c '
  export container=docker
  exec /lib/systemd/systemd
' &
sd_pid=$!
for i in $(seq 1 50); do
  if [ -S /run/systemd/private ]; then
    break
  fi
  sleep 0.1
done
if [ ! -S /run/systemd/private ]; then
  echo "systemd did not start" >&2
  kill "$sd_pid"
  wait "$sd_pid" || true
  exit 1
fi
nsenter --target "$sd_pid" --mount --uts --ipc --net --pid bash -lc '
  set -euo pipefail
  systemctl daemon-reload
  systemctl start clusterrebootd.service
  test "$(systemctl show -p Result --value clusterrebootd.service)" = "success"
'
kill "$sd_pid"
wait "$sd_pid" || true
`,
			timeout:    4 * time.Minute,
			privileged: true,
			extraArgs:  append([]string(nil), systemdExtraArgs...),
			mounts:     []testutil.ContainerMount{cgroupMount},
		},
		{
			name:      "rockylinux-9",
			image:     "rockylinux:9",
			format:    "rpm",
			mountPath: "/tmp/clusterrebootd.rpm",
			script: `set -euo pipefail
dnf install -y systemd util-linux dbus
dnf install -y --setopt=install_weak_deps=False --nogpgcheck /tmp/clusterrebootd.rpm
rpm -q clusterrebootd
/usr/bin/clusterrebootd version
test -x /usr/bin/clusterrebootd
test -f /etc/clusterrebootd/config.yaml
test -f /lib/systemd/system/clusterrebootd.service
test -f /usr/lib/tmpfiles.d/clusterrebootd.conf
mkdir -p /etc/systemd/system/clusterrebootd.service.d
cat <<'EOF' >/etc/systemd/system/clusterrebootd.service.d/test-smoke.conf
[Service]
Type=oneshot
ExecStart=
ExecStart=/usr/bin/clusterrebootd version
EOF
mkdir -p /run/systemd/system
unshare --fork --pid --mount-proc bash -c '
  export container=docker
  exec /usr/lib/systemd/systemd
' &
sd_pid=$!
for i in $(seq 1 50); do
  if [ -S /run/systemd/private ]; then
    break
  fi
  sleep 0.1
done
if [ ! -S /run/systemd/private ]; then
  echo "systemd did not start" >&2
  kill "$sd_pid"
  wait "$sd_pid" || true
  exit 1
fi
nsenter --target "$sd_pid" --mount --uts --ipc --net --pid bash -lc '
  set -euo pipefail
  systemctl daemon-reload
  systemctl start clusterrebootd.service
  test "$(systemctl show -p Result --value clusterrebootd.service)" = "success"
'
kill "$sd_pid"
wait "$sd_pid" || true
`,
			timeout:    5 * time.Minute,
			privileged: true,
			extraArgs:  append([]string(nil), systemdExtraArgs...),
			mounts:     []testutil.ContainerMount{cgroupMount},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()

			mount := testutil.ContainerMount{Source: packages[tc.format], Target: tc.mountPath, ReadOnly: true}
			mounts := append([]testutil.ContainerMount{mount}, tc.mounts...)
			output, err := runtime.Run(ctx, testutil.ContainerRunOptions{
				Image:      tc.image,
				Cmd:        []string{"bash", "-lc", tc.script},
				Mounts:     mounts,
				Privileged: tc.privileged,
				ExtraArgs:  tc.extraArgs,
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

	root := repoRoot(t)
	distDir := filepath.Join(root, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatalf("failed to create dist directory: %v", err)
	}

	binaryPath := filepath.Join(distDir, "clusterrebootd")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", binaryPath, "./cmd/clusterrebootd")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build clusterrebootd binary: %v\n%s", err, output)
	}

	t.Cleanup(func() {
		_ = os.RemoveAll(distDir)
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

	root := repoRoot(t)
	cfgPath := filepath.Join(root, "packaging", "nfpm.yaml")

	cfg, err := nfpm.ParseFileWithEnvMapping(cfgPath, mapping)
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

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to determine working directory: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("failed to change to repository root: %v", err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

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

func repoRoot(t testing.TB) string {
	t.Helper()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to determine working directory: %v", err)
	}
	root := filepath.Clean(filepath.Join(cwd, ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("failed to locate repository root from %s: %v", cwd, err)
	}
	return root
}
