// SPDX-License-Identifier: MIT

package release

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"
)

// GitHub's addresses.
const (
	githubURL = "https://github.com"
	rawURL    = "https://raw.githubusercontent.com"
)

// defaultTimeout bounds each download when Client.httpClient is nil.
const defaultTimeout = 2 * time.Minute

// maxRedirects is as many redirects as Go's own client follows.
const maxRedirects = 10

// Client fetches builds and Dygma's files. Its zero value downloads from
// GitHub and checks manifests against the version Go recorded in this
// binary.
type Client struct {
	// httpClient makes the requests; nil is a client with a 2-minute
	// timeout on http.DefaultTransport, which honours the proxy variables.
	// Each request uses a copy with a CheckRedirect of its own.
	httpClient *http.Client
	// githubBase and rawBase replace https://github.com and
	// https://raw.githubusercontent.com, for tests.
	githubBase, rawBase string
	// installerVersion is the running installer's version, which each
	// manifest's min_installer is checked against; empty is the version Go
	// recorded in this binary. See Manifest.checkInstaller.
	installerVersion string
}

// NotFound is what GitHub did not find.
type NotFound int

const (
	// NoRelease is a repository without a published release: GitHub
	// found no latest release. A private or missing repository looks the
	// same.
	NoRelease NotFound = iota
	// NoTag is a tag without a release, or a release without
	// manifest.json; one request cannot tell them apart.
	NoTag
	// NoFile is a release, found by its tag, without a file.
	NoFile
)

// NotFoundError is GitHub's 404 for a release's file.
type NotFoundError struct {
	Kind NotFound
	Repo string
	// Tag is the release's tag: the one asked for, or the one GitHub named
	// as the latest. Empty for NoRelease.
	Tag string
	// File is the file GitHub did not find.
	File string
}

func (e *NotFoundError) Error() string {
	switch e.Kind {
	case NoRelease:
		return fmt.Sprintf("%s has no published release on GitHub, or is not a public repository", e.Repo)
	case NoTag:
		return fmt.Sprintf("%s has no release tagged %s, or that release has no %s", e.Repo, e.Tag, e.File)
	}
	return fmt.Sprintf("release %s of %s has no %s", e.Tag, e.Repo, e.File)
}

// Release is a release on GitHub that Resolve found, before its images are
// downloaded.
type Release struct {
	Source Source
	// Tag is the release's tag: Source.Tag, or the one GitHub named as the
	// latest.
	Tag      string
	Manifest *Manifest
}

// Resolve downloads and checks the manifest of the release src names: the
// one src.Tag names, or else the latest, whose tag it reads from GitHub's
// redirect. A 404 is a *NotFoundError, and a build that needs a newer
// installer a *TooOldError.
func (c *Client) Resolve(ctx context.Context, src Source) (*Release, error) {
	if src.Repo == "" || src.Path != "" {
		return nil, errors.New("no release on GitHub to fetch the build from")
	}
	tag := src.Tag
	link := c.downloadURL(src.Repo, tag, manifestName)
	if tag == "" {
		link = c.github() + "/" + src.Repo + "/releases/latest/download/" + manifestName
	}
	data, named, err := c.get(ctx, link, maxManifestSize, false)
	switch {
	case err != nil && tag != "":
		return nil, notFound(err, NoTag, src.Repo, tag, manifestName)
	case err != nil && named == "":
		return nil, notFound(err, NoRelease, src.Repo, "", manifestName)
	case err != nil:
		return nil, notFound(err, NoFile, src.Repo, named, manifestName)
	case tag == "" && named == "":
		return nil, fmt.Errorf("GitHub did not name the tag of the latest release of %s", src.Repo)
	case tag == "":
		tag = named
	}
	m, err := parseManifest(c.downloadURL(src.Repo, tag, manifestName), data)
	if err != nil {
		return nil, err
	}
	if err := m.checkInstaller(c.version()); err != nil {
		return nil, err
	}
	return &Release{Source: src, Tag: tag, Manifest: m}, nil
}

// Download downloads r's images from r's tag, so that a newer release
// published since Resolve does not mix in, and checks them against r's
// manifest and as a write checks them.
//
// The files of one tag can still change: a keymap repository's workflow
// replaces those of its one release on each build. An image that is
// missing, or that differs from r's manifest, gives a *ChangedError.
// Download does not take the newer build instead, because the caller
// planned what to write from r's manifest.
func (c *Client) Download(ctx context.Context, r *Release) (*Build, error) {
	b, err := c.download(ctx, r)
	if changing(err) {
		return nil, &ChangedError{Repo: r.Source.Repo, Tag: r.Tag}
	}
	return b, err
}

// ChangedError is a release whose files changed while they downloaded.
type ChangedError struct {
	Repo, Tag string
}

func (e *ChangedError) Error() string {
	return fmt.Sprintf("release %s of %s changed while it downloaded; run the command again", e.Tag, e.Repo)
}

// changing reports whether err is what a release whose files are being
// replaced gives: an image that is missing, or that differs from the
// manifest.
func changing(err error) bool {
	if nf, ok := errors.AsType[*NotFoundError](err); ok {
		return nf.Kind == NoFile
	}
	_, ok := errors.AsType[*mismatchError](err)
	return ok
}

// download downloads and checks r's images.
func (c *Client) download(ctx context.Context, r *Release) (*Build, error) {
	f := files{
		read: func(name string, limit int64) ([]byte, error) {
			data, _, err := c.get(ctx, c.downloadURL(r.Source.Repo, r.Tag, name), limit, false)
			return data, notFound(err, NoFile, r.Source.Repo, r.Tag, name)
		},
		label: func(name string) string { return c.downloadURL(r.Source.Repo, r.Tag, name) },
	}
	return readBuild(r.Manifest, [3]string{}, f)
}

// LatestTag returns the tag of repo's latest release, which GitHub's
// redirect names, without downloading anything from it. A repository
// without a release gives a *NotFoundError.
func (c *Client) LatestTag(ctx context.Context, repo string) (string, error) {
	_, tag, err := c.get(ctx, c.github()+"/"+repo+"/releases/latest/download/"+manifestName, maxManifestSize, true)
	if err != nil {
		return "", notFound(err, NoRelease, repo, "", manifestName)
	}
	if tag == "" {
		return "", fmt.Errorf("GitHub did not name the tag of the latest release of %s", repo)
	}
	return tag, nil
}

// statusError is an answer other than 200 OK.
type statusError struct {
	url    string
	status string
	code   int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("downloading %s: the server answered %s", e.url, e.status)
}

// notFound turns a 404 into a *NotFoundError, and returns other errors as
// they are.
func notFound(err error, kind NotFound, repo, tag, name string) error {
	if se, ok := errors.AsType[*statusError](err); ok && se.code == http.StatusNotFound {
		return &NotFoundError{Kind: kind, Repo: repo, Tag: tag, File: name}
	}
	return err
}

// get downloads link, following redirects, and returns its body if it is
// at most limit bytes. It also returns the tag of the first redirect to a
// release's manifest.json, which is how GitHub names the latest release.
// With stopAtTag, it follows no further than that redirect, and returns no
// body.
func (c *Client) get(ctx context.Context, link string, limit int64, stopAtTag bool) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "defy-installer")
	client := http.Client{Timeout: defaultTimeout}
	if c.httpClient != nil {
		client = *c.httpClient
	}
	var tag string
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("more than %d redirects", maxRedirects)
		}
		if via[0].URL.Scheme == "https" && next.URL.Scheme != "https" {
			return fmt.Errorf("refusing a redirect from HTTPS to %s", next.URL.Redacted())
		}
		if tag == "" {
			if t, ok := c.manifestTag(next.URL); ok {
				tag = t
				if stopAtTag {
					return http.ErrUseLastResponse
				}
			}
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, tag, err
	}
	defer resp.Body.Close()
	if stopAtTag && tag != "" {
		return nil, tag, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, tag, &statusError{url: link, status: resp.Status, code: resp.StatusCode}
	}
	data, err := readAtMost(resp.Body, link, limit)
	return data, tag, err
}

// manifestTag returns the tag in a link to a release's manifest.json on
// GitHub: <owner>/<repo>/releases/download/<tag>/manifest.json, with an
// owner, a repository and a tag that ParseSource accepts. Other redirects,
// such as one from a renamed repository, are passed over.
func (c *Client) manifestTag(u *url.URL) (string, bool) {
	base, err := url.Parse(c.github())
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host {
		return "", false
	}
	path, ok := strings.CutPrefix(u.EscapedPath(), strings.TrimSuffix(base.EscapedPath(), "/")+"/")
	if !ok {
		return "", false
	}
	parts := strings.Split(path, "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" || parts[5] != manifestName {
		return "", false
	}
	tag, err := url.PathUnescape(parts[4])
	if err != nil || checkTag(tag) != "" || !validOwner(parts[0]) || !validRepo(parts[1]) {
		return "", false
	}
	return tag, true
}

func (c *Client) downloadURL(repo, tag, name string) string {
	return c.github() + "/" + repo + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name)
}

func (c *Client) github() string {
	if c.githubBase != "" {
		return strings.TrimSuffix(c.githubBase, "/")
	}
	return githubURL
}

func (c *Client) raw() string {
	if c.rawBase != "" {
		return strings.TrimSuffix(c.rawBase, "/")
	}
	return rawURL
}

func (c *Client) version() string {
	if c.installerVersion != "" {
		return c.installerVersion
	}
	return InstallerVersion()
}

// InstallerVersion returns the running installer's version: the module
// version that go install or a tagged build records in the binary, or
// "(devel)".
func InstallerVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "(devel)"
}
