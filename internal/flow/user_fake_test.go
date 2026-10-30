// SPDX-License-Identifier: MIT

package flow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
	"github.com/borovaka/defy-installer/internal/focus/focustest"
	nt "github.com/borovaka/defy-installer/internal/neuron/neurontest"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/settings"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// defy is a whole keyboard for the user commands' tests: a Neuron that
// runs ZMK, Dygma's firmware, its bootloader or the UF2 loader, and
// enumerates again as it moves between them, with two halves behind it.
type defy struct {
	t  *testing.T
	mu sync.Mutex
	// mode is "zmk", "stock", "boot", "loader", or "" with no Neuron
	// connected.
	mode string
	// after is the mode the bootloader starts once S# has gone: "boot"
	// for a bootloader entered with the button, which stays.
	after string
	// started runs as the firmware starts from the bootloader: a test
	// changes there what the new firmware reports.
	started func(d *defy)
	// busy opens fail as for a port another program holds.
	busy int
	// onePort lists of a Neuron running ZMK show only its Studio port, as
	// for a moment after it enumerates.
	onePort int

	updater *fakeUpdater
	// boot is the bootloader's last session, and stock Dygma's firmware,
	// which enters the bootloader on upgrade.neuron. loader is the UF2
	// loader's port, once opened, which enters the bootloader on the touch.
	boot   *nt.Bootloader
	stock  *focustest.Neuron
	loader *nt.Loader
	opened []string
	out    bytes.Buffer
}

func newDefy(t *testing.T, mode string) *defy {
	d := &defy{t: t, mode: mode, after: "zmk", updater: newZMKUpdater(t), stock: focustest.New(t)}
	d.stock.OnReboot = func() { d.setMode("boot") }
	return d
}

func (d *defy) setMode(mode string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mode = mode
}

func (d *defy) list(context.Context) ([]usbserial.Port, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.mode == "zmk" && d.onePort > 0:
		d.onePort--
		return []usbserial.Port{studioPort}, nil
	case d.mode == "zmk":
		return []usbserial.Port{studioPort, updaterPort}, nil
	case d.mode == "stock":
		return []usbserial.Port{stockPort}, nil
	case d.mode == "boot":
		return []usbserial.Port{bootPort}, nil
	case d.mode == "loader":
		return []usbserial.Port{loaderPort}, nil
	}
	return nil, nil
}

func (d *defy) open(path string, _ time.Duration) (Port, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opened = append(d.opened, path)
	if d.busy > 0 {
		d.busy--
		return nil, fmt.Errorf("opening %s: %w", path, usbserial.ErrBusy)
	}
	switch {
	case path == studioPort.Path && d.mode == "zmk":
		return &studioFake{d: d}, nil
	case path == updaterPort.Path && d.mode == "zmk":
		d.updater.Output = nil
		return d.updater, nil
	case path == bootPort.Path && d.mode == "boot":
		d.boot = nt.New(d.t)
		d.boot.OnStart = d.start
		return d.boot, nil
	case path == stockPort.Path && d.mode == "stock":
		return d.stock, nil
	case path == loaderPort.Path && d.mode == "loader":
		d.loader = nt.NewLoader(d.t)
		d.loader.OnRequest = func() { d.setMode("boot") }
		return d.loader, nil
	}
	return nil, fmt.Errorf("opening %s: no such port", path)
}

// start starts the firmware from the bootloader.
func (d *defy) start() {
	d.setMode(d.after)
	if d.after != "boot" && d.started != nil {
		d.started(d)
	}
}

// env is an Env that reaches d.
func (d *defy) env() Env {
	return Env{List: d.list, Open: d.open, Out: &d.out}
}

// studioFake is ZMK Studio's port: it answers no updater frame, and a
// change to 1200 baud then a close enter the bootloader.
type studioFake struct {
	d    *defy
	baud int
}

func (s *studioFake) Read([]byte) (int, error) {
	time.Sleep(50 * time.Millisecond)
	return 0, os.ErrDeadlineExceeded
}

func (s *studioFake) Write(p []byte) (int, error) { return len(p), nil }
func (s *studioFake) SetDTR(bool) error           { return nil }
func (s *studioFake) ResetInput() error           { return nil }

func (s *studioFake) SetBaudRate(baud int) error {
	s.baud = baud
	return nil
}

func (s *studioFake) Close() error {
	if s.baud == 1200 {
		s.d.setMode("boot")
	}
	return nil
}

// newZMKUpdater is the half updater of the Neuron running ZMK, with PAIRING,
// SPLIT STATUS, and two halves that run module v0.1.0 and report over
// their cables once written. Neither half has a valid seal, nor reports
// yet, and neither connects over Bluetooth.
func newZMKUpdater(t *testing.T) *fakeUpdater {
	f := withPairing(t, []byte{1, 2, 3, 3})
	f.Connects = []int{-1, -1}
	for i := range f.Halves {
		f.Halves[i] = ut.Half{Module: "v0.1.0", AfterWrite: ut.Cable}
	}
	return f
}

// The Neuron's errno values that the tests' refusals give.
const (
	eio   = 5
	ebusy = 16
)

// asleep makes a half not report until a key wakes it after requests HALF
// VERSIONS requests; it then reports over Bluetooth.
func asleep(f *fakeUpdater, side firmware.Side, requests int) {
	f.Halves[sideIndex(side)].Asleep = requests
	f.Halves[sideIndex(side)].Link = ut.Bluetooth
}

func sideIndex(side firmware.Side) int {
	if side == firmware.Right {
		return 1
	}
	return 0
}

// runs makes both halves run b's packages, reporting by link.
func runs(f *fakeUpdater, b *release.Build, link ut.Link) {
	for i, h := range []*firmware.HalfImage{b.Left, b.Right} {
		f.Halves[i].CRC, f.Halves[i].Version, f.Halves[i].Link = h.ProgramCRC, h.Version, link
		f.Halves[i].Module = "v0.2.0"
	}
}

// reportsBuild makes the Neuron report b's module, source and commit.
func reportsBuild(f *fakeUpdater, b *release.Build) {
	f.Version = ut.VersionPayload(b.Manifest.Module, b.Manifest.Source, b.Manifest.Commit)
	if f.Clear == nil {
		f.Clear = []int16{0}
	}
}

// source is s as release.ParseSource reads it, as --from gives it.
func source(t *testing.T, s string) release.Source {
	t.Helper()
	src, err := release.ParseSource(s)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// The commits of the tests' builds.
var (
	commitA = strings.Repeat("3f2a91c0", 5)
	commitB = strings.Repeat("8c04e7d0", 5)
)

// testBuild is a build of source at commit, module v0.2.0, made now. Its
// halves' programs are filled with fill, so builds with another fill have
// other program CRCs.
func testBuild(t *testing.T, source, commit string, fill byte) *release.Build {
	t.Helper()
	half := func(name string, side ft.Side) *firmware.HalfImage {
		h, err := firmware.ParseHalfImage(name, ft.HalfPackage(ft.RecordedHalfApp(fill), 1, side))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	b := &release.Build{
		Neuron: recordedImage(t),
		Left:   half("left-hybrid-radio.bin", ft.Left),
		Right:  half("right-hybrid-radio.bin", ft.Right),
	}
	b.Manifest = &release.Manifest{
		Module: "v0.2.0", Source: source, Commit: commit, MinInstaller: "v0.1.0",
		CommitTime: time.Now(),
		Neuron:     release.ManifestFile{Name: "neuron-hybrid.hex"},
		Left:       release.ManifestFile{Name: "left-hybrid-radio.bin", ProgramCRC32: b.Left.ProgramCRC, SealVersion: 1},
		Right:      release.ManifestFile{Name: "right-hybrid-radio.bin", ProgramCRC32: b.Right.ProgramCRC, SealVersion: 1},
	}
	return b
}

// fakeReleases serves builds as GitHub's releases and as files on this
// computer.
type fakeReleases struct {
	// builds holds each release's build by repo@tag, and latest each
	// repository's latest tag.
	builds map[string]*release.Build
	latest map[string]string
	// local holds the builds on this computer by path.
	local map[string]*release.Build
	// stock is Dygma's files, downloaded or read from stockDir.
	stock    *release.Stock
	stockDir string
	// err fails every request, as without the network.
	err       error
	downloads []string
}

func newReleases() *fakeReleases {
	return &fakeReleases{builds: map[string]*release.Build{}, latest: map[string]string{}, local: map[string]*release.Build{}}
}

// publish makes b repo's release tag, and its latest.
func (g *fakeReleases) publish(repo, tag string, b *release.Build) {
	g.builds[strings.ToLower(repo)+"@"+tag] = b
	g.latest[strings.ToLower(repo)] = tag
}

func (g *fakeReleases) Resolve(_ context.Context, src release.Source) (*release.Release, error) {
	if g.err != nil {
		return nil, g.err
	}
	tag := src.Tag
	if tag == "" {
		tag = g.latest[strings.ToLower(src.Repo)]
	}
	b, ok := g.builds[strings.ToLower(src.Repo)+"@"+tag]
	switch {
	case !ok && src.Tag != "":
		return nil, &release.NotFoundError{Kind: release.NoTag, Repo: src.Repo, Tag: tag, File: "manifest.json"}
	case !ok:
		return nil, &release.NotFoundError{Kind: release.NoRelease, Repo: src.Repo, File: "manifest.json"}
	}
	return &release.Release{Source: src, Tag: tag, Manifest: b.Manifest}, nil
}

func (g *fakeReleases) Download(_ context.Context, r *release.Release) (*release.Build, error) {
	if g.err != nil {
		return nil, g.err
	}
	g.downloads = append(g.downloads, r.Source.Repo+"@"+r.Tag)
	return g.builds[strings.ToLower(r.Source.Repo)+"@"+r.Tag], nil
}

func (g *fakeReleases) ReadLocal(path string) (*release.Build, error) {
	b, ok := g.local[path]
	if !ok {
		return nil, fmt.Errorf("%s: %w", path, os.ErrNotExist)
	}
	return b, nil
}

func (g *fakeReleases) LatestTag(_ context.Context, repo string) (string, error) {
	if g.err != nil {
		return "", g.err
	}
	tag, ok := g.latest[strings.ToLower(repo)]
	if !ok {
		return "", &release.NotFoundError{Kind: release.NoRelease, Repo: repo}
	}
	return tag, nil
}

func (g *fakeReleases) FetchStock(context.Context) (*release.Stock, error) {
	if g.err != nil {
		return nil, g.err
	}
	return g.stock, nil
}

// LoadStock reads only the folder the test put g.stock in.
func (g *fakeReleases) LoadStock(dir string) (*release.Stock, error) {
	if dir != g.stockDir {
		return nil, fs.ErrNotExist
	}
	return g.stock, nil
}

// userTest is a user command's run against a fake keyboard.
type userTest struct {
	t        *testing.T
	d        *defy
	releases *fakeReleases
	settings *settings.File
	opts     Options
}

func newUserTest(t *testing.T, mode, command string) *userTest {
	t.Helper()
	s := &settings.File{Path: filepath.Join(t.TempDir(), settings.FileName)}
	u := &userTest{t: t, d: newDefy(t, mode), releases: newReleases(), settings: s}
	u.opts = Options{Command: command, Settings: s, Releases: u.releases, Echo: true}
	return u
}

// run runs the command with input as what the user types.
func (u *userTest) run(f func(context.Context, Env, Options) error, input string) (string, error) {
	u.opts.In = strings.NewReader(input)
	err := f(u.t.Context(), u.d.env(), u.opts)
	return u.d.out.String(), err
}

// errNoNetwork stands in for a failed request to GitHub.
var errNoNetwork = &url.Error{Op: "Get", URL: "https://github.com/borovaka/zmk-defy-wireless/releases/latest/download/manifest.json",
	Err: errors.New("dial tcp: lookup github.com: no such host")}
