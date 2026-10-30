// SPDX-License-Identifier: MIT

package flow

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/updater"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

// TestWirelessWaitAsksForARestart: a half connects only after 3 minutes.
// The message names it when HALF VERSIONS has a Bluetooth record from the
// other half alone.
func TestWirelessWaitAsksForARestart(t *testing.T) {
	for _, tt := range []struct {
		name        string
		left, right ut.Link
		failed      bool
		want        string
	}{
		{"the right half", ut.Bluetooth, ut.NoLink, false, "  The right half has not connected after 2 minutes."},
		{"the left half", ut.NoLink, ut.Bluetooth, false, "  The left half has not connected after 2 minutes."},
		{"HALF VERSIONS fails", ut.Bluetooth, ut.NoLink, true, "  One half has not connected after 2 minutes."},
	} {
		bubble(t, tt.name, func(t *testing.T) {
			u, _ := firstInstall(t)
			f := u.d.updater
			f.Connects = []int{3, 200}
			f.Halves[0].AfterWrite, f.Halves[1].AfterWrite = tt.left, tt.right
			if tt.failed {
				f.Answers = map[updater.Opcode]ut.Answer{updater.OpHalfVersions: {Status: -eio}}
			}
			out, err := u.run(Install, "y\n\n\n")
			if err != nil {
				t.Fatal(err)
			}
			wantLines(t, out,
				"  Waiting for the halves... 1 of 2 connected...",
				tt.want+" Unplug its cable, switch it off,",
				"  wait 5 seconds and switch it on. Press Enter to keep waiting.",
				"  Waiting for the halves... 1 of 2 connected... 2 of 2 connected.",
				firstInstallDone[0])
		})
	}
}

func TestTheImageCheckAfterAHalvesWrite(t *testing.T) {
	bubble(t, "no report", func(t *testing.T) {
		// The halves do not report within 30 seconds.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0x11, 0xa5)
		u.d.updater.Halves[0].AfterWrite, u.d.updater.Halves[1].AfterWrite = ut.NoLink, ut.NoLink
		start := time.Now()
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, writeLines[0], notReported,
			"Done. Keep typing with the cables in, or unplug them and switch the halves on.")
		if waited := time.Since(start); waited < halvesWait {
			t.Errorf("waited %v", waited)
		}
	})
	bubble(t, "no HALF VERSIONS", func(t *testing.T) {
		// Neuron firmware without HALF VERSIONS says nothing of the halves,
		// nor clears Studio's changes.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0x11, 0xa5)
		u.d.started = func(d *defy) { d.updater.Version = nil }
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, writeLines[0],
			"  The changes made in ZMK Studio could not be cleared. Studio's \"Restore Stock Settings\" clears them.",
			"Done. Keep typing with the cables in, or unplug them and switch the halves on.")
		wantNoOutput(t, out, "Both halves run", notReported)
	})
	bubble(t, "unreadable reply", func(t *testing.T) {
		// A reply that cannot be read fails only the check: the run goes on.
		u, _, newBuild := zmkUpdate(t, release.DefaultRepo, 0x11, 0xa5)
		u.d.started = func(d *defy) {
			reportsBuild(d.updater, newBuild)
			d.updater.Answers = map[updater.Opcode]ut.Answer{updater.OpHalfVersions: {Payload: []byte{1}}}
		}
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, writeLines[0], notReported,
			"Done. Keep typing with the cables in, or unplug them and switch the halves on.")
	})
}

func TestWirelessOnFirmwareThatCannotSay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _ := firstInstall(t)
		u.d.updater.Connects = nil
		out, err := u.run(Install, "y\n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "  Waiting for the halves... this Neuron firmware cannot say when they connect.",
			"  Once both halves light cyan, check that typing works.", "", firstInstallDone[0])
	})
}
