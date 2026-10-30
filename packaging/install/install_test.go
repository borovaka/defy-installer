// SPDX-License-Identifier: MIT

// Package install runs the install scripts as the README does, piped from
// a fake release served over HTTP: install.sh through sh on macOS and
// Linux, install.ps1 through Windows PowerShell on Windows. The release's
// program is this test binary, which answers --version when stubEnv is
// set, so the tests need no Go toolchain where they run.
package install

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const stubEnv = "DEFY_INSTALL_TEST_STUB"

func TestMain(m *testing.M) {
	if os.Getenv(stubEnv) != "" {
		fmt.Println("defy-installer version v0.0.0-test")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// release writes a release for this computer into a folder served over
// HTTP and returns its URL: the install script, the archive and SHA256SUMS,
// which lists the archive's SHA-256, or a wrong one if spoil is set.
func release(t *testing.T, spoil bool) string {
	t.Helper()
	archive := map[string]string{
		"darwin":  "defy-installer-macos.zip",
		"linux":   "defy-installer-linux-" + map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH] + ".tar.gz",
		"windows": "defy-installer-windows.zip",
	}[runtime.GOOS]
	script, program := "install.sh", "defy-installer"
	if runtime.GOOS == "windows" {
		script, program = "install.ps1", "defy-installer.exe"
		if runtime.GOARCH == "arm64" {
			archive = "defy-installer-windows-arm64.zip"
		}
	}
	exe, err := os.Executable()
	must(t, err)
	stub, err := os.ReadFile(exe)
	must(t, err)
	data := pack(t, strings.HasSuffix(archive, ".zip"), map[string][]byte{
		program:               stub,
		"defy-installer.yaml": []byte("release settings\n"),
		"README.txt":          []byte("release readme\n"),
	})
	sum := sha256.Sum256(data)
	if spoil {
		sum[0] ^= 1
	}
	dir := t.TempDir()
	text, err := os.ReadFile(filepath.Join("..", script))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, script), text, 0o644))
	must(t, os.WriteFile(filepath.Join(dir, archive), data, 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "SHA256SUMS"), fmt.Appendf(nil, "%x  %s\n", sum, archive), 0o644))
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(server.Close)
	return server.URL
}

// pack returns a zip or a gzipped tar of a defy-installer folder holding
// files, the program executable, as packaging/archive packs a release.
func pack(t *testing.T, isZip bool, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if isZip {
		w := zip.NewWriter(&buf)
		for name, data := range files {
			h := &zip.FileHeader{Name: "defy-installer/" + name, Method: zip.Deflate}
			h.SetMode(0o755)
			f, err := w.CreateHeader(h)
			must(t, err)
			_, err = f.Write(data)
			must(t, err)
		}
		must(t, w.Close())
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	must(t, w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "defy-installer/", Mode: 0o755}))
	for name, data := range files {
		must(t, w.WriteHeader(&tar.Header{Name: "defy-installer/" + name, Mode: 0o755, Size: int64(len(data))}))
		_, err := w.Write(data)
		must(t, err)
	}
	must(t, w.Close())
	must(t, gz.Close())
	return buf.Bytes()
}

// install pipes the release's script into the shell, as the README says,
// with dest as the folder to install into.
func install(t *testing.T, base, dest string) (string, error) {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "irm "+base+"/install.ps1 | iex")
	} else {
		if _, err := exec.LookPath("curl"); err != nil {
			t.Skip("no curl")
		}
		cmd = exec.Command("sh", "-c", `curl -fsSL "$DEFY_INSTALLER_BASE/install.sh" | sh`)
	}
	cmd.Env = append(os.Environ(), stubEnv+"=1", "DEFY_INSTALLER_BASE="+base, "DEFY_INSTALLER_DIR="+dest)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstall(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "defy-installer")
	out, err := install(t, release(t, false), dest)
	if err != nil || !strings.Contains(out, "v0.0.0-test") {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	checkFile(t, filepath.Join(dest, "defy-installer.yaml"), "release settings\n")
	checkFile(t, filepath.Join(dest, "README.txt"), "release readme\n")
}

func TestUpdateKeepsSettings(t *testing.T) {
	dest := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dest, "defy-installer.yaml"), []byte("my settings\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(dest, "README.txt"), []byte("old readme\n"), 0o644))
	if out, err := install(t, release(t, false), dest); err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	checkFile(t, filepath.Join(dest, "defy-installer.yaml"), "my settings\n")
	checkFile(t, filepath.Join(dest, "README.txt"), "release readme\n")
}

func TestRefusesWrongChecksum(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "defy-installer")
	out, err := install(t, release(t, true), dest)
	if err == nil || !strings.Contains(out, "SHA256SUMS") {
		t.Fatalf("installed despite a wrong checksum: %v\n%s", err, out)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("%s exists after a refused install", dest)
	}
}

func checkFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Errorf("%s holds %q (%v), want %q", path, got, err, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
