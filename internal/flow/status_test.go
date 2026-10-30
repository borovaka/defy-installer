// SPDX-License-Identifier: MIT

package flow

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/updater"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

func TestStatus(t *testing.T) {
	bubble(t, "ZMK", func(t *testing.T) {
		u, _, newBuild := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		u.opts.Command = "status"
		reportsBuild(u.d.updater, newBuild)
		// Its last record came by Bluetooth, but the battery sample shows
		// the cable is up, and keys go over it.
		u.d.updater.Halves[1].Link = ut.Bluetooth
		// The left half is on Bluetooth at 80%, and the right on both
		// links, as a cabled half is, charging.
		u.d.updater.Battery = ut.Snapshot(48, 60000, ut.BatteryRecord(0, 0x0f, 2, 80, ut.BatteryFrame(0, 0)),
			ut.BatteryRecord(1, 0x07, 3, 255, ut.BatteryFrame(1, 0x10)))
		out, err := u.run(Status, "")
		if err != nil {
			t.Fatal(err)
		}
		want := "Neuron       ZMK v0.2.0, keymap alice/my-defy at 8c04e7d, connected by USB\n" +
			"Left half    ZMK v0.2.0, connected wirelessly, battery 80%\n" +
			"Right half   ZMK v0.2.0, connected by cable, charging\n" +
			"The latest build of alice/my-defy is 8c04e7d: up to date.\n" +
			"Settings: " + u.settings.Path + ", not created yet\n"
		if out != want {
			t.Errorf("got\n%s\nwant\n%s", out, want)
		}
		if slices.Contains(u.d.opened, bootPort.Path) || u.d.updater.Count(updater.OpOpen) > 0 {
			t.Errorf("opened %q and ran %v", u.d.opened, u.d.updater.Executed)
		}
	})
	bubble(t, "no Neuron", func(t *testing.T) {
		// Without a Neuron or the network.
		u := newUserTest(t, "", "status")
		u.releases.err = errNoNetwork
		out, err := u.run(Status, "")
		if err != nil {
			t.Fatal(err)
		}
		want := "Neuron       not found. Plug it into this computer with its USB cable.\n" +
			"Settings: " + u.settings.Path + ", not created yet\n"
		if out != want {
			t.Errorf("got\n%s\nwant\n%s", out, want)
		}
	})
	for _, tt := range []struct {
		name string
		// saved puts the zip in the settings file instead of --firmware;
		// current reports the zip's build on the keyboard.
		saved, current, noManifest bool
		want                       string
	}{
		{"--firmware", false, false, false, "The build in firmware.zip is 8c04e7d: defy-installer update installs it."},
		{"saved", true, true, false, "The build in firmware.zip, saved in defy-installer.yaml, is 8c04e7d: up to date."},
		{"without a manifest", false, false, true,
			"The build in firmware.zip is without a manifest: defy-installer update installs it."},
	} {
		bubble(t, "a build on this computer, "+tt.name, func(t *testing.T) {
			u, _, newBuild := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
			u.opts.Command = "status"
			zip := filepath.Join(t.TempDir(), "firmware.zip")
			if tt.current {
				reportsBuild(u.d.updater, newBuild)
			}
			if tt.noManifest {
				newBuild.Manifest = nil
			}
			u.releases.local[zip] = newBuild
			if tt.saved {
				u.settings.Firmware = zip
			} else {
				u.opts.Firmware = zip
			}
			out, err := u.run(Status, "")
			if err != nil {
				t.Fatal(err)
			}
			wantLines(t, out, tt.want)
			if len(u.releases.downloads) > 0 {
				t.Errorf("downloaded %q", u.releases.downloads)
			}
		})
	}
	bubble(t, "an asleep half", func(t *testing.T) {
		// An asleep half, older firmware on the other half, and a newer
		// release.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0x11)
		u.d.updater.Halves[0].Link = ut.NoLink
		out, err := u.run(Status, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out,
			"Neuron       ZMK v0.1.0, default layout at 3f2a91c, connected by USB",
			"Left half    not connected: asleep or switched off",
			"Right half   ZMK v0.2.0, connected wirelessly",
			"The latest release is ZMK v0.2.0: defy-installer update installs it.")
	})
}

func TestStatusSaysWhatToDo(t *testing.T) {
	bubble(t, "bootloader", func(t *testing.T) {
		// A Neuron waiting in its bootloader, which may have been on its
		// way to either firmware.
		u := newUserTest(t, "boot", "status")
		u.releases.err = errNoNetwork
		out, err := u.run(Status, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Neuron       waiting in its bootloader, after a write that did not finish. Keep it "+
			"plugged in, and run defy-installer install to finish, or defy-installer uninstall to put Dygma's "+
			"firmware back. Either writes the whole Neuron again, which clears its Bluetooth pairings and any "+
			"changes made in ZMK Studio")
	})
	bubble(t, "UF2 loader", func(t *testing.T) {
		// The loader reports nothing, and is left as it is.
		u := newUserTest(t, "loader", "status")
		u.releases.publish(release.DefaultRepo, "v0.2.0", testBuild(t, release.DefaultRepo, commitB, 0xa5))
		out, err := u.run(Status, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out,
			"Neuron       waiting for a UF2 file on its DEFY drive. Unplug it and plug it back in to start ZMK. If the "+
				"drive comes back, copy the UF2 file again, or run defy-installer update",
			"Left half    not known: the Neuron does not report the halves while it shows its DEFY drive",
			"Right half   not known: the Neuron does not report the halves while it shows its DEFY drive",
			"The latest release is ZMK v0.2.0: defy-installer update installs it.")
		if len(u.d.opened) > 0 {
			t.Errorf("opened %q", u.d.opened)
		}
	})
	for _, tt := range []struct {
		source string
		want   string
	}{
		{"local", "Updates: the keyboard runs a local build, so give defy-installer update --from or --firmware."},
		{"not a repository", `Updates: the Neuron reports that it was built from "not a repository", which the ` +
			"installer cannot follow, so give defy-installer update --from or --firmware."},
	} {
		bubble(t, tt.source, func(t *testing.T) {
			u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
			u.d.updater.Version = ut.VersionPayload("v0.2.0", tt.source, "")
			out, err := u.run(Status, "")
			if err != nil {
				t.Fatal(err)
			}
			wantLines(t, out, tt.want)
		})
	}
	bubble(t, "saved path gone", func(t *testing.T) {
		// A saved firmware path that cannot be read, and the way back to
		// GitHub.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		gone := filepath.Join(t.TempDir(), "firmware.zip")
		u.settings.Firmware = gone
		out, err := u.run(Status, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Updates install from "+gone+", saved in defy-installer.yaml, which cannot be read: "+gone+
			": file does not exist", "To use your keymap repository again: defy-installer update --from alice/my-defy")
	})
}
