// SPDX-License-Identifier: MIT

package release

import (
	"errors"
	"strings"
	"testing"
)

// sources are sources that ParseSource reads.
var sources = map[string]Source{
	"default":                         {Repo: DefaultRepo},
	"Default@v0.1.0":                  {Repo: DefaultRepo, Tag: "v0.1.0"},
	"alice/my-defy":                   {Repo: "alice/my-defy"},
	"alice/my-defy@firmware":          {Repo: "alice/my-defy", Tag: "firmware"},
	"Alice-B/My_Defy.zmk@v1.0.0-rc.1": {Repo: "Alice-B/My_Defy.zmk", Tag: "v1.0.0-rc.1"},
	"a/b@v1.0.0+build.5":              {Repo: "a/b", Tag: "v1.0.0+build.5"},
	"old--name-/x":                    {Repo: "old--name-/x"},
	strings.Repeat("o", 39) + "/" + strings.Repeat("r", 100): {
		Repo: strings.Repeat("o", 39) + "/" + strings.Repeat("r", 100)},
}

func TestParseSource(t *testing.T) {
	for s, want := range sources {
		got, err := ParseSource(s)
		if err != nil || got != want {
			t.Errorf("ParseSource(%q) = %+v, %v; want %+v", s, got, err, want)
		}
	}
}

// sourceRefusals are sources that ParseSource refuses, with the reason it
// gives.
var sourceRefusals = map[string]string{
	"":                                  "it is empty",
	"alice":                             "it has no / between the owner and the repository",
	"alice/my/defy":                     "it has more than one /",
	"https://github.com/alice/my-defy":  "not its address",
	"github.com/alice/my-defy":          "not its address",
	"-alice/my-defy":                    `"-alice" is not a GitHub account name`,
	"al ice/my-defy":                    `"al ice" is not a GitHub account name`,
	strings.Repeat("o", 40) + "/x":      "is not a GitHub account name",
	"/my-defy":                          `"" is not a GitHub account name`,
	"alice/":                            `"" is not a GitHub repository name`,
	"alice/..":                          `".." is not a GitHub repository name`,
	"alice/my defy":                     `"my defy" is not a GitHub repository name`,
	"alice/" + strings.Repeat("r", 101): "is not a GitHub repository name",
	"alice/my-defy@":                    "the tag after @ is empty",
	"alice/my-defy@v1 beta":             `the tag "v1 beta" has characters other than`,
	"alice/my-defy@v1..2":               "is not a name Git allows",
}

func TestParseSourceRefuses(t *testing.T) {
	for s, reason := range sourceRefusals {
		_, err := ParseSource(s)
		se, ok := errors.AsType[*SourceError](err)
		if !ok {
			t.Errorf("ParseSource(%q): error %v, want a SourceError", s, err)
			continue
		}
		if se.Value != s || !strings.Contains(se.Reason, reason) {
			t.Errorf("ParseSource(%q): %+v, want reason %q", s, se, reason)
		}
	}
	_, err := ParseSource("alice")
	wantError(t, err, `"alice" is not default or owner/repo: it has no / between the owner and the repository; `+
		"write it like alice/my-defy, or default@v0.1.0 to stay on one release")
}

func TestSourceString(t *testing.T) {
	for _, tc := range []struct {
		src       Source
		s         string
		isDefault bool
	}{
		{Default(), "default", true},
		{Source{Repo: "Borovaka/ZMK-Defy-Wireless", Tag: "v0.1.0"}, "default@v0.1.0", true},
		{Source{Repo: "alice/my-defy", Tag: "firmware"}, "alice/my-defy@firmware", false},
		{Local("/home/alice/firmware.zip"), "/home/alice/firmware.zip", false},
	} {
		if got := tc.src.String(); got != tc.s || tc.src.IsDefault() != tc.isDefault {
			t.Errorf("%+v: %q, default %v", tc.src, got, tc.src.IsDefault())
		}
		if tc.src.Path == "" {
			if back, err := ParseSource(tc.s); err != nil || !strings.EqualFold(back.Repo, tc.src.Repo) || back.Tag != tc.src.Tag {
				t.Errorf("%q reads back as %+v, %v", tc.s, back, err)
			}
		}
	}
}
