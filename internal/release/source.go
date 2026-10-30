// SPDX-License-Identifier: MIT

// Package release finds the firmware to install: a release of
// zmk-defy-wireless or of a keymap repository on GitHub, a build on this
// computer, or Dygma's 2.3.0 files for going back to stock. It downloads
// through GitHub's release download links, not its REST API, checks each
// file against the build's manifest.json, and checks each image as a write
// does.
package release

import (
	"fmt"
	"strings"
)

// DefaultRepo is the repository whose releases carry the default keymap.
const DefaultRepo = "borovaka/zmk-defy-wireless"

// Source is where a build comes from: a release on GitHub, or a zip or a
// folder on this computer.
type Source struct {
	// Repo is a GitHub repository as owner/repo; empty for a local source.
	Repo string
	// Tag pins Repo's release; empty follows the latest release.
	Tag string
	// Path is a zip or a folder on this computer; empty for a release.
	Path string
}

// Default returns the latest release of DefaultRepo.
func Default() Source {
	return Source{Repo: DefaultRepo}
}

// Local returns a zip or a folder on this computer as a source.
func Local(path string) Source {
	return Source{Path: path}
}

// IsZero reports whether src is the zero Source, which names no build.
func (src Source) IsZero() bool {
	return src == Source{}
}

// IsDefault reports whether src is a release of DefaultRepo.
func (src Source) IsDefault() bool {
	return src.Path == "" && strings.EqualFold(src.Repo, DefaultRepo)
}

// String returns src as ParseSource reads it, with DefaultRepo as
// default, or its path.
func (src Source) String() string {
	if src.Path != "" || src.Repo == "" {
		return src.Path
	}
	s := src.Repo
	if src.IsDefault() {
		s = "default"
	}
	if src.Tag != "" {
		s += "@" + src.Tag
	}
	return s
}

// SourceError is a source that ParseSource cannot read.
type SourceError struct {
	Value string
	// Reason says what is wrong with Value.
	Reason string
}

// SourceForm is the form ParseSource reads, as its errors give it.
const SourceForm = "write it like alice/my-defy, or default@v0.1.0 to stay on one release"

func (e *SourceError) Error() string {
	return fmt.Sprintf("%q is not default or owner/repo: %s; %s", e.Value, e.Reason, SourceForm)
}

// ParseSource reads a source as --from and the settings file's keymap give
// it: default or owner/repo, each with an optional @ and a release tag,
// such as alice/my-defy or default@v0.1.0. The owner and the repository
// must be names GitHub allows. An error is a *SourceError.
func ParseSource(s string) (Source, error) {
	fail := func(format string, args ...any) (Source, error) {
		return Source{}, &SourceError{Value: s, Reason: fmt.Sprintf(format, args...)}
	}
	if s == "" {
		return fail("it is empty")
	}
	if strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "github.com/") {
		return fail("it is a web address, and the installer takes the repository's owner/repo, not its address")
	}
	name, tag, pinned := strings.Cut(s, "@")
	if pinned {
		if reason := checkTag(tag); reason != "" {
			return fail("%s", reason)
		}
	}
	if strings.EqualFold(name, "default") {
		return Source{Repo: DefaultRepo, Tag: tag}, nil
	}
	owner, repo, ok := strings.Cut(name, "/")
	switch {
	case !ok:
		return fail("it has no / between the owner and the repository")
	case strings.Contains(repo, "/"):
		return fail("it has more than one /")
	case !validOwner(owner):
		return fail("%q is not a GitHub account name: letters, digits and hyphens, at most 39, "+
			"not starting with a hyphen", owner)
	case !validRepo(repo):
		return fail("%q is not a GitHub repository name: letters, digits, '.', '-' and '_', at most 100", repo)
	}
	return Source{Repo: name, Tag: tag}, nil
}

// validOwner checks a GitHub account name. A trailing hyphen and two
// hyphens in a row pass: GitHub's sign-up refuses them, but older accounts
// have them.
func validOwner(s string) bool {
	return len(s) >= 1 && len(s) <= 39 && s[0] != '-' && onlyChars(s, "-")
}

// validRepo checks a GitHub repository name; GitHub replaces any other
// character with a hyphen when it creates one.
func validRepo(s string) bool {
	return len(s) >= 1 && len(s) <= 100 && s != "." && s != ".." && onlyChars(s, "._-")
}

// checkTag returns why tag is not a release tag the installer accepts, or
// "". It takes the characters release tags use, within Git's rules for
// tag names.
func checkTag(tag string) string {
	switch {
	case tag == "":
		return "the tag after @ is empty"
	case !onlyChars(tag, "._+-"):
		return fmt.Sprintf("the tag %q has characters other than letters, digits, '.', '_', '+' and '-'", tag)
	case tag[0] == '.' || tag[0] == '-' || strings.HasSuffix(tag, ".") ||
		strings.Contains(tag, "..") || strings.HasSuffix(tag, ".lock"):
		return fmt.Sprintf("the tag %q is not a name Git allows", tag)
	}
	return ""
}

// onlyChars reports whether s holds only ASCII letters, digits and the
// characters in extra.
func onlyChars(s, extra string) bool {
	for _, c := range []byte(s) {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte(extra, c) >= 0) {
			return false
		}
	}
	return true
}
