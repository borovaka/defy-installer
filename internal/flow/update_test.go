// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/updater"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

// What the pairing step says about the halves.
var wirelessSteps = []string{
	"  Keep the cables in while each half pairs with the Neuron over Bluetooth.",
	"  A half that installs its radio lights yellow and orange first. Each lights cyan once paired.",
}

func TestAnUnpairedNeuronGetsItsHalvesWritten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The Neuron runs the build and the halves report it, but the
		// Neuron is not paired with both: they cannot type. An asleep half
		// cannot report over Bluetooth then, so none is woken.
		u, _, newBuild := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		reportsBuild(u.d.updater, newBuild)
		asleep(u.d.updater, firmware.Right, -1)
		u.d.updater.Pairing = []byte{1, 2, 3, 0}
		u.d.updater.Connects = []int{1, 1}
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, slices.Concat([]string{
			"Found your Defy. It runs ZMK from alice/my-defy, commit 8c04e7d.",
			"The Neuron is not paired with both halves, so both halves are written and paired with it again, which " +
				"clears the halves' Bluetooth settings.",
			"",
			"Step 1 of 3: get ready",
			"",
			"Step 2 of 3: writing, about 2 minutes. Do not unplug anything.",
			writeLines[1], writeLines[2],
			"  Both halves run the new firmware.",
			"",
			"Step 3 of 3: pairing",
		}, wirelessSteps, []string{
			"  Waiting for the halves... 2 of 2 connected.",
			"",
			"Done. Your keyboard runs the new firmware.",
		})...)
		wantNoOutput(t, out, "asleep", "Studio", "Writing the Neuron")
		if u.d.updater.Count(updater.OpResetPairing) != 1 || u.d.updater.Count(updater.OpStart) != 2 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
}

// settingsImage is a Neuron image that reaches into the ZMK settings.
func settingsImage(t *testing.T) *firmware.NeuronImage {
	t.Helper()
	im, err := firmware.ParseNeuronImage("full.hex", ft.Hex(ft.At(0x1000, ft.NeuronProgram(0x1000, 3, 0x1000)),
		ft.At(firmware.NeuronSettingsStart, make([]byte, 16))))
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestUpdateRefusesAnImageThatReachesTheSettings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _, newBuild := zmkUpdate(t, "alice/my-defy", 0x11, 0xa5)
		newBuild.Neuron = settingsImage(t)
		_, err := u.run(Install, "\n")
		wantError(t, err, "This build's Neuron image reaches into the ZMK settings, so an update cannot keep them.\n"+
			"Write it with defy-installer advanced neuron write, without --keep-settings, which clears them.")
		if slices.Contains(u.d.opened, bootPort.Path) || u.d.updater.Count(updater.OpOpen) > 0 {
			t.Errorf("opened %q and ran %v", u.d.opened, u.d.updater.Executed)
		}
	})
}

func TestUpdateWhenZMKDoesNotComeBack(t *testing.T) {
	bubble(t, "Neuron only", func(t *testing.T) {
		// The bootloader stays after S#, as one entered with the button
		// does: nothing is cleared, and the run says to plug the Neuron in
		// again.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		u.d.after = "boot"
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Writing the Neuron  [##########################] 100%",
			"The Neuron is written, but ZMK did not start within 10 seconds. Unplug the Neuron and plug it back in.",
			"Changes made in ZMK Studio may still be in place; Studio's \"Restore Stock Settings\" clears them.",
			"Done.")
		if u.d.updater.Count(updater.OpClearKeymap) > 0 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
	bubble(t, "after the halves", func(t *testing.T) {
		// The halves are written through their cables, but nothing types
		// until ZMK starts, so the run does not say it is done.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0x11, 0xa5)
		u.d.after = "boot"
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Step 2 of 2: writing, about 2 minutes. Do not unplug anything.",
			writeLines[1], writeLines[2], writeLines[0],
			"  The Neuron is written, but ZMK did not start within 10 seconds. Unplug the Neuron and plug it back in.",
			"",
			"Once ZMK starts, keep typing with the cables in, or unplug them and switch the halves on.")
		wantNoOutput(t, out, "Done.", "Both halves run the new firmware")
		if u.d.updater.Count(updater.OpClearKeymap) > 0 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
	bubble(t, "after a pairing reset", func(t *testing.T) {
		// After a pairing reset, the halves' steps come without a wait for
		// them, since nothing can say when they connect.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0x11, 0xa5)
		u.d.updater.Pairing = []byte{1, 2, 3, 0}
		u.d.after = "boot"
		start := time.Now()
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, slices.Concat([]string{
			writeLines[0],
			"  The Neuron is written, but ZMK did not start within 10 seconds. Unplug the Neuron and plug it back in.",
			"",
			"Step 3 of 3: pairing",
		}, wirelessSteps, []string{
			"",
			"Done. Your keyboard runs the new firmware.",
		})...)
		wantNoOutput(t, out, "Waiting for the halves", "Both halves run the new firmware")
		if u.d.updater.Count(updater.OpSplitStatus) > 0 || time.Since(start) > 2*DefaultNeuronTimeout {
			t.Errorf("asked SPLIT STATUS %d times, in %v", u.d.updater.Count(updater.OpSplitStatus), time.Since(start))
		}
	})
}

func TestTheNeuronMayListOnePortAtFirst(t *testing.T) {
	// whenLine runs f once the output shows line.
	whenLine := func(u *userTest, line string, f func()) Env {
		env := u.d.env()
		env.Out = &cancelOn{line: line, cancel: f}
		return env
	}
	bubble(t, "for a moment", func(t *testing.T) {
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		env := whenLine(u, studioLost, func() {
			u.d.mu.Lock()
			u.d.onePort = 3
			u.d.mu.Unlock()
		})
		u.opts.In = strings.NewReader("")
		if err := Install(t.Context(), env, u.opts); err != nil {
			t.Fatal(err)
		}
		out := env.Out.(*cancelOn).String()
		wantLines(t, out, "Writing the Neuron  [##########################] 100%", "Done.")
		if !slices.Contains(u.d.opened, bootPort.Path) {
			t.Errorf("the Neuron was not written:\n%s", out)
		}
	})
	bubble(t, "for too long", func(t *testing.T) {
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		env := whenLine(u, studioLost, func() {
			u.d.mu.Lock()
			u.d.onePort = 1000
			u.d.mu.Unlock()
		})
		u.opts.In = strings.NewReader("")
		start := time.Now()
		err := Install(t.Context(), env, u.opts)
		wantError(t, err, "The Neuron still shows only one of its two ports after 5 seconds, so it was not written. "+
			"Wait a few seconds, then run\n  defy-installer update --from alice/my-defy\nagain.")
		if waited := time.Since(start); waited < onePortWait || slices.Contains(u.d.opened, bootPort.Path) {
			t.Errorf("waited %v; opened %q", waited, u.d.opened)
		}
	})
}

func TestWhenBothPorts(t *testing.T) {
	// Other errors come back at once, since a write may have begun.
	other := errors.New("no bootloader")
	calls := 0
	if err := whenBothPorts(t.Context(), func() error { calls++; return other }); err != other || calls != 1 {
		t.Errorf("got %v after %d calls", err, calls)
	}
	// An interrupt stops the wait.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := whenBothPorts(ctx, func() error { return neuron.ErrOnePort }); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

// zmkUpdate is a keyboard running ZMK from source at commitA with halves
// that run oldBuild's packages over Bluetooth, and newBuild published as
// source's latest release.
func zmkUpdate(t *testing.T, source string, oldFill, newFill byte) (u *userTest, oldBuild, newBuild *release.Build) {
	u = newUserTest(t, "zmk", "update")
	oldBuild = testBuild(t, source, commitA, oldFill)
	newBuild = testBuild(t, source, commitB, newFill)
	tag := "firmware"
	if source == release.DefaultRepo {
		oldBuild.Manifest.Module = "v0.1.0"
		tag = "v0.2.0"
	}
	reportsBuild(u.d.updater, oldBuild)
	runs(u.d.updater, oldBuild, 2)
	u.releases.publish(source, tag, newBuild)
	u.d.started = func(d *defy) { reportsBuild(d.updater, newBuild) }
	return u, oldBuild, newBuild
}

func TestUpdateWritesOnlyTheNeuron(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A keymap change: the halves already run the build's images.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		u.opts.Terminal = true
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantScreen(t, out,
			"Found your Defy. It runs ZMK from alice/my-defy, commit 3f2a91c.",
			"The latest build of alice/my-defy is commit 8c04e7d, built today. Downloading... checked.",
			neuronOnly,
			studioLost,
			"Writing the Neuron  [##########################] 100%",
			"Done.")
		if u.d.updater.Count(updater.OpOpen) > 0 || u.d.updater.Count(updater.OpClearKeymap) != 1 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
		// An update keeps the ZMK settings: the erase stops where they
		// start.
		wantErase(t, u.d.boot, firmware.NeuronSettingsStart)
	})
}

func TestUpdateWritesTheHalvesFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0x11, 0xa5)
		u.opts.Terminal = true
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantScreen(t, out,
			"Found your Defy. It runs ZMK v0.1.0 with the default layout.",
			"ZMK v0.2.0 is out. Downloading it from GitHub... checked.",
			"The halves need new firmware too.",
			studioLost,
			"",
			"Step 1 of 2: get ready",
			"  Plug both halves into the Neuron with their cables, and switch both halves off.",
			"  Press Enter when ready.",
			"",
			"Step 2 of 2: writing, about 2 minutes. Do not unplug anything.",
			writeLines[1], writeLines[2], writeLines[0],
			"  Both halves run the new firmware.",
			"",
			"Done. Keep typing with the cables in, or unplug them and switch the halves on.")
		if u.d.updater.Count(updater.OpResetPairing) > 0 || u.d.updater.Count(updater.OpClearKeymap) != 1 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
}

func TestUpdateWakesAnAsleepHalf(t *testing.T) {
	bubble(t, "one asleep", func(t *testing.T) {
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		asleep(u.d.updater, firmware.Right, 4)
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out,
			"The latest build of alice/my-defy is commit 8c04e7d, built today. Downloading... checked.",
			"The right half is asleep. Press Shift on it to wake it.",
			"Waiting... the right half runs this build's image.",
			neuronOnly,
			"Done.")
		if u.d.updater.Count(updater.OpOpen) > 0 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
	bubble(t, "both asleep", func(t *testing.T) {
		// Both asleep, and one wakes with other firmware.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		asleep(u.d.updater, firmware.Left, 3)
		asleep(u.d.updater, firmware.Right, 5)
		u.d.updater.Halves[1].CRC = 1
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Both halves are asleep. Press Shift on each to wake them.",
			"Waiting... the left half runs this build's image. The right half runs other firmware.",
			"The halves need new firmware too.")
	})
}

func TestUpdateChecksSilentHalvesThroughTheCables(t *testing.T) {
	bubble(t, "images current", func(t *testing.T) {
		// The halves never report, and PROBE finds the build's images:
		// the session closes without writing them.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		asleep(u.d.updater, firmware.Left, -1)
		asleep(u.d.updater, firmware.Right, -1)
		start := time.Now()
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(start) < halvesWait {
			t.Errorf("waited %v for the halves", time.Since(start))
		}
		wantLines(t, out,
			"Both halves are asleep. Press Shift on each to wake them.",
			"Waiting... no answer after 30 seconds.",
			"The halves did not say what they run, so the installer checks them through their cables.",
			studioLost,
			"Step 1 of 2: get ready",
			"Step 2 of 2: writing, about 2 minutes. Do not unplug anything.",
			"  Both halves already run this build's images.",
			writeLines[0],
			"Done. Keep typing with the cables in, or unplug them and switch the halves on.")
		if u.d.updater.Count(updater.OpProbe) != 2 || u.d.updater.Count(updater.OpStart) > 0 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
	bubble(t, "other images", func(t *testing.T) {
		// PROBE finds other images, and both halves are written.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0x11, 0xa5)
		asleep(u.d.updater, firmware.Left, -1)
		asleep(u.d.updater, firmware.Right, -1)
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, writeLines[1], writeLines[2], writeLines[0], "  Both halves run the new firmware.")
		if u.d.updater.Count(updater.OpStart) != 2 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
}

func TestUpdateTriesTheHalvesAgain(t *testing.T) {
	for _, tt := range []struct {
		name string
		open int16 // OPEN's refusal, or 0 for a half that does not answer PROBE
		want string
	}{
		{"a silent half", 0, "  The left half does not answer. Check that its cable is in the Neuron and the half is " +
			"switched off."},
		{"Neuron busy", -ebusy, "  The Neuron is still busy with the halves. Wait a few seconds."},
		{"OPEN refused", -eio, "  The halves do not answer. Check that both cables are in the Neuron and both halves " +
			"are switched off."},
	} {
		bubble(t, tt.name, func(t *testing.T) {
			// The first attempt fails, and the next one opens.
			u, _, _ := zmkUpdate(t, release.DefaultRepo, 0x11, 0xa5)
			f := u.d.updater
			if tt.open == 0 {
				f.Halves[0].ProbeFails = 1
			} else {
				f.Reject[updater.OpOpen] = tt.open
				f.Filter = func(r ut.Request[updater.Opcode], reply []byte) []byte {
					if r.Op == updater.OpOpen {
						delete(f.Reject, updater.OpOpen)
					}
					return reply
				}
			}
			out, err := u.run(Install, "\n\n")
			if err != nil {
				t.Fatal(err)
			}
			wantLines(t, out, "Step 2 of 2: writing, about 2 minutes. Do not unplug anything.", tt.want,
				"  Press Enter to try again.", writeLines[1], writeLines[2], writeLines[0])
			if f.Count(updater.OpOpen) != 2 {
				t.Errorf("ran %v", f.Executed)
			}
		})
	}
}

func TestUpToDate(t *testing.T) {
	upToDate := func(t *testing.T) *userTest {
		u, _, newBuild := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		reportsBuild(u.d.updater, newBuild)
		return u
	}
	bubble(t, "asked", func(t *testing.T) {
		u := upToDate(t)
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		if want := "Found your Defy. It runs ZMK v0.2.0 with the default layout. It is up to date.\n" +
			"Write it again anyway? [y/N] \n"; out != want {
			t.Errorf("got\n%s\nwant\n%s", out, want)
		}
		if len(u.releases.downloads) > 0 || slices.Contains(u.d.opened, bootPort.Path) || u.d.updater.Count(updater.OpOpen) > 0 {
			t.Errorf("downloaded %q, opened %q", u.releases.downloads, u.d.opened)
		}
	})
	bubble(t, "--yes", func(t *testing.T) {
		// --yes does not write it again.
		u := upToDate(t)
		u.opts.Yes = true
		out, err := u.run(Install, "")
		if err != nil || strings.Contains(out, "again") || len(u.releases.downloads) > 0 {
			t.Errorf("got %v:\n%s", err, out)
		}
	})
	bubble(t, "written again", func(t *testing.T) {
		// Written again: both halves, whatever PROBE finds, and the
		// Neuron.
		u := upToDate(t)
		out, err := u.run(Install, "y\n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Write it again anyway? [y/N] y", "Downloading it from GitHub... checked.",
			studioLost,
			"Step 1 of 2: get ready", writeLines[1], writeLines[2], writeLines[0], "  Both halves run the new firmware.")
		if u.d.updater.Count(updater.OpStart) != 2 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
}

func TestStudioChanges(t *testing.T) {
	for _, tt := range []struct {
		name  string
		clear []int16
		input string
		want  string
	}{
		{"Studio unlocked", []int16{-ebusy, 0}, "\n", "ZMK Studio is unlocked, so its changes cannot be cleared. " +
			"Close or lock Studio, then press Enter to try again."},
		// Any other refusal comes after the write, so the run goes on.
		{"another refusal", []int16{-eio}, "", "The changes made in ZMK Studio could not be cleared. Studio's " +
			"\"Restore Stock Settings\" clears them."},
	} {
		bubble(t, tt.name, func(t *testing.T) {
			u, _, newBuild := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
			u.d.started = func(d *defy) {
				reportsBuild(d.updater, newBuild)
				d.updater.Clear = tt.clear
			}
			out, err := u.run(Install, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			wantLines(t, out, "Writing the Neuron  [##########################] 100%", tt.want, "Done.")
		})
	}
}

func TestOlderFirmwareGetsEverythingWritten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Without VERSION, HALF VERSIONS or PAIRING: both halves and the
		// Neuron are written, and the pairing is left as it is.
		u, _, newBuild := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		u.d.updater.Version, u.d.updater.Pairing = nil, nil
		u.d.started = func(d *defy) { reportsBuild(d.updater, newBuild) }
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out,
			"Found your Defy. It runs ZMK.",
			"Its firmware is too old to say what it runs, so the whole keyboard is written.",
			"The default layout: ZMK v0.2.0. Downloading it from GitHub... checked.",
			studioLost,
			"Step 1 of 2: get ready",
			writeLines[1], writeLines[2], writeLines[0],
			"  Both halves run the new firmware.",
			"Done. Keep typing with the cables in, or unplug them and switch the halves on.")
		if u.d.updater.Count(updater.OpStart) != 2 || u.d.updater.Count(updater.OpResetPairing) > 0 {
			t.Errorf("ran %v", u.d.updater.Executed)
		}
	})
}

func TestInstallOnZMKUpdates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		u.opts.Command = "install"
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Found your Defy. It runs ZMK from alice/my-defy, commit 3f2a91c.",
			"ZMK is already installed, so this updates it and keeps your Bluetooth pairings.")
	})
}

func TestUpToDateOnceAnAsleepHalfReports(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _, newBuild := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		reportsBuild(u.d.updater, newBuild)
		asleep(u.d.updater, firmware.Left, 2)
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		want := "Found your Defy. It runs ZMK from alice/my-defy, commit 8c04e7d.\n" +
			"The left half is asleep. Press Shift on it to wake it.\n" +
			"Waiting... the left half runs this build's image.\n" +
			"It is up to date.\n" +
			"Write it again anyway? [y/N] \n"
		if out != want || len(u.releases.downloads) > 0 || slices.Contains(u.d.opened, bootPort.Path) {
			t.Errorf("got\n%s\nwant\n%s", out, want)
		}
	})
}
