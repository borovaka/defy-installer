// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/borovaka/defy-installer/internal/flow"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/settings"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// testDeps are Deps that fail the test when a port is listed or opened,
// read a settings file that does not exist yet, find nothing on GitHub,
// and flush nothing.
func testDeps(t *testing.T) Deps {
	return Deps{
		Env:           noPorts(t),
		Settings:      settingsAt(filepath.Join(t.TempDir(), settings.FileName)),
		Releases:      noReleases{},
		FlushTerminal: func(*os.File) error { return nil },
	}
}

// settingsAt reads a settings file at path that does not exist yet.
func settingsAt(path string) func() (*settings.File, error) {
	return func() (*settings.File, error) { return &settings.File{Path: path}, nil }
}

// run runs the command line with testDeps, and returns what it printed.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runWith(t, testDeps(t), args...)
}

// runWith runs the command line with d, as Run does, and returns what it
// printed.
func runWith(t *testing.T, d Deps, args ...string) (string, error) {
	t.Helper()
	var out, errs strings.Builder
	root := d.rootCommand()
	root.Writer, root.ErrWriter = &out, &errs
	err := runRoot(t.Context(), root, append([]string{"defy-installer"}, args...))
	return out.String(), err
}

func write(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func wantOutput(t *testing.T, out string, err error, lines ...string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
}

func wantError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Errorf("error %v, want one containing %q", err, contains)
	}
}

// noReleases fails every request, as without the network.
type noReleases struct{}

var errOffline = errors.New("offline")

func (noReleases) Resolve(context.Context, release.Source) (*release.Release, error) {
	return nil, errOffline
}
func (noReleases) Download(context.Context, *release.Release) (*release.Build, error) {
	return nil, errOffline
}
func (noReleases) ReadLocal(string) (*release.Build, error)           { return nil, errOffline }
func (noReleases) LatestTag(context.Context, string) (string, error)  { return "", errOffline }
func (noReleases) FetchStock(context.Context) (*release.Stock, error) { return nil, errOffline }

// LoadStock reads a folder on this computer, which needs no network.
func (noReleases) LoadStock(dir string) (*release.Stock, error) {
	return (&release.Client{}).LoadStock(dir)
}

// noPorts fails the test when a port is listed or opened.
func noPorts(t *testing.T) flow.Env {
	env := withoutNeuron(t)
	env.List = func(context.Context) ([]usbserial.Port, error) {
		t.Error("a port was listed")
		return nil, nil
	}
	return env
}

// withoutNeuron lists no ports, and fails the test if one is opened.
func withoutNeuron(t *testing.T) flow.Env {
	return flow.Env{
		List: func(context.Context) ([]usbserial.Port, error) { return nil, nil },
		Open: func(string, time.Duration) (flow.Port, error) {
			t.Error("a port was opened")
			return nil, errors.New("no ports")
		},
	}
}
