// SPDX-License-Identifier: MIT

package release

import (
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// manifestName is the manifest's file name, in a release and beside a
// build's images.
const manifestName = "manifest.json"

// maxFileSize caps each file of a build or of Dygma's release. A half's
// package is at most 1772 KiB, and a Neuron image in Intel HEX about three
// times its 452 KiB.
const maxFileSize = 4 << 20

// maxManifestSize caps manifest.json, about 1 KiB as scripts/build writes
// it.
const maxManifestSize = 64 << 10

// InstallerReleases is where the installer's latest release is.
const InstallerReleases = "https://github.com/borovaka/defy-installer/releases/latest"

// Manifest is a build's manifest.json, schema 1, as zmk-defy-wireless's
// docs/versions.md describes it.
type Manifest struct {
	// Module is the zmk-defy-wireless version the build embeds, such as
	// v0.1.0, or unknown.
	Module string
	// Source is the keymap repository the Neuron image embeds, as
	// owner/repo, or local.
	Source string
	// Commit is that repository's commit in full, with -dirty for a
	// changed tree; empty for a build outside git.
	Commit string
	// CommitTime is the commit's own time; zero for a build outside git.
	CommitTime time.Time
	// MinInstaller is the oldest installer version that writes the build.
	MinInstaller string
	Neuron       ManifestFile
	Left         ManifestFile
	Right        ManifestFile
}

// ManifestFile is an image the manifest lists.
type ManifestFile struct {
	Name   string
	Size   int64
	SHA256 [32]byte
	// ProgramCRC32 and SealVersion are a half's, from its package's seal;
	// zero for the Neuron.
	ProgramCRC32 uint32
	SealVersion  uint32
}

// files returns the images m lists: the Neuron's, the left half's and the
// right's.
func (m *Manifest) files() [3]*ManifestFile {
	return [3]*ManifestFile{&m.Neuron, &m.Left, &m.Right}
}

type manifestJSON struct {
	Schema       *int    `json:"schema"`
	Module       *string `json:"module"`
	Source       *string `json:"source"`
	Commit       *string `json:"commit"`
	CommitTime   *string `json:"commit_time"`
	MinInstaller *string `json:"min_installer"`
	Files        *struct {
		Neuron *fileJSON `json:"neuron"`
		Left   *fileJSON `json:"left"`
		Right  *fileJSON `json:"right"`
	} `json:"files"`
}

type fileJSON struct {
	Name         *string `json:"name"`
	Size         *int64  `json:"size"`
	SHA256       *string `json:"sha256"`
	ProgramCRC32 *uint32 `json:"program_crc32"`
	SealVersion  *uint32 `json:"seal_version"`
}

// The limits scripts/build holds the embedded values to, in bytes.
const (
	maxModule = 32
	maxSource = 140
)

var commitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})(-dirty)?$`)

// parseManifest reads and checks a manifest: valid JSON without a key
// twice in one object, schema 1, every field the schema has, and file
// names without paths. name labels its errors. JSON keys match only in
// their own case, and unknown keys are ignored, so a key in another case
// counts as missing.
func parseManifest(name string, data []byte) (*Manifest, error) {
	// Any later schema still has these two fields, so that an installer
	// too old to read it can say which version reads it.
	var head struct {
		Schema       jsontext.Value `json:"schema"`
		MinInstaller jsontext.Value `json:"min_installer"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", name, err)
	}
	switch schema := string(head.Schema); {
	case schema == "" || schema == "null":
		return nil, fmt.Errorf("%s has no schema", name)
	case schema != "1":
		var need string
		if json.Unmarshal(head.MinInstaller, &need) == nil {
			if fullVersion(need) {
				return nil, fmt.Errorf("%s has schema %s, which this installer does not read; it needs defy-installer %s "+
					"or later, from %s", name, schema, need, InstallerReleases)
			}
		}
		return nil, fmt.Errorf("%s has schema %s, and this installer reads only schema 1", name, schema)
	}

	var raw manifestJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	fail := func(format string, args ...any) (*Manifest, error) {
		return nil, fmt.Errorf("%s: "+format, append([]any{name}, args...)...)
	}
	for _, f := range []struct {
		name  string
		value *string
	}{
		{"module", raw.Module}, {"source", raw.Source}, {"commit", raw.Commit},
		{"commit_time", raw.CommitTime}, {"min_installer", raw.MinInstaller},
	} {
		if f.value == nil {
			return fail("%s is missing", f.name)
		}
	}
	m := &Manifest{Module: *raw.Module, Source: *raw.Source, Commit: *raw.Commit, MinInstaller: *raw.MinInstaller}
	if !embeddable(m.Module, maxModule) || m.Module == "" {
		return fail("module %q is not printable ASCII without \" or \\, 1 to %d bytes", m.Module, maxModule)
	}
	if !embeddable(m.Source, maxSource) || m.Source == "" {
		return fail("source %q is not printable ASCII without \" or \\, 1 to %d bytes", m.Source, maxSource)
	}
	if m.Commit != "" && !commitPattern.MatchString(m.Commit) {
		return fail("commit %q is not a full commit hash, with -dirty for a changed tree", m.Commit)
	}
	if t := *raw.CommitTime; t != "" {
		var err error
		if m.CommitTime, err = time.Parse(time.RFC3339, t); err != nil {
			return fail("commit_time %q is not an RFC 3339 time", t)
		}
	}
	if !fullVersion(m.MinInstaller) {
		return fail("min_installer %q is not a version like v0.1.0", m.MinInstaller)
	}

	if raw.Files == nil {
		return fail("files is missing")
	}
	seen := map[string]string{manifestName: "the manifest"}
	for _, f := range []struct {
		key  string
		raw  *fileJSON
		dst  *ManifestFile
		half bool
	}{
		{"neuron", raw.Files.Neuron, &m.Neuron, false},
		{"left", raw.Files.Left, &m.Left, true},
		{"right", raw.Files.Right, &m.Right, true},
	} {
		if err := f.raw.check(f.key, f.half, f.dst); err != nil {
			return fail("%v", err)
		}
		if other, ok := seen[f.dst.Name]; ok {
			return fail("files.%s has the name %s, as %s does", f.key, f.dst.Name, other)
		}
		seen[f.dst.Name] = "files." + f.key
	}
	return m, nil
}

func (f *fileJSON) check(key string, half bool, dst *ManifestFile) error {
	if f == nil {
		return fmt.Errorf("files.%s is missing", key)
	}
	if f.Name == nil || f.Size == nil || f.SHA256 == nil {
		return fmt.Errorf("files.%s needs name, size and sha256", key)
	}
	if half && (f.ProgramCRC32 == nil || f.SealVersion == nil) {
		return fmt.Errorf("files.%s needs program_crc32 and seal_version", key)
	}
	if !validFileName(*f.Name) {
		return fmt.Errorf("files.%s.name %q is not a plain file name: letters, digits, '.', '_', '+' and '-', "+
			"at most 100, not starting with '.'", key, *f.Name)
	}
	if *f.Size <= 0 || *f.Size > maxFileSize {
		return fmt.Errorf("files.%s.size %d is not 1 to %d bytes", key, *f.Size, maxFileSize)
	}
	sum, err := hex.DecodeString(*f.SHA256)
	if err != nil || len(sum) != len(dst.SHA256) || *f.SHA256 != strings.ToLower(*f.SHA256) {
		return fmt.Errorf("files.%s.sha256 %q is not a SHA-256 in lowercase hex", key, *f.SHA256)
	}
	*dst = ManifestFile{Name: *f.Name, Size: *f.Size, SHA256: [32]byte(sum)}
	if half {
		dst.ProgramCRC32, dst.SealVersion = *f.ProgramCRC32, *f.SealVersion
	}
	return nil
}

// embeddable checks a value scripts/build embeds: printable ASCII without
// '"' or '\', at most limit bytes.
func embeddable(s string, limit int) bool {
	if len(s) > limit {
		return false
	}
	for _, c := range []byte(s) {
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// validFileName checks a name the manifest gives a file: no path, and only
// characters that are safe on every OS.
func validFileName(s string) bool {
	return len(s) >= 1 && len(s) <= 100 && s[0] != '.' && onlyChars(s, "._+-")
}

// TooOldError is a build that needs a newer installer.
type TooOldError struct {
	// Version is the running installer's, and MinInstaller the build's.
	Version      string
	MinInstaller string
}

func (e *TooOldError) Error() string {
	return fmt.Sprintf("this build needs defy-installer %s or later, and this one is %s; download the latest from %s",
		e.MinInstaller, e.Version, InstallerReleases)
}

// checkInstaller checks that version, the running installer's, is at least
// m.MinInstaller, and returns a *TooOldError if it is not.
//
// A development build skips the check, since its version does not say what
// it can write: "(devel)", which go run and builds outside a git checkout
// record; a pseudo-version, such as v0.1.1-0.20261024120000-0123456789ab,
// which Go records for a commit without a tag; a version with +dirty, for
// a checkout with changes; and any other version that is not semantic.
func (m *Manifest) checkInstaller(version string) error {
	build := semver.Build(version)
	development := !fullVersion(version) || module.IsPseudoVersion(version) ||
		build == "+dirty" || strings.HasSuffix(build, ".dirty")
	if !development && semver.Compare(version, m.MinInstaller) < 0 {
		return &TooOldError{Version: version, MinInstaller: m.MinInstaller}
	}
	return nil
}

// fullVersion reports whether v is a semantic version with Go's leading v
// and all three numbers, such as v1.2.3-rc.1. The semver package also reads
// v1 and v1.2 as shorthand for v1.0.0 and v1.2.0, which a manifest may not
// use.
func fullVersion(v string) bool {
	return semver.IsValid(v) && semver.Canonical(v)+semver.Build(v) == v
}
