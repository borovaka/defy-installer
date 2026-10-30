// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/updater"
)

// showsDrive makes u's Neuron show its UF2 loader's drive. The first start
// from the bootloader brings back first, "zmk", "loader" or "boot"; each
// start after it brings back then, running what the test set up for a
// start.
func showsDrive(u *userTest, first, then string) {
	u.d.setMode("loader")
	started := u.d.started
	u.d.after = first
	u.d.started = func(d *defy) { d.after, d.started = then, started }
}

// The lines a run from the drive starts with.
var fromTheDrive = []string{
	"Found your Defy. Its Neuron is waiting for a UF2 file on its DEFY drive.",
	"Starting ZMK on the Neuron...",
}

// driveCameBack is said when the drive comes back after the start.
const driveCameBack = "The DEFY drive came back, so ZMK on the Neuron is incomplete or does not start."

// wantLoaderTouches fails unless the loader's port was opened n times, and
// the last open touched it for the bootloader.
func wantLoaderTouches(t *testing.T, d *defy, n int) {
	t.Helper()
	opens := 0
	for _, path := range d.opened {
		if path == loaderPort.Path {
			opens++
		}
	}
	if want := []string{"DTR on", "1200 baud", "DTR off", "close"}; opens != n || !slices.Equal(d.loader.Lines, want) {
		t.Errorf("opened the loader's port %d times, want %d; its line went %q", opens, n, d.loader.Lines)
	}
}

func TestUpdateFromTheDriveWhenZMKStarts(t *testing.T) {
	bubble(t, "a keymap change", func(t *testing.T) {
		// ZMK starts, and the update goes on as for a Neuron found running
		// it: the Neuron alone, keeping its settings.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		showsDrive(u, "zmk", "zmk")
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, append(slices.Clone(fromTheDrive),
			"ZMK started. It runs ZMK from alice/my-defy, commit 3f2a91c.", "Done.")...)
		wantLoaderTouches(t, u.d, 1)
		wantErase(t, u.d.boot, firmware.NeuronSettingsStart)
		if u.d.updater.Count(updater.OpOpen) > 0 || u.d.updater.Count(updater.OpClearKeymap) != 1 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
	bubble(t, "up to date, --yes", func(t *testing.T) {
		// --yes answers no question, and writes nothing that is up to
		// date: the start sent only the identification and S#.
		u, _, newBuild := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		reportsBuild(u.d.updater, newBuild)
		showsDrive(u, "zmk", "zmk")
		u.opts.Yes = true
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		if want := "Found your Defy. Its Neuron is waiting for a UF2 file on its DEFY drive.\n" +
			"Starting ZMK on the Neuron...\n" +
			"ZMK started. It runs ZMK v0.2.0 with the default layout. It is up to date.\n"; out != want {
			t.Errorf("got\n%s\nwant\n%s", out, want)
		}
		if len(u.d.boot.Commands) != 2 || u.d.boot.Commands[1] != "S#" || len(u.releases.downloads) > 0 {
			t.Errorf("sent %q, downloaded %q", u.d.boot.Commands, u.releases.downloads)
		}
	})
}

func TestUpdateFromTheDriveWhenTheDriveComesBack(t *testing.T) {
	bubble(t, "halves current", func(t *testing.T) {
		// Nothing reports what the Neuron ran, so the default layout is
		// written, keeping the settings. The halves then connect again
		// over Bluetooth and report the build's images: no cables, and no
		// Enter. --yes asks for nothing more.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		asleep(u.d.updater, firmware.Left, 2)
		asleep(u.d.updater, firmware.Right, 3)
		showsDrive(u, "loader", "zmk")
		u.opts.Terminal, u.opts.Yes = true, true
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantScreen(t, out, append(slices.Clone(fromTheDrive),
			driveCameBack,
			"This installs ZMK v0.2.0 with the default layout. Downloading it from GitHub... checked.",
			studioLost,
			"Writing the Neuron  [##########################] 100%",
			"If a half is asleep, press Shift on it to wake it.",
			"Waiting... the left half runs this build's image. The right half runs this build's image.",
			"Both halves already run this build's images.",
			"Done.")...)
		wantLoaderTouches(t, u.d, 2)
		wantErase(t, u.d.boot, firmware.NeuronSettingsStart)
		if u.d.updater.Count(updater.OpOpen) > 0 || u.d.updater.Count(updater.OpClearKeymap) != 1 || u.settings.Exists {
			t.Errorf("ran %v; saved the settings: %v", u.d.updater.Executed, u.settings.Exists)
		}
	})
	bubble(t, "halves differ", func(t *testing.T) {
		// The halves report other images: they are written through their
		// cables, keeping the pairing, as an update writes them.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0x11, 0xa5)
		showsDrive(u, "loader", "zmk")
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, slices.Concat(fromTheDrive, []string{
			driveCameBack,
			"Writing the Neuron  [##########################] 100%",
			"The halves need new firmware too.",
			"",
			"Step 1 of 2: get ready",
			"  Plug both halves into the Neuron with their cables, and switch both halves off.",
			"  Press Enter when ready.",
			"",
			"Step 2 of 2: writing, about 2 minutes. Do not unplug anything.",
			writeLines[1], writeLines[2],
			"  Both halves run the new firmware.",
			"",
			"Done. Keep typing with the cables in, or unplug them and switch the halves on.",
		})...)
		wantNoOutput(t, out, "asleep", "Step 3")
		if u.d.updater.Count(updater.OpStart) != 2 || u.d.updater.Count(updater.OpResetPairing) > 0 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
		wantErase(t, u.d.boot, firmware.NeuronSettingsStart)
	})
	bubble(t, "your keymap", func(t *testing.T) {
		// The keymap saved in the settings file is the build written.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		u.settings.Keymap = source(t, "alice/my-defy")
		showsDrive(u, "loader", "zmk")
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, driveCameBack,
			"This installs ZMK from alice/my-defy, built today from commit 8c04e7d, module v0.2.0. Downloading... checked.",
			"Both halves already run this build's images.", "Done.")
	})
	bubble(t, "ZMK does not start after the write", func(t *testing.T) {
		// The bootloader stays after the write's S#: the halves wait for
		// the next run.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		showsDrive(u, "loader", "boot")
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Writing the Neuron  [##########################] 100%",
			"The Neuron is written, but ZMK did not start within 10 seconds. Unplug the Neuron and plug it back in.",
			"Changes made in ZMK Studio may still be in place; Studio's \"Restore Stock Settings\" clears them.",
			"Once ZMK starts, run",
			"  defy-installer update --from default",
			"again to check the halves. If the DEFY drive comes back instead, this build does not start on the "+
				"Neuron. Install another with --from or --firmware.")
		wantNoOutput(t, out, "Done.")
	})
	bubble(t, "an image that reaches the settings", func(t *testing.T) {
		// It is refused before anything is erased.
		u, _, newBuild := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		newBuild.Neuron = settingsImage(t)
		showsDrive(u, "loader", "zmk")
		_, err := u.run(Install, "")
		wantError(t, err, "This build's Neuron image reaches into the ZMK settings, so an update cannot keep them.")
		if len(u.d.boot.Commands) != 2 || u.d.boot.Commands[1] != "S#" {
			t.Errorf("sent %q", u.d.boot.Commands)
		}
	})
}

func TestUpdateFromTheDriveWhenTheStartDoesNotFinish(t *testing.T) {
	bubble(t, "nothing comes back", func(t *testing.T) {
		// The bootloader stays after S#, as one entered with the button
		// does.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		showsDrive(u, "boot", "boot")
		start := time.Now()
		out, err := u.run(Install, "")
		wantLines(t, out, fromTheDrive...)
		wantError(t, err, "Neither ZMK nor the DEFY drive came back within 30 seconds, so nothing was written. "+
			"Unplug the Neuron and plug it back in, then run\n  defy-installer update\nagain.")
		if time.Since(start) < driveStartWait || len(u.d.boot.Commands) != 2 || len(u.releases.downloads) > 0 {
			t.Errorf("sent %q after %v", u.d.boot.Commands, time.Since(start))
		}
	})
	bubble(t, "stopped in the bootloader", func(t *testing.T) {
		// Stopped once the Neuron has entered its bootloader: a replug
		// sends it back to the loader, where a run keeps its settings.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		showsDrive(u, "zmk", "zmk")
		u.opts.Verbose = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		env := u.d.env()
		env.Out = &cancelOn{line: "Entered the Neuron's bootloader from its UF2 loader", cancel: cancel}
		u.opts.In = strings.NewReader("")
		err := Install(ctx, env, u.opts)
		wantError(t, err, "Stopped. Nothing was written. Unplug the Neuron and plug it back in, then run\n"+
			"  defy-installer update\nagain.")
		if u.d.boot == nil || u.d.boot.Started || len(u.releases.downloads) > 0 {
			t.Errorf("the bootloader started the firmware, or %q downloaded", u.releases.downloads)
		}
	})
}
