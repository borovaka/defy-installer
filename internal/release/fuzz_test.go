// SPDX-License-Identifier: MIT

package release

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// The fuzz targets run on their seeds with go test. To search further:
//
//	go test -run '^$' -fuzz '^FuzzParseManifest$' -fuzztime 1m ./internal/release

// FuzzParseManifest checks that a manifest the parser reads names each
// file once, and reads back as the same manifest once written out again.
func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(docManifest))
	for _, table := range []map[string]func(m map[string]any){manifestAccepts, manifestRefusals} {
		for _, change := range table {
			m := manifestFields(f, images(1), neuronName, leftRadio, rightRadio)
			change(m)
			f.Add(encode(f, m))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := parseManifest("m.json", data)
		if err != nil {
			if !strings.HasPrefix(err.Error(), "m.json") {
				t.Fatalf("error %q does not name the manifest", err)
			}
			return
		}
		checkManifest(t, m)
		back, err := parseManifest("again", manifestText(t, m))
		if err != nil {
			t.Fatalf("%+v does not read back: %v", m, err)
		}
		if !back.CommitTime.Equal(m.CommitTime) {
			t.Fatalf("commit time %v reads back as %v", m.CommitTime, back.CommitTime)
		}
		back.CommitTime = m.CommitTime
		if *back != *m {
			t.Fatalf("%+v reads back as %+v", m, back)
		}
	})
}

// checkManifest checks that m names each file once, and never as the
// manifest.
func checkManifest(t *testing.T, m *Manifest) {
	t.Helper()
	names := map[string]bool{manifestName: true}
	for _, file := range m.files() {
		if names[file.Name] {
			t.Fatalf("file %+v", file)
		}
		names[file.Name] = true
	}
}

// manifestText writes m out in schema 1.
func manifestText(t testing.TB, m *Manifest) []byte {
	entry := func(f ManifestFile, half bool) map[string]any {
		e := map[string]any{"name": f.Name, "size": f.Size, "sha256": hex.EncodeToString(f.SHA256[:])}
		if half {
			e["program_crc32"], e["seal_version"] = f.ProgramCRC32, f.SealVersion
		}
		return e
	}
	commitTime := ""
	if !m.CommitTime.IsZero() {
		commitTime = m.CommitTime.Format(time.RFC3339Nano)
	}
	return encode(t, map[string]any{
		"schema": 1, "module": m.Module, "source": m.Source, "commit": m.Commit,
		"commit_time": commitTime, "min_installer": m.MinInstaller,
		"files": map[string]any{
			"neuron": entry(m.Neuron, false), "left": entry(m.Left, true), "right": entry(m.Right, true),
		},
	})
}

// FuzzParseSource checks that a source ParseSource reads is written back
// as text that reads as the same source, and that it refuses text with a
// *SourceError for that text.
func FuzzParseSource(f *testing.F) {
	for s := range sources {
		f.Add(s)
	}
	for s := range sourceRefusals {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		src, err := ParseSource(s)
		if err != nil {
			if se, ok := errors.AsType[*SourceError](err); !ok || se.Value != s || se.Reason == "" {
				t.Fatalf("ParseSource(%q): error %v", s, err)
			}
			return
		}
		owner, repo, _ := strings.Cut(src.Repo, "/")
		if src.Path != "" || !validOwner(owner) || !validRepo(repo) || src.Tag != "" && checkTag(src.Tag) != "" {
			t.Fatalf("ParseSource(%q) = %+v", s, src)
		}
		text := src.String()
		back, err := ParseSource(text)
		if err != nil {
			t.Fatalf("%q reads as %+v, written as %q, which does not read: %v", s, src, text, err)
		}
		if !strings.EqualFold(back.Repo, src.Repo) || back.Tag != src.Tag || back.String() != text {
			t.Fatalf("%q reads as %+v, written as %q, which reads as %+v", s, src, text, back)
		}
	})
}
