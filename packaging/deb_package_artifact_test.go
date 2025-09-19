package packaging_test

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/blakesmith/ar"
	"github.com/ulikunitz/xz"
)

func TestDebPackageShipsCoreAssets(t *testing.T) {
	debPath := buildDebPackage(t)
	files := listDebDataFiles(t, debPath)

	required := []string{
		"/usr/bin/clusterrebootd",
		"/etc/clusterrebootd/config.yaml",
		"/lib/systemd/system/clusterrebootd.service",
		"/usr/lib/tmpfiles.d/clusterrebootd.conf",
	}

	for _, want := range required {
		if !files[want] {
			t.Fatalf("expected deb package to contain %s, got contents: %v", want, sortedKeys(files))
		}
	}
}

func buildDebPackage(t testing.TB) string {
	t.Helper()

	packages := buildSmokeTestPackages(t)
	deb, ok := packages["deb"]
	if !ok {
		t.Fatalf("failed to build deb package")
	}
	return deb
}

func listDebDataFiles(t testing.TB, debPath string) map[string]bool {
	t.Helper()

	file, err := os.Open(debPath)
	if err != nil {
		t.Fatalf("failed to open deb package %s: %v", debPath, err)
	}
	defer file.Close()

	reader := ar.NewReader(file)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to read deb archive header: %v", err)
		}

		if strings.HasPrefix(header.Name, "data.tar") {
			tarReader := newTarReader(t, header.Name, reader)
			return collectTarPaths(t, tarReader)
		}
	}

	t.Fatalf("data archive not found inside %s", debPath)
	return nil
}

func newTarReader(t testing.TB, name string, r io.Reader) *tar.Reader {
	t.Helper()

	switch {
	case strings.HasSuffix(name, ".gz"):
		gz, err := gzip.NewReader(r)
		if err != nil {
			t.Fatalf("failed to create gzip reader for %s: %v", name, err)
		}
		t.Cleanup(func() {
			_ = gz.Close()
		})
		return tar.NewReader(gz)
	case strings.HasSuffix(name, ".xz"):
		xzr, err := xz.NewReader(r)
		if err != nil {
			t.Fatalf("failed to create xz reader for %s: %v", name, err)
		}
		return tar.NewReader(xzr)
	case strings.HasSuffix(name, ".tar"):
		return tar.NewReader(r)
	default:
		t.Fatalf("unsupported data archive compression for %s", name)
		return nil
	}
}

func collectTarPaths(t testing.TB, tr *tar.Reader) map[string]bool {
	t.Helper()

	files := make(map[string]bool)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to read tar entry: %v", err)
		}

		normalized := normalizeTarPath(header.Name)
		if normalized != "" {
			files[normalized] = true
		}
	}

	return files
}

func normalizeTarPath(name string) string {
	cleaned := strings.TrimPrefix(name, "./")
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" {
		return ""
	}
	return "/" + cleaned
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
