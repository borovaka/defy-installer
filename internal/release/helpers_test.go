// SPDX-License-Identifier: MIT

package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/borovaka/defy-installer/internal/firmware"
	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
)

// images returns the images of build n, under the names scripts/build
// gives the -hybrid-radio build; another n gives other contents.
func images(n byte) map[string][]byte {
	return map[string][]byte{
		neuronName: ft.Hex(ft.At(firmware.NeuronCodeStart, ft.NeuronProgram(0x1800, n, firmware.NeuronCodeStart))),
		leftRadio:  ft.HalfPackage(ft.ValidHalfApplication(), uint32(n), ft.Left),
		rightRadio: ft.HalfPackage(ft.ValidHalfApplication(), uint32(n), ft.Right),
	}
}

// manifestFields returns a manifest for files, under the names neuron,
// left and right, as a map that tests can change before encoding it.
func manifestFields(t testing.TB, files map[string][]byte, neuron, left, right string) map[string]any {
	t.Helper()
	entry := func(name string, half bool) map[string]any {
		data, ok := files[name]
		if !ok {
			t.Fatalf("no file %s", name)
		}
		sum := sha256.Sum256(data)
		e := map[string]any{"name": name, "size": len(data), "sha256": hex.EncodeToString(sum[:])}
		if half {
			h, err := firmware.ParseHalfImage(name, data)
			if err != nil {
				t.Fatal(err)
			}
			e["program_crc32"], e["seal_version"] = h.ProgramCRC, h.Version
		}
		return e
	}
	return map[string]any{
		"schema":        1,
		"module":        "v0.1.0",
		"source":        "alice/my-defy",
		"commit":        strings.Repeat("8c04e7d0", 5),
		"commit_time":   "2026-10-24T12:05:31+03:00",
		"min_installer": "v0.1.0",
		"files": map[string]any{
			"neuron": entry(neuron, false),
			"left":   entry(left, true),
			"right":  entry(right, true),
		},
	}
}

func encode(t testing.TB, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

// withManifest returns a build's images and its manifest.json.
func withManifest(t testing.TB) map[string][]byte {
	t.Helper()
	return withManifestOf(t, images(1))
}

// withManifestOf adds a manifest.json to files, the images of a build.
func withManifestOf(t testing.TB, files map[string][]byte) map[string][]byte {
	t.Helper()
	files[manifestName] = encode(t, manifestFields(t, files, neuronName, leftRadio, rightRadio))
	return files
}

// fakeGitHub imitates GitHub's release downloads: a link to the latest
// release redirects to the release's own link, which redirects to the file
// on a second server, as GitHub's asset host.
type fakeGitHub struct {
	github, assets *httptest.Server

	mu sync.Mutex
	// latest is each repository's latest release, and releases each
	// repository's files by tag.
	latest   map[string]string
	releases map[string]map[string]map[string][]byte
	// requests holds the paths asked of both servers, in order.
	requests []string
	// intercept, if set, sees each request first, and answers it by
	// returning true.
	intercept func(w http.ResponseWriter, r *http.Request, assets bool) bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{latest: map[string]string{}, releases: map[string]map[string]map[string][]byte{}}
	g.assets = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.intercepted(w, r, true) {
			return
		}
		repo, rest, _ := cut3(r.URL.Path)
		tag, name, _ := strings.Cut(rest, "/")
		if data, ok := g.file(repo, tag, name); ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	g.github = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.intercepted(w, r, false) {
			return
		}
		repo, rest, ok := cut3(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if name, ok := strings.CutPrefix(rest, "releases/latest/download/"); ok {
			g.mu.Lock()
			tag := g.latest[repo]
			g.mu.Unlock()
			if tag == "" {
				http.NotFound(w, r)
				return
			}
			http.Redirect(w, r, "/"+repo+"/releases/download/"+tag+"/"+name, http.StatusFound)
			return
		}
		if file, ok := strings.CutPrefix(rest, "releases/download/"); ok {
			tag, name, _ := strings.Cut(file, "/")
			if _, ok := g.file(repo, tag, name); ok {
				http.Redirect(w, r, g.assets.URL+"/"+repo+"/"+tag+"/"+name, http.StatusFound)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(g.github.Close)
	t.Cleanup(g.assets.Close)
	return g
}

// cut3 splits /owner/repo/rest into owner/repo and rest.
func cut3(path string) (repo, rest string, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(parts) != 3 {
		return "", "", false
	}
	return parts[0] + "/" + parts[1], parts[2], true
}

// publish adds a release to repo, and makes it the latest.
func (g *fakeGitHub) publish(repo, tag string, files map[string][]byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.releases[repo] == nil {
		g.releases[repo] = map[string]map[string][]byte{}
	}
	g.releases[repo][tag] = files
	g.latest[repo] = tag
}

func (g *fakeGitHub) file(repo, tag, name string) ([]byte, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	data, ok := g.releases[repo][tag][name]
	return data, ok
}

// intercepted logs a request, and passes it to intercept.
func (g *fakeGitHub) intercepted(w http.ResponseWriter, r *http.Request, assets bool) bool {
	g.mu.Lock()
	path := r.URL.Path
	if assets {
		path = "assets " + path
	}
	g.requests = append(g.requests, path)
	intercept := g.intercept
	g.mu.Unlock()
	return intercept != nil && intercept(w, r, assets)
}

// setIntercept sets intercept.
func (g *fakeGitHub) setIntercept(f func(w http.ResponseWriter, r *http.Request, assets bool) bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.intercept = f
}

func (g *fakeGitHub) paths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.requests...)
}

// client returns a client of g, as an installer at version.
func (g *fakeGitHub) client(version string) *Client {
	return &Client{httpClient: g.github.Client(), githubBase: g.github.URL, rawBase: g.assets.URL,
		installerVersion: version}
}

func wantError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Errorf("no error, want one containing %q", contains)
	} else if !strings.Contains(err.Error(), contains) {
		t.Errorf("error %q does not contain %q", err, contains)
	}
}

// fetch resolves the release src names, then downloads its images.
func fetch(ctx context.Context, c *Client, src Source) (*Build, error) {
	r, err := c.Resolve(ctx, src)
	if err != nil {
		return nil, err
	}
	return c.Download(ctx, r)
}
