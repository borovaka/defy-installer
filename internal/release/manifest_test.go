// SPDX-License-Identifier: MIT

package release

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A manifest in the form docs/versions.md gives, as scripts/build writes
// it.
const docManifest = `{
  "schema": 1,
  "module": "v0.1.0",
  "source": "borovaka/zmk-defy-wireless",
  "commit": "2826cb2f0d6a0b8b0c1e7a5a9d3c4b2e1f0a9b8c",
  "commit_time": "2026-10-24T12:05:31+03:00",
  "min_installer": "v0.1.0",
  "files": {
    "neuron": {"name": "neuron-hybrid.hex", "size": 285712, "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
    "left": {"name": "left-hybrid-radio.bin", "size": 471040, "sha256": "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
             "program_crc32": 3221225472, "seal_version": 1},
    "right": {"name": "right-hybrid-radio.bin", "size": 471040, "sha256": "2123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
              "program_crc32": 1073741824, "seal_version": 1}
  }
}
`

func TestParseManifest(t *testing.T) {
	m, err := parseManifest(manifestName, []byte(docManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Module != "v0.1.0" || m.Source != DefaultRepo || m.Commit != "2826cb2f0d6a0b8b0c1e7a5a9d3c4b2e1f0a9b8c" ||
		m.MinInstaller != "v0.1.0" || !m.CommitTime.Equal(time.Date(2026, 10, 24, 9, 5, 31, 0, time.UTC)) {
		t.Errorf("manifest %+v", m)
	}
	if m.Neuron.Name != neuronName || m.Neuron.Size != 285712 || m.Neuron.SHA256[0] != 0x01 || m.Neuron.ProgramCRC32 != 0 {
		t.Errorf("neuron %+v", m.Neuron)
	}
	if m.Left.Name != leftRadio || m.Left.ProgramCRC32 != 3221225472 || m.Left.SealVersion != 1 ||
		m.Right.Name != rightRadio || m.Right.ProgramCRC32 != 1073741824 || m.Right.SealVersion != 1 {
		t.Errorf("halves %+v %+v", m.Left, m.Right)
	}
}

// manifestAccepts are changes to a manifest that it still reads with: a
// build outside git, a dirty tree and fields a newer module adds.
var manifestAccepts = map[string]func(m map[string]any){
	"outside git": func(m map[string]any) {
		m["source"], m["commit"], m["commit_time"], m["module"] = "local", "", "", "unknown"
	},
	"dirty":          func(m map[string]any) { m["commit"] = strings.Repeat("ab", 20) + "-dirty" },
	"SHA-256 commit": func(m map[string]any) { m["commit"] = strings.Repeat("ab", 32) + "-dirty" },
	"UTC":            func(m map[string]any) { m["commit_time"] = "2026-10-24T09:05:31Z" },
	"a new field":    func(m map[string]any) { m["radio"] = map[string]any{"name": "radio.bin"} },
	"a pre-release":  func(m map[string]any) { m["min_installer"] = "v0.2.0-rc.1" },
}

func TestParseManifestAccepts(t *testing.T) {
	for name, change := range manifestAccepts {
		m := manifestFields(t, images(1), neuronName, leftRadio, rightRadio)
		change(m)
		if _, err := parseManifest(name, encode(t, m)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// listed returns the entry for key in a manifest's files.
func listed(m map[string]any, key string) map[string]any {
	return m["files"].(map[string]any)[key].(map[string]any)
}

// manifestRefusals are changes to a manifest that it is refused with, by
// what the error says.
var manifestRefusals = map[string]func(m map[string]any){
	"m.json has no schema": func(m map[string]any) { delete(m, "schema") },
	"has schema 2, which this installer does not read; it needs defy-installer v0.4.0 or later, from " +
		"https://github.com/borovaka/defy-installer/releases/latest": func(m map[string]any) {
		m["schema"], m["min_installer"] = 2, "v0.4.0"
	},
	"has schema 2, and this installer reads only schema 1": func(m map[string]any) { m["schema"], m["min_installer"] = 2, 4 },
	`has schema "1", which this installer does not read`:   func(m map[string]any) { m["schema"] = "1" },
	"module is missing":                        func(m map[string]any) { delete(m, "module") },
	"is not printable ASCII without":           func(m map[string]any) { m["module"] = strings.Repeat("v", 33) },
	`module "" is not printable ASCII`:         func(m map[string]any) { m["module"] = "" },
	`source "alice\\defy" is not printable`:    func(m map[string]any) { m["source"] = `alice\defy` },
	`commit "8c04e7d" is not a full commit`:    func(m map[string]any) { m["commit"] = "8c04e7d" },
	`commit_time "yesterday" is not an RFC`:    func(m map[string]any) { m["commit_time"] = "yesterday" },
	`min_installer "v0.2" is not`:              func(m map[string]any) { m["min_installer"] = "v0.2" },
	"files is missing":                         func(m map[string]any) { delete(m, "files") },
	"files.right is missing":                   func(m map[string]any) { delete(m["files"].(map[string]any), "right") },
	"files.neuron needs name, size and sha256": func(m map[string]any) { delete(listed(m, "neuron"), "sha256") },
	"files.left needs program_crc32 and seal_version": func(m map[string]any) {
		delete(listed(m, "left"), "seal_version")
	},
	`files.left.name "../left.bin" is not a plain file name`: func(m map[string]any) { listed(m, "left")["name"] = "../left.bin" },
	"files.neuron.size 0 is not 1 to 4194304 bytes":          func(m map[string]any) { listed(m, "neuron")["size"] = 0 },
	"is not a SHA-256 in lowercase hex": func(m map[string]any) {
		listed(m, "right")["sha256"] = strings.ToUpper(listed(m, "right")["sha256"].(string))
	},
	"files.right has the name left-hybrid-radio.bin, as files.left does": func(m map[string]any) {
		listed(m, "right")["name"] = leftRadio
	},
	"files.neuron has the name manifest.json, as the manifest does": func(m map[string]any) {
		listed(m, "neuron")["name"] = manifestName
	},
}

func TestParseManifestRefuses(t *testing.T) {
	for want, change := range manifestRefusals {
		m := manifestFields(t, images(1), neuronName, leftRadio, rightRadio)
		change(m)
		_, err := parseManifest("m.json", encode(t, m))
		wantError(t, err, want)
		if err != nil && !strings.HasPrefix(err.Error(), "m.json") {
			t.Errorf("error %q does not name the manifest", err)
		}
	}
	// A CRC outside uint32 does not read.
	m := manifestFields(t, images(1), neuronName, leftRadio, rightRadio)
	listed(m, "left")["program_crc32"] = -1
	if _, err := parseManifest("m.json", encode(t, m)); err == nil {
		t.Error("a CRC of -1 passed")
	}
	_, err := parseManifest("m.json", []byte(`{"schema": 1,`))
	wantError(t, err, "m.json is not valid JSON")
	_, err = parseManifest("m.json", []byte(docManifest+"{}"))
	wantError(t, err, "m.json is not valid JSON")
}

// A name given twice is invalid JSON, and a name in another case is not
// the field's name, so the field is missing.
func TestParseManifestNames(t *testing.T) {
	for in, want := range map[string]string{
		`{"schema": 1, "schema": 1}`:                        "m.json is not valid JSON",
		strings.Replace(docManifest, `"left"`, `"Left"`, 1): "m.json: files.left is missing",
	} {
		_, err := parseManifest("m.json", []byte(in))
		wantError(t, err, want)
	}
}

func TestCheckInstaller(t *testing.T) {
	for _, tc := range []struct {
		version, min string
		ok           bool
	}{
		{"v0.1.0", "v0.1.0", true},
		{"v0.1.0", "v0.1.1", false},
		// Development builds pass, whatever their version.
		{"(devel)", "v9.0.0", true},
		{"", "v9.0.0", true},
		{"v0.0.0-20261024120000-0123456789ab", "v9.0.0", true},
		{"v0.1.1-0.20261024120000-0123456789ab", "v9.0.0", true},
		{"v0.1.1-0.20261024120000-0123456789ab+dirty", "v9.0.0", true},
		{"v0.2.0-rc.1.0.20261024120000-0123456789ab", "v9.0.0", true},
		{"v0.1.0+dirty", "v9.0.0", true},
		{"v0.1.0+incompatible.dirty", "v9.0.0", true},
		{"0.1.0", "v9.0.0", true},
		{"v0.1", "v9.0.0", true},
	} {
		err := (&Manifest{MinInstaller: tc.min}).checkInstaller(tc.version)
		if tooOld, isTooOld := errors.AsType[*TooOldError](err); tc.ok != (err == nil) ||
			!tc.ok && (!isTooOld || tooOld.Version != tc.version || tooOld.MinInstaller != tc.min) {
			t.Errorf("installer %q, min_installer %q: %v", tc.version, tc.min, err)
		}
	}
}
