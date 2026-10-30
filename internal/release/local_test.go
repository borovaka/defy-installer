// SPDX-License-Identifier: MIT

package release

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeFolder writes files into a new folder and returns it.
func writeFolder(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeZip writes a zip of files, by their names in it, and returns it.
func writeZip(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "firmware.zip")
	if err := os.WriteFile(path, zipOf(t, files), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// zipOf returns a zip of files, by their names in it, in order.
func zipOf(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		w, err := zw.Create(name)
		if err == nil {
			_, err = w.Write(files[name])
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// within returns files under folder in a zip.
func within(folder string, files map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for name, data := range files {
		out[folder+"/"+name] = data
	}
	return out
}

func fetchLocal(t *testing.T, path, version string) (*Build, error) {
	t.Helper()
	return (&Client{installerVersion: version}).ReadLocal(path)
}

// localBuilds are builds on this computer as a folder holds them, and as
// zips hold them, by name.
func localBuilds(t testing.TB) (folder map[string][]byte, zips map[string]map[string][]byte) {
	// The other files a build's folder holds.
	extra := map[string][]byte{"left-hybrid-radio.uf2": {1}, "radio.bin": {2}, "radio.seal": {3}}
	withExtra := withManifest(t)
	maps.Copy(withExtra, extra)
	junk := within("firmware", withManifest(t))
	junk["__MACOSX/firmware/._neuron-hybrid.hex"] = []byte{0}
	junk["firmware/.DS_Store"] = []byte{0}
	return withExtra, map[string]map[string][]byte{
		"artifact zip":           withExtra,
		"zip of a folder":        within("firmware", withManifest(t)),
		"zip of a folder, macOS": junk,
	}
}

func TestReadLocal(t *testing.T) {
	folder, zips := localBuilds(t)
	paths := map[string]string{"folder": writeFolder(t, folder)}
	for name, files := range zips {
		paths[name] = writeZip(t, files)
	}
	for name, path := range paths {
		b, err := fetchLocal(t, path, "v0.1.0")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if b.Manifest == nil || b.Neuron == nil {
			t.Errorf("%s: build %+v", name, b)
		}
		if !strings.HasPrefix(b.Left.Name, path) || !strings.HasSuffix(b.Left.Name, leftRadio) {
			t.Errorf("%s: left half named %s", name, b.Left.Name)
		}
	}
}

// A build from before manifests installs by its file names, preferring
// the halves that carry the radio firmware.
func TestReadLocalWithoutManifest(t *testing.T) {
	all := images(1)
	all[leftPlain], all[rightPlain] = all[leftRadio], all[rightRadio]
	plain := map[string][]byte{neuronName: all[neuronName], leftPlain: all[leftRadio], rightPlain: all[rightRadio]}
	mixed := maps.Clone(plain)
	mixed[leftRadio] = all[leftRadio]

	for _, tc := range []struct {
		name        string
		path        string
		left, right string
	}{
		{"both pairs", writeFolder(t, all), leftRadio, rightRadio},
		{"both pairs zipped", writeZip(t, within("firmware", all)), leftRadio, rightRadio},
		{"-hybrid only", writeFolder(t, plain), leftPlain, rightPlain},
		{"one -hybrid-radio", writeFolder(t, mixed), leftPlain, rightPlain},
	} {
		b, err := fetchLocal(t, tc.path, "v0.1.0")
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if b.Manifest != nil || !strings.HasSuffix(b.Left.Name, tc.left) || !strings.HasSuffix(b.Right.Name, tc.right) {
			t.Errorf("%s: manifest %v, halves %s and %s", tc.name, b.Manifest, b.Left.Name, b.Right.Name)
		}
	}
}

// localRefusal is a build on this computer that the installer refuses,
// in a folder or a zip, with what its error says.
type localRefusal struct {
	want  string
	files map[string][]byte
	zip   bool
}

func localRefusals(t testing.TB) []localRefusal {
	noNeuron := images(1)
	delete(noNeuron, neuronName)
	noRight := images(1)
	delete(noRight, rightRadio)
	tooNew := images(1)
	m := manifestFields(t, tooNew, neuronName, leftRadio, rightRadio)
	m["min_installer"] = "v0.2.0"
	tooNew[manifestName] = encode(t, m)
	damaged := withManifest(t)
	damaged[leftRadio] = bytes.Clone(damaged[leftRadio])
	damaged[leftRadio][100] ^= 1
	badManifest := images(1)
	badManifest[manifestName] = []byte("{}")
	twoFolders := within("a", images(1))
	maps.Copy(twoFolders, within("b", images(1)))
	large := images(1)
	large[neuronName] = make([]byte, maxFileSize+1)
	return []localRefusal{
		{"has neither manifest.json nor neuron-hybrid.hex", noNeuron, false},
		{"has no manifest.json, and neither left-hybrid-radio.bin and right-hybrid-radio.bin nor left-hybrid.bin and right-hybrid.bin", noRight, false},
		{"this build needs defy-installer v0.2.0 or later", tooNew, true},
		{"left-hybrid-radio.bin does not match the manifest", damaged, false},
		{"has no schema", badManifest, true},
		{"holds no files at its top level, nor one folder", twoFolders, true},
		{"neuron-hybrid.hex is larger than 4194304 bytes", large, true},
	}
}

func TestReadLocalRefuses(t *testing.T) {
	for _, tc := range localRefusals(t) {
		var path string
		if tc.zip {
			path = writeZip(t, tc.files)
		} else {
			path = writeFolder(t, tc.files)
		}
		_, err := fetchLocal(t, path, "v0.1.0")
		wantError(t, err, tc.want)
	}
	notZip := filepath.Join(t.TempDir(), "firmware.zip")
	os.WriteFile(notZip, []byte("not a zip"), 0o644)
	_, err := fetchLocal(t, notZip, "v0.1.0")
	wantError(t, err, "is neither a folder nor a zip")
	if _, err := fetchLocal(t, filepath.Join(t.TempDir(), "missing"), "v0.1.0"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing folder: %v", err)
	}
}

// A folder's files are read inside it: a link that leads out of it is
// refused.
func TestReadLocalStaysInTheFolder(t *testing.T) {
	files := images(1)
	dir := writeFolder(t, map[string][]byte{leftRadio: files[leftRadio], rightRadio: files[rightRadio]})
	outside := writeFolder(t, files)
	if err := os.Symlink(filepath.Join(outside, neuronName), filepath.Join(dir, neuronName)); err != nil {
		t.Skip("no symbolic links:", err)
	}
	_, err := fetchLocal(t, dir, "v0.1.0")
	wantError(t, err, "open "+filepath.Join(dir, neuronName)+": ")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error %v, want one that says the link leads out of the folder", err)
	}
}
