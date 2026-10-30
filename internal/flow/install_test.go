// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	nt "github.com/borovaka/defy-installer/internal/neuron/neurontest"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/updater"
)

// The lines of a first install's end.
var firstInstallDone = []string{
	"Done. Your keyboard runs ZMK.",
	"Keep typing with the cables in, or unplug them and switch the halves on.",
	"Pair your computers over Bluetooth again: ZMK keeps its own pairings.",
	"To try layout changes, open https://zmk.studio with the Neuron plugged in by USB.",
	"Studio changes last until the next update. For a lasting layout, make your own keymap repository.",
}

// The lines of a three-image write step.
var writeLines = []string{
	"  Writing the Neuron      [##########################] 100%",
	"  Writing the left half   [##########################] 100%",
	"  Writing the right half  [##########################] 100%",
}

const escLine = "  Press the Esc key on the left half once, the top-left key. Dygma's firmware asks for it before an update."

// firstInstall is a keyboard on Dygma's firmware, with the default
// layout's v0.2.0 published, whose halves connect over Bluetooth once
// written.
func firstInstall(t *testing.T) (*userTest, *release.Build) {
	u := newUserTest(t, "stock", "install")
	b := testBuild(t, release.DefaultRepo, commitB, 0xa5)
	u.releases.publish(release.DefaultRepo, "v0.2.0", b)
	u.d.updater.Pairing = []byte{1, 2, 0, 0}
	u.d.updater.Connects = []int{2, 4}
	u.d.started = func(d *defy) { reportsBuild(d.updater, b) }
	return u, b
}

func TestFirstInstall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The screen agreed for a first install.
		u, _ := firstInstall(t)
		u.opts.Terminal = true
		out, err := u.run(Install, "y\n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantScreen(t, out, slices.Concat([]string{
			"Found your Defy. It runs Dygma's firmware.",
			"This installs ZMK v0.2.0 with the default layout. Downloading it from GitHub... checked.",
			"",
			"Installing ZMK erases the layout stored on the keyboard.",
			"Back it up in Bazecor first, then quit Bazecor.",
			"Have you backed up your layout? [y/N] y",
			"",
			"Step 1 of 3: get ready",
			"  Plug both halves into the Neuron with their cables, and switch both halves off.",
			"  Keep the Neuron plugged into this computer.",
			"  Press Enter when ready.",
			"",
			"Step 2 of 3: writing, about 2 minutes. Do not unplug anything.",
		}, writeLines, []string{
			"",
			"Step 3 of 3: pairing",
			"  Keep the cables in while each half pairs with the Neuron over Bluetooth.",
			"  A half that installs its radio lights yellow and orange first. Each lights cyan once paired.",
			"  Waiting for the halves... 1 of 2 connected... 2 of 2 connected.",
			"",
		}, firstInstallDone)...)
		// The new Neuron is not paired with the halves, so their write
		// reset the pairing; the wait for them counts SPLIT STATUS.
		if u.d.updater.Count(updater.OpResetPairing) != 1 || u.d.updater.Count(updater.OpStart) != 2 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
		if u.settings.Exists {
			t.Error("a run without --from or --firmware saved the settings")
		}
		// A first install clears the ZMK settings: the erase reaches their
		// end.
		wantErase(t, u.d.boot, firmware.NeuronSettingsEnd)
	})
}

// wantErase fails unless the bootloader's last session erased from 0x1000
// to end, once, and was told to start the firmware.
func wantErase(t *testing.T, boot *nt.Bootloader, end uint32) {
	t.Helper()
	want := fmt.Sprintf("E%08X,%08X#", firmware.NeuronCodeStart, end-firmware.NeuronCodeStart)
	var erases []string
	for _, c := range boot.Commands {
		if strings.HasPrefix(c, "E") {
			erases = append(erases, c)
		}
	}
	if !slices.Equal(erases, []string{want}) || !boot.Started {
		t.Errorf("erased %q and started %v, want %q", erases, boot.Started, want)
	}
}

func TestFirstInstallWaitsForEsc(t *testing.T) {
	bubble(t, "Esc seen", func(t *testing.T) {
		// The left half is cabled with Dygma's firmware: the Esc line comes
		// once, when the updater first is not ready.
		u, _ := firstInstall(t)
		u.d.stock.ReadyAfter = 3
		out, err := u.run(Install, "y\n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Step 2 of 3: writing, about 2 minutes. Do not unplug anything.", escLine, writeLines[0])
		if n := strings.Count(out, "Esc"); n != 1 {
			t.Errorf("Esc said %d times:\n%s", n, out)
		}
	})
	bubble(t, "Esc not seen", func(t *testing.T) {
		// The key is not seen within 2 minutes: Enter, then the updater is
		// asked again, without a new upgrade.start.
		u, _ := firstInstall(t)
		u.d.stock.ReadyAfter = 600 // polled every 250 ms
		start := time.Now()
		out, err := u.run(Install, "y\n\n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, escLine,
			"  Dygma's firmware did not see the Esc key. Press Esc on the left half, then press Enter.",
			writeLines[0])
		if n := strings.Count(out, "did not see the Esc key"); n != 1 {
			t.Errorf("said %d times that the key was not seen", n)
		}
		if n := strings.Count(strings.Join(u.d.stock.Commands, "\n"), "upgrade.start"); n != 1 {
			t.Errorf("sent upgrade.start %d times", n)
		}
		if waited := time.Since(start); waited < escLimit {
			t.Errorf("waited %v", waited)
		}
	})
}

func TestFirstInstallNeedsTheBackupAnswer(t *testing.T) {
	bubble(t, "answered no", func(t *testing.T) {
		u, _ := firstInstall(t)
		out, err := u.run(Install, "n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Have you backed up your layout? [y/N] n",
			"Nothing was written. Back up your layout in Bazecor, then run", "  defy-installer install --from default",
			"again.")
		if len(u.d.opened) > 0 {
			t.Errorf("opened %q", u.d.opened)
		}
	})
	bubble(t, "--yes without Enter", func(t *testing.T) {
		// --yes answers the question, and still waits for Enter: without
		// one, the run stops before writing.
		u, _ := firstInstall(t)
		u.opts.Yes = true
		out, err := u.run(Install, "")
		wantLines(t, out, "Have you backed up your layout? [y/N] y", "  Press Enter when ready.")
		if !errors.Is(err, errNoInput) || len(u.d.opened) > 0 {
			t.Errorf("got %v after opening %q", err, u.d.opened)
		}
		wantError(t, err, "Run it again in a terminal.")
	})
}

// A first install stopped once ZMK runs on the Neuron again has only the
// halves left to write, and says so.
func TestFirstInstallStoppedAfterTheNeuron(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _ := firstInstall(t)
		u.opts.Verbose = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		env := u.d.env()
		env.Out = &cancelOn{line: "The firmware is back at", cancel: cancel}
		u.opts.In = strings.NewReader("y\n\n")
		err := Install(ctx, env, u.opts)
		wantError(t, err, "Stopped. A half may hold part of its firmware. Keep both halves cabled to the Neuron "+
			"and switched off, and run\n  defy-installer install --from default\nagain to write them.")
	})
}

func TestUpdateOnDygmasFirmwareInstalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _ := firstInstall(t)
		u.opts.Command = "update"
		out, err := u.run(Install, "y\n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Found your Defy. It runs Dygma's firmware.", "ZMK is not installed yet, so this installs it.")
	})
}

func TestBootloaderRecovery(t *testing.T) {
	bubble(t, "ZMK starts", func(t *testing.T) {
		u, _ := firstInstall(t)
		u.d.setMode("boot")
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, slices.Concat([]string{
			"Found your Defy. Its Neuron is waiting in its bootloader, after a write that did not finish.",
			"This installs ZMK v0.2.0 with the default layout. Downloading it from GitHub... checked.",
			"This writes the Neuron again, then both halves. Writing the Neuron clears its Bluetooth pairings and " +
				"any changes made in ZMK Studio.",
			"",
			"Step 1 of 3: get ready",
		}, writeLines, []string{"Step 3 of 3: pairing"}, firstInstallDone)...)
		if strings.Contains(out, "backed up") || u.d.updater.Count(updater.OpResetPairing) != 1 {
			t.Errorf("ran %v:\n%s", u.d.updater.Executed, out)
		}
		// What the settings area holds is not known, so it is cleared.
		wantErase(t, u.d.boot, firmware.NeuronSettingsEnd)
	})
	bubble(t, "the bootloader stays", func(t *testing.T) {
		// A bootloader entered with the button stays after S#: the halves
		// wait for the next run.
		u, _ := firstInstall(t)
		u.d.setMode("boot")
		u.d.after = "boot"
		out, err := u.run(Install, "\n")
		wantError(t, err, "The Neuron is written, but ZMK did not start within 10 seconds. Unplug the Neuron and "+
			"plug it back in without holding its button, then run\n  defy-installer install --from default\nagain to "+
			"write the halves.")
		if strings.Contains(out, "half  [") {
			t.Errorf("the halves were written:\n%s", out)
		}
	})
}
