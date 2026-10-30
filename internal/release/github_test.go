// SPDX-License-Identifier: MIT

package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const repo = "alice/my-defy"

func TestFetchLatest(t *testing.T) {
	g := newFakeGitHub(t)
	g.publish(repo, "v0.1.0", withManifest(t))
	g.publish(repo, "v0.2.0", withManifest(t))
	b, err := fetch(t.Context(), g.client("v0.1.0"), Source{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if b.Manifest == nil || b.Manifest.Source != repo || b.Neuron == nil || b.Left == nil || b.Right == nil {
		t.Fatalf("build %+v", b)
	}
	if !strings.HasSuffix(b.Left.Name, "/releases/download/v0.2.0/"+leftRadio) {
		t.Errorf("left half named %s", b.Left.Name)
	}
	// The tag comes from the latest link's redirect, and every file after
	// it from that tag's links.
	for i, p := range g.paths() {
		if i == 0 && p != "/alice/my-defy/releases/latest/download/manifest.json" || i > 0 && !strings.Contains(p, "/v0.2.0/") {
			t.Errorf("request %d: %s", i, p)
		}
	}
}

func TestFetchPinned(t *testing.T) {
	g := newFakeGitHub(t)
	g.publish(repo, "v0.1.0", withManifest(t))
	g.publish(repo, "v0.2.0", withManifest(t))
	c := g.client("v0.1.0")
	r, err := c.Resolve(t.Context(), Source{Repo: repo, Tag: "v0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(t.Context(), r); err != nil || r.Tag != "v0.1.0" {
		t.Errorf("tag %s, %v", r.Tag, err)
	}
	for _, p := range g.paths() {
		if strings.Contains(p, "latest") || strings.Contains(p, "v0.2.0") {
			t.Errorf("asked for %s", p)
		}
	}
}

func TestFetchNotFound(t *testing.T) {
	g := newFakeGitHub(t)
	noManifest := images(1)
	g.publish("bob/old-defy", "firmware", noManifest)
	g.publish(repo, "v0.1.0", withManifest(t))

	for _, tc := range []struct {
		src  Source
		want NotFoundError
		text string
	}{
		{Source{Repo: "dave/nothing"}, NotFoundError{Kind: NoRelease, Repo: "dave/nothing", File: manifestName},
			"dave/nothing has no published release on GitHub, or is not a public repository"},
		{Source{Repo: repo, Tag: "v9.9.9"}, NotFoundError{Kind: NoTag, Repo: repo, Tag: "v9.9.9", File: manifestName},
			"alice/my-defy has no release tagged v9.9.9, or that release has no manifest.json"},
		{Source{Repo: "bob/old-defy"}, NotFoundError{Kind: NoFile, Repo: "bob/old-defy", Tag: "firmware", File: manifestName},
			"release firmware of bob/old-defy has no manifest.json"},
	} {
		_, err := fetch(t.Context(), g.client("v0.1.0"), tc.src)
		nf, ok := errors.AsType[*NotFoundError](err)
		if !ok {
			t.Errorf("%s: error %v, want a NotFoundError", tc.src, err)
			continue
		}
		if *nf != tc.want || err.Error() != tc.text {
			t.Errorf("%s: %+v %q, want %+v %q", tc.src, *nf, err, tc.want, tc.text)
		}
	}
}

// An image that differs from the manifest is checked again, and when the
// manifest stays as it was the release is taken to have changed during
// the download; a folder says what differs.
func TestFetchChecksTheImages(t *testing.T) {
	changed := "release v0.1.0 of alice/my-defy changed while it downloaded; run the command again"
	size := len(images(1)[leftRadio])
	for _, tc := range []struct {
		release, folder string
		change          func(t *testing.T, files map[string][]byte)
	}{
		{changed, "neuron-hybrid.hex does not match the manifest's SHA-256", func(t *testing.T, files map[string][]byte) {
			files[neuronName] = bytes.Clone(files[neuronName])
			files[neuronName][1] ^= 1
		}},
		{changed, fmt.Sprintf("left-hybrid-radio.bin is larger than the %d bytes the manifest says", size), func(t *testing.T, files map[string][]byte) {
			files[leftRadio] = append(files[leftRadio], 0)
		}},
		{changed, fmt.Sprintf("right-hybrid-radio.bin is %d bytes, and the manifest says %d", size-256, size), func(t *testing.T, files map[string][]byte) {
			files[rightRadio] = files[rightRadio][:len(files[rightRadio])-256]
		}},
	} {
		files := withManifest(t)
		tc.change(t, files)
		g := newFakeGitHub(t)
		g.publish(repo, "v0.1.0", files)
		_, err := fetch(t.Context(), g.client("v0.1.0"), Source{Repo: repo})
		wantError(t, err, tc.release)
		_, err = fetchLocal(t, writeFolder(t, files), "v0.1.0")
		wantError(t, err, tc.folder)
	}

	// A half's seal that differs from the manifest's.
	g := newFakeGitHub(t)
	files := withManifest(t)
	m := manifestFields(t, files, neuronName, leftRadio, rightRadio)
	listed(m, "right")["program_crc32"] = 1
	files[manifestName] = encode(t, m)
	g.publish(repo, "v0.1.0", files)
	_, err := fetch(t.Context(), g.client("v0.1.0"), Source{Repo: repo})
	wantError(t, err, "and the manifest says 0x00000001 and 1")
}

func TestFetchNeedsANewerInstaller(t *testing.T) {
	g := newFakeGitHub(t)
	files := images(1)
	m := manifestFields(t, files, neuronName, leftRadio, rightRadio)
	m["min_installer"] = "v0.3.0"
	files[manifestName] = encode(t, m)
	g.publish(repo, "v0.3.0", files)

	_, err := fetch(t.Context(), g.client("v0.2.9"), Source{Repo: repo})
	if tooOld, ok := errors.AsType[*TooOldError](err); !ok || tooOld.MinInstaller != "v0.3.0" || tooOld.Version != "v0.2.9" {
		t.Fatalf("error %v, want a TooOldError", err)
	}
	wantError(t, err, "needs defy-installer v0.3.0 or later, and this one is v0.2.9; download the latest from "+InstallerReleases)
	for _, p := range g.paths() {
		if !strings.HasSuffix(p, manifestName) {
			t.Errorf("downloaded %s for a build it cannot write", p)
		}
	}
	if _, err := fetch(t.Context(), g.client("(devel)"), Source{Repo: repo}); err != nil {
		t.Errorf("a development build: %v", err)
	}
}

// A keymap repository's workflow replaces the files of its one release,
// the images first and the manifest last. A download that sees a changed
// or missing image stops, rather than take a build the caller did not plan
// for.
func TestDownloadAChangingRelease(t *testing.T) {
	noRight := withManifest(t)
	delete(noRight, rightRadio)
	for _, tc := range []struct {
		name   string
		during map[string][]byte
	}{
		{"the images replaced", withManifestOf(t, images(2))},
		{"an image deleted", noRight},
	} {
		g := newFakeGitHub(t)
		g.publish(repo, "firmware", withManifest(t))
		c := g.client("v0.1.0")
		r, err := c.Resolve(t.Context(), Source{Repo: repo})
		if err != nil {
			t.Fatal(err)
		}
		g.publish(repo, "firmware", tc.during)
		_, err = c.Download(t.Context(), r)
		if _, ok := errors.AsType[*ChangedError](err); !ok ||
			err.Error() != "release firmware of alice/my-defy changed while it downloaded; run the command again" {
			t.Errorf("%s: error %v, want a ChangedError", tc.name, err)
		}
	}
}

func TestLatestTag(t *testing.T) {
	g := newFakeGitHub(t)
	g.publish(repo, "v0.2.0", withManifest(t))
	tag, err := g.client("").LatestTag(t.Context(), repo)
	if err != nil || tag != "v0.2.0" {
		t.Fatalf("tag %q, error %v", tag, err)
	}
	if got, want := g.paths(), []string{"/alice/my-defy/releases/latest/download/manifest.json"}; !slices.Equal(got, want) {
		t.Errorf("requests %q, want %q", got, want)
	}
	_, err = g.client("").LatestTag(t.Context(), "dave/nothing")
	if nf, ok := errors.AsType[*NotFoundError](err); !ok || nf.Kind != NoRelease {
		t.Errorf("error %v, want NoRelease", err)
	}
}

// A renamed repository's links redirect to the new name first; the tag
// comes from the redirect to the release's own link.
func TestFetchFromARenamedRepository(t *testing.T) {
	g := newFakeGitHub(t)
	g.publish("alice/new-name", "v1.0.0", withManifest(t))
	g.setIntercept(func(w http.ResponseWriter, r *http.Request, assets bool) bool {
		rest, ok := strings.CutPrefix(r.URL.Path, "/alice/old-name/")
		if ok && !assets {
			http.Redirect(w, r, "/alice/new-name/"+rest, http.StatusMovedPermanently)
		}
		return ok && !assets
	})
	c := g.client("")
	r, err := c.Resolve(t.Context(), Source{Repo: "alice/old-name"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(t.Context(), r); err != nil || r.Tag != "v1.0.0" {
		t.Errorf("tag %s, %v", r.Tag, err)
	}
}

func TestFetchRefusesARedirectToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/alice/my-defy/releases/download/v1/manifest.json", http.StatusFound)
	}))
	defer secure.Close()
	c := &Client{httpClient: secure.Client(), githubBase: secure.URL}
	_, err := fetch(t.Context(), c, Source{Repo: repo})
	wantError(t, err, "refusing a redirect from HTTPS")
}

func TestFetchErrors(t *testing.T) {
	g := newFakeGitHub(t)
	g.setIntercept(func(w http.ResponseWriter, r *http.Request, assets bool) bool {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return true
	})
	_, err := fetch(t.Context(), g.client(""), Source{Repo: repo})
	wantError(t, err, "the server answered 503 Service Unavailable")

	g.setIntercept(func(w http.ResponseWriter, r *http.Request, assets bool) bool {
		w.Write(make([]byte, maxManifestSize+1))
		return true
	})
	_, err = fetch(t.Context(), g.client(""), Source{Repo: repo, Tag: "v1"})
	wantError(t, err, "is larger than 65536 bytes")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fetch(ctx, g.client(""), Source{Repo: repo}); !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want context.Canceled", err)
	}
}

func TestManifestTag(t *testing.T) {
	c := &Client{}
	for link, want := range map[string]string{
		"https://github.com/alice/my-defy/releases/download/v0.2.0/manifest.json":        "v0.2.0",
		"https://github.com/alice/my-defy/releases/download/release%2B1/manifest.json":   "release+1",
		"https://github.com/alice/my-defy/releases/latest/download/manifest.json":        "",
		"https://github.com/alice/my-defy/releases/download/v0.2.0/neuron-hybrid.hex":    "",
		"https://example.com/alice/my-defy/releases/download/v0.2.0/manifest.json":       "",
		"http://github.com/alice/my-defy/releases/download/v0.2.0/manifest.json":         "",
		"https://release-assets.githubusercontent.com/github-production-release-asset/1": "",
		// What GitHub would not send, which the installer would otherwise
		// put into messages and links.
		"https://github.com/alice/my-defy/releases/download/v1%2F2/manifest.json": "",
		"https://github.com/alice/../releases/download/v0.2.0/manifest.json":      "",
	} {
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := c.manifestTag(u); got != want {
			t.Errorf("%s: tag %q, want %q", link, got, want)
		}
	}
}

// Resolve reads the manifest alone; Download then takes the images from
// the same tag, even when a newer release is out by then.
func TestResolveThenDownload(t *testing.T) {
	g := newFakeGitHub(t)
	g.publish(repo, "v0.1.0", withManifest(t))
	c := g.client("v0.1.0")
	r, err := c.Resolve(t.Context(), Source{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if r.Tag != "v0.1.0" || r.Manifest.Source != repo || len(g.paths()) != 3 {
		t.Errorf("release %+v after requests %q", r, g.paths())
	}
	broken := withManifest(t)
	broken[neuronName] = []byte("not this one")
	g.publish(repo, "v0.2.0", broken)
	b, err := c.Download(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if b.Manifest != r.Manifest {
		t.Errorf("build %+v", b)
	}
	if _, err := c.Resolve(t.Context(), Local(t.TempDir())); err == nil {
		t.Error("Resolve took a folder")
	}
}
