// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/settings"
	"github.com/borovaka/defy-installer/internal/updater"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

func TestChoosingTheSource(t *testing.T) {
	alice := release.Source{Repo: "alice/my-defy"}
	version := func(source string) *updater.Version {
		return &updater.Version{Module: "v0.2.0", Source: source, Commit: commitA}
	}
	builds := t.TempDir()
	zip, folder := filepath.Join(builds, "a.zip"), filepath.Join(builds, "b")
	for _, tt := range []struct {
		name     string
		from     release.Source
		firmware string
		keymap   release.Source
		saved    string
		v        *updater.Version
		want     choice
		err      string
	}{
		{name: "default", want: choice{release.Default(), byDefault}},
		{name: "the Neuron's", v: version("alice/my-defy"), want: choice{alice, byNeuron}},
		{name: "the Neuron's default", v: version("borovaka/zmk-defy-wireless"), want: choice{release.Default(), byNeuron}},
		{name: "settings keymap", keymap: release.Source{Repo: "bob/keys", Tag: "v1"}, v: version("alice/my-defy"),
			want: choice{release.Source{Repo: "bob/keys", Tag: "v1"}, bySettingsKeymap}},
		{name: "settings firmware", keymap: release.Source{Repo: "bob/keys"}, saved: zip, v: version("alice/my-defy"),
			want: choice{release.Local(zip), bySettingsFirmware}},
		{name: "--from", from: alice, saved: zip, want: choice{alice, byFlag}},
		{name: "--firmware", firmware: folder, keymap: release.Source{Repo: "bob/keys"}, want: choice{release.Local(folder), byFlag}},
		{name: "a local build", v: version("local"), err: "--from default"},
		{name: "a local build with a keymap saved", keymap: release.Default(), v: version("local"),
			want: choice{release.Default(), bySettingsKeymap}},
		{name: "a source not on GitHub", v: version("not a repository"), err: "cannot follow"},
	} {
		r := &run{opts: Options{Command: "update", From: tt.from, Firmware: tt.firmware,
			Settings: &settings.File{Keymap: tt.keymap, Firmware: tt.saved}}}
		got, err := r.choose(tt.v)
		switch {
		case tt.err != "":
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("%s: got %v, %v", tt.name, got, err)
			}
		case err != nil || got != tt.want:
			t.Errorf("%s: got %+v, %v; want %+v", tt.name, got, err, tt.want)
		}
	}
}

func TestALocalBuildIsNotReplacedUnasked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		u.d.updater.Version = ut.VersionPayload("v0.2.0-3-g1234567", "local", "")
		out, err := u.run(Install, "")
		wantError(t, err, "The keyboard runs a local build, which is not on GitHub, so the installer does not replace it "+
			"unless told what to install instead.\nFor the default layout: defy-installer update --from default\n"+
			"For a build on this computer: defy-installer update --firmware <zip or folder>")
		if !strings.HasPrefix(out, "Found your Defy. It runs ZMK v0.2.0-3-g1234567, a local build.\n") ||
			len(u.releases.downloads) > 0 || slices.Contains(u.d.opened, bootPort.Path) {
			t.Errorf("output:\n%s", out)
		}
	})
}

func TestSavedChoicesAreSaid(t *testing.T) {
	bubble(t, "saved firmware", func(t *testing.T) {
		// A saved firmware path decides, and says how to use GitHub again.
		u, _, newBuild := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		zip := filepath.Join(t.TempDir(), "firmware.zip")
		u.releases.local[zip] = newBuild
		u.settings.Keymap, u.settings.Firmware = source(t, "alice/my-defy"), zip
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out,
			"Found your Defy. It runs ZMK from alice/my-defy, commit 3f2a91c.",
			"Installing from "+zip+", saved in defy-installer.yaml. It does not check GitHub.",
			"To use your keymap repository again: defy-installer update --from alice/my-defy",
			"Read firmware.zip: alice/my-defy, commit 8c04e7d, module v0.2.0. Checked.",
			neuronOnly,
			"Done.")
	})
	bubble(t, "pinned tag", func(t *testing.T) {
		// A pinned tag, with a newer release out.
		u, oldBuild, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		u.releases.builds[strings.ToLower(release.DefaultRepo)+"@v0.1.0"] = oldBuild
		u.settings.Keymap = source(t, "default@v0.1.0")
		reportsBuild(u.d.updater, oldBuild)
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out,
			"Found your Defy. It runs ZMK v0.1.0 with the default layout.",
			"Staying on v0.1.0, pinned in defy-installer.yaml. The latest is v0.2.0.",
			"To follow the latest again: defy-installer update --from default",
			"It is up to date.")
	})
	bubble(t, "pinned latest", func(t *testing.T) {
		// A pinned tag that is the latest needs no notice.
		u, _, newBuild := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		reportsBuild(u.d.updater, newBuild)
		u.settings.Keymap = source(t, "default@v0.2.0")
		out, err := u.run(Install, "\n")
		if err != nil || strings.Contains(out, "Staying") {
			t.Errorf("got %v:\n%s", err, out)
		}
	})
}

func TestSettingsAreSavedOnlyOnSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		alice := testBuild(t, "alice/my-defy", commitB, 0xa5)
		u.releases.publish("alice/my-defy", "firmware", alice)
		u.d.started = func(d *defy) { reportsBuild(d.updater, alice) }
		u.opts.From = source(t, "alice/my-defy")
		// The first run is interrupted as it writes the Neuron.
		ctx, cancel := context.WithCancel(t.Context())
		w := &cancelOn{line: "Writing the Neuron  [", cancel: cancel}
		env := u.d.env()
		env.Out = w
		u.opts.In = strings.NewReader("")
		err := Install(ctx, env, u.opts)
		cancel()
		wantError(t, err, "Stopped. The Neuron is waiting in its bootloader. Keep it plugged in and run\n"+
			"  defy-installer install --from alice/my-defy\nagain to finish. "+clears+"Unplugged, it needs its button "+
			"to reach the bootloader again.")
		if !errors.Is(err, context.Canceled) || u.settings.Exists {
			t.Errorf("got %v; settings saved %v", err, u.settings.Exists)
		}
		if _, err := os.Stat(u.settings.Path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the settings file exists: %v", err)
		}

		// The Neuron waits in its bootloader and reports nothing. The next
		// run is the one the message gives: it writes the Neuron, clearing
		// its settings, then the halves with a pairing reset, and saves
		// --from.
		follow(t, &u.opts, err)
		u.d.setMode("boot")
		u.d.started = func(d *defy) {
			reportsBuild(d.updater, alice)
			d.updater.Pairing = []byte{1, 2, 0, 0}
		}
		u.d.updater.Connects = []int{1, 1}
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, append(firstInstallDone[:4:4],
			`From now on, "defy-installer update" installs the latest build of alice/my-defy.`,
			"For the default layout again: defy-installer update --from default")...)
		if s := u.settings; !s.Exists || s.Keymap != source(t, "alice/my-defy") || s.Firmware != "" {
			t.Errorf("saved %+v", s)
		}
	})
}

func TestAFirmwareFolderIsSavedAndNamesItsRepository(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _, newBuild := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		reportsBuild(u.d.updater, newBuild)
		alice := testBuild(t, "alice/my-defy", commitB, 0xa5)
		folder := filepath.Join(t.TempDir(), "alice")
		u.releases.local[folder] = alice
		u.d.started = func(d *defy) { reportsBuild(d.updater, alice) }
		u.opts.Firmware = folder
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		// The build's manifest names its repository, so the way back to
		// GitHub is that repository, not the default layout.
		wantLines(t, out,
			`Done. From now on, "defy-installer update" installs from `+folder+".",
			"To use your keymap repository again: defy-installer update --from alice/my-defy")
		if s := u.settings; !s.Exists || s.Firmware != folder || !s.Keymap.IsZero() {
			t.Errorf("saved %+v", s)
		}
	})
}

func TestABuildWithoutAManifest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Without a manifest, nothing says whether the Neuron runs the
		// build, so it is written.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		b := testBuild(t, "alice/my-defy", commitB, 0xa5)
		b.Manifest = nil
		folder := filepath.Join(t.TempDir(), "build")
		u.releases.local[folder] = b
		u.opts.Firmware = folder
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out,
			"Found your Defy. It runs ZMK from alice/my-defy, commit 3f2a91c.",
			"Read build: zmk.hex, left-hybrid-radio.bin and right-hybrid-radio.bin, without a manifest. Checked.",
			neuronOnly,
			"Writing the Neuron  [##########################] 100%",
			`Done. From now on, "defy-installer update" installs from `+folder+".",
			"To install from GitHub again: defy-installer update --from default")
	})
}

func TestSwitchingToYourOwnKeymap(t *testing.T) {
	switching := func(t *testing.T) *userTest {
		u, _, newBuild := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		reportsBuild(u.d.updater, newBuild)
		alice := testBuild(t, "alice/my-defy", commitB, 0xa5)
		u.releases.publish("alice/my-defy", "firmware", alice)
		u.d.started = func(d *defy) { reportsBuild(d.updater, alice) }
		u.opts.From = source(t, "alice/my-defy")
		return u
	}
	bubble(t, "saved", func(t *testing.T) {
		u := switching(t)
		u.opts.Terminal = true
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantScreen(t, out,
			"Found your Defy. It runs ZMK v0.2.0 with the default layout.",
			"Your keymap: alice/my-defy, built today from commit 8c04e7d, module v0.2.0. Downloading... checked.",
			neuronOnly,
			studioLost,
			"Writing the Neuron  [##########################] 100%",
			`Done. From now on, "defy-installer update" installs the latest build of alice/my-defy.`,
			"For the default layout again: defy-installer update --from default")
	})
	bubble(t, "a failed save", func(t *testing.T) {
		// The file was broken by hand while the run went on: the failure
		// comes after the done lines.
		u := switching(t)
		if err := os.WriteFile(u.settings.Path, []byte("keymap: [\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := u.run(Install, "")
		wantError(t, err, "The keyboard runs the new firmware, but saving the choice in "+u.settings.Path+" failed")
		if !strings.HasSuffix(out, "Writing the Neuron  [##########################] 100%\nDone.\n") {
			t.Errorf("output:\n%s", out)
		}
	})
}

func TestSavingAPinnedTagOrTheDefault(t *testing.T) {
	bubble(t, "pinned tag", func(t *testing.T) {
		// The keyboard runs v0.1.0, and stays there.
		u, oldBuild, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		u.releases.builds[strings.ToLower(release.DefaultRepo)+"@v0.1.0"] = oldBuild
		u.opts.From, u.opts.Yes = source(t, "default@v0.1.0"), true
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Found your Defy. It runs ZMK v0.1.0 with the default layout. It is up to date.",
			`From now on, "defy-installer update" stays on v0.1.0 of the default layout.`,
			"To follow the latest again: defy-installer update --from default")
		if u.settings.Keymap != source(t, "default@v0.1.0") {
			t.Errorf("saved %+v", u.settings)
		}
	})
	bubble(t, "default", func(t *testing.T) {
		u, _, newBuild := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		reportsBuild(u.d.updater, newBuild)
		u.opts.From, u.opts.Yes = release.Default(), true
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, `From now on, "defy-installer update" installs the latest release of the default layout.`)
		if u.settings.Keymap != release.Default() {
			t.Errorf("saved %+v", u.settings)
		}
	})
}

func TestAFirmwarePathThatIsNotThere(t *testing.T) {
	bubble(t, "saved", func(t *testing.T) {
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		gone := filepath.Join(t.TempDir(), "firmware.zip")
		u.settings.Firmware = gone
		_, err := u.run(Install, "")
		wantError(t, err, gone+", saved in defy-installer.yaml, is not there any more. Give the firmware's new place "+
			"with\n  defy-installer update --firmware <zip or folder>\n"+
			"To use your keymap repository again: defy-installer update --from alice/my-defy")
	})
	bubble(t, "--firmware", func(t *testing.T) {
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		u.opts.Firmware = filepath.Join(t.TempDir(), "firmware.zip")
		_, err := u.run(Install, "")
		wantError(t, err, u.opts.Firmware+" is not there. Check the path, then try again.")
	})
}

func TestTheNeuronRunsABuildOnlyWhenEveryPartMatches(t *testing.T) {
	build := func(module, source, commit string) *release.Manifest {
		return &release.Manifest{Module: module, Source: source, Commit: commit}
	}
	runs := &updater.Version{Module: "v0.2.0", Source: "alice/my-defy", Commit: commitA}
	for _, tt := range []struct {
		name string
		v    *updater.Version
		m    *release.Manifest
		want bool
	}{
		{"the same build", runs, build("v0.2.0", "Alice/My-Defy", commitA), true},
		{"another commit", runs, build("v0.2.0", "alice/my-defy", commitB), false},
		{"another source", runs, build("v0.2.0", "bob/keys", commitA), false},
		{"another module", runs, build("v0.2.1", "alice/my-defy", commitA), false},
		{"a dirty keymap", &updater.Version{Module: "v0.2.0", Source: "alice/my-defy", Commit: commitA + "-dirty"},
			build("v0.2.0", "alice/my-defy", commitA+"-dirty"), false},
		{"a dirty module", &updater.Version{Module: "ea8fc85-dirty", Source: "alice/my-defy", Commit: commitA},
			build("ea8fc85-dirty", "alice/my-defy", commitA), false},
		{"a module outside git", &updater.Version{Module: "unknown", Source: "alice/my-defy", Commit: commitA},
			build("unknown", "alice/my-defy", commitA), false},
		{"a local build", &updater.Version{Module: "v0.2.0", Source: "local", Commit: commitA},
			build("v0.2.0", "local", commitA), false},
		{"a build outside git", &updater.Version{Module: "v0.2.0", Source: "alice/my-defy"},
			build("v0.2.0", "alice/my-defy", ""), false},
		{"firmware without VERSION", nil, build("v0.2.0", "alice/my-defy", commitA), false},
	} {
		if got := neuronRuns(tt.v, tt.m); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}
