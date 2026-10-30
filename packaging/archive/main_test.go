// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var modified = time.Date(2026, 10, 24, 9, 5, 31, 0, time.UTC)

// pack writes a binary and two files, and archives them into out in dir.
func pack(t *testing.T, dir, out string) (string, error) {
	t.Helper()
	files := map[string]string{"defy-installer-linux-amd64": "binary", "defy-installer.yaml": "keymap: ''\n", "README.txt": "Read me.\n"}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, out)
	return path, run(path, modified, filepath.Join(dir, "defy-installer-linux-amd64"), "defy-installer",
		[]string{filepath.Join(dir, "defy-installer.yaml"), filepath.Join(dir, "README.txt")})
}

// The entries every archive holds, in order: name, mode and contents.
var want = []entry{
	{"defy-installer/", fs.ModeDir | 0o755, nil},
	{"defy-installer/defy-installer", 0o755, []byte("binary")},
	{"defy-installer/defy-installer.yaml", 0o644, []byte("keymap: ''\n")},
	{"defy-installer/README.txt", 0o644, []byte("Read me.\n")},
}

func TestZip(t *testing.T) {
	path, err := pack(t, t.TempDir(), "a.zip")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var got []entry
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			data = nil
		}
		got = append(got, entry{f.Name, f.Mode(), data})
		// Unix modes, which the tools read only from a zip made on Unix,
		// and the time, which MS-DOS's fields hold to 2 seconds.
		if f.CreatorVersion>>8 != 3 || !f.Modified.Equal(modified) || !f.ModTime().Equal(modified.Truncate(2*time.Second)) {
			t.Errorf("%s: made on %d, modified %v and %v", f.Name, f.CreatorVersion>>8, f.Modified, f.ModTime())
		}
	}
	if !slices.EqualFunc(got, want, sameEntry) {
		t.Errorf("entries %v, want %v", got, want)
	}
}

func TestTar(t *testing.T) {
	path, err := pack(t, t.TempDir(), "a.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	if gz.Name != "" || !gz.ModTime.IsZero() {
		t.Errorf("gzip header names %q, time %v", gz.Name, gz.ModTime)
	}
	tr := tar.NewReader(gz)
	var got []entry
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			data = nil
		}
		got = append(got, entry{h.Name, h.FileInfo().Mode(), data})
		if h.Format != tar.FormatUSTAR || !h.ModTime.Equal(modified) || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: %+v", h.Name, h)
		}
	}
	if !slices.EqualFunc(got, want, sameEntry) {
		t.Errorf("entries %v, want %v", got, want)
	}
}

func sameEntry(a, b entry) bool {
	return a.name == b.name && a.mode == b.mode && bytes.Equal(a.data, b.data)
}

// The same files at another time, in another folder, give the same
// archive.
func TestSameArchive(t *testing.T) {
	for _, name := range []string{"a.zip", "a.tar.gz"} {
		first, err := pack(t, t.TempDir(), name)
		if err != nil {
			t.Fatal(err)
		}
		second, err := pack(t, t.TempDir(), name)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := os.ReadFile(first)
		b, _ := os.ReadFile(second)
		if len(a) == 0 || !bytes.Equal(a, b) {
			t.Errorf("%s: %d and %d bytes differ", name, len(a), len(b))
		}
	}
}

func TestRefuses(t *testing.T) {
	dir := t.TempDir()
	if _, err := pack(t, dir, "a.tar.xz"); err == nil || !strings.Contains(err.Error(), "not a .zip or .tar.gz") {
		t.Errorf("error %v", err)
	}
	other := filepath.Join(dir, "other")
	os.Mkdir(other, 0o755)
	os.WriteFile(filepath.Join(other, "README.txt"), nil, 0o644)
	err := run(filepath.Join(dir, "b.zip"), modified, filepath.Join(dir, "defy-installer-linux-amd64"), "defy-installer",
		[]string{filepath.Join(dir, "README.txt"), filepath.Join(other, "README.txt")})
	if err == nil || !strings.Contains(err.Error(), "two files named README.txt") {
		t.Errorf("error %v", err)
	}
}

// TestUnpacks unpacks the archives with the tools users have, where this
// system has them: ditto, which unpacks as macOS's Archive Utility does,
// unzip and tar. The binary must come out executable.
func TestUnpacks(t *testing.T) {
	dir := t.TempDir()
	zipPath, err := pack(t, dir, "a.zip")
	if err != nil {
		t.Fatal(err)
	}
	tarPath, err := pack(t, dir, "a.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	for _, tool := range []struct {
		name string
		args func(archive, into string) []string
		path string
	}{
		{"ditto", func(a, into string) []string { return []string{"-x", "-k", a, into} }, zipPath},
		{"unzip", func(a, into string) []string { return []string{"-q", a, "-d", into} }, zipPath},
		// tar runs in the destination with a relative archive path: GNU tar,
		// as Git for Windows ships it, reads C:\... as host:file.
		{"tar", func(a, into string) []string { return []string{"-x", "-z", "-f", relative(t, into, a)} }, tarPath},
	} {
		path, err := exec.LookPath(tool.name)
		if err != nil {
			continue
		}
		ran = true
		into := t.TempDir()
		cmd := exec.Command(path, tool.args(tool.path, into)...)
		cmd.Dir = into
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", tool.name, err, out)
			continue
		}
		for _, e := range want[1:] {
			path := filepath.Join(into, filepath.FromSlash(e.name))
			info, err := os.Stat(path)
			if err != nil {
				t.Errorf("%s: %v", tool.name, err)
				continue
			}
			data, _ := os.ReadFile(path)
			// Windows has no executable bit.
			if !bytes.Equal(data, e.data) || os.PathSeparator == '/' && info.Mode().Perm()&0o100 != e.mode.Perm()&0o100 {
				t.Errorf("%s: %s has mode %v and %q", tool.name, e.name, info.Mode(), data)
			}
		}
	}
	if !ran {
		t.Skip("no ditto, unzip or tar")
	}
}

// relative is target's path from dir.
func relative(t *testing.T, dir, target string) string {
	t.Helper()
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}
