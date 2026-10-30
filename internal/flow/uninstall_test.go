// SPDX-License-Identifier: MIT

package flow

import (
	"bytes"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/release"
)

// stockFiles stands in for Dygma's files, as the recorded scenarios do.
func stockFiles(t *testing.T) *release.Stock {
	return &release.Stock{Neuron: recordedStock(), Keyscanner: keyscannerStandIn(t)}
}

func TestUninstall(t *testing.T) {
	bubble(t, "from ZMK", func(t *testing.T) {
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		u.opts.Command = "uninstall"
		u.releases.stock = stockFiles(t)
		u.d.after = "stock"
		out, err := u.run(Uninstall, "y\n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out,
			"Found your Defy. It runs ZMK v0.1.0.",
			"This puts Dygma's 2.3.0 firmware back on the whole keyboard.",
			"ZMK's Bluetooth pairings and any changes made in ZMK Studio will be lost.",
			"Continue? [y/N] y",
			"Downloading Dygma's firmware from github.com/Dygmalab... checked.",
			"",
			"Step 1 of 3: get ready",
			"  Plug both halves into the Neuron with their cables, and switch both halves off.",
			"  Press Enter when ready.",
			"",
			"Step 2 of 3: writing, about 2 minutes. Do not unplug anything.",
			writeLines[0],
			"  Writing the right half  [##########################] 100%",
			"  Writing the left half   [##########################] 100%",
			"",
			"Step 3 of 3: finish",
			"  Unplug the Neuron from this computer and plug it back in.",
			"  Then, for each half: unplug its cable, wait 5 seconds, and switch it on.",
			"",
			"Done. Your keyboard runs Dygma's firmware again.",
			"Open Bazecor and restore your layout from your backup.",
			"Pair your computers over Bluetooth again.")
		data := u.releases.stock.Keyscanner.TransferData()
		if f := u.d.stock; !slices.Equal(f.Finished, []int{0, 1}) || !bytes.Equal(f.Memory[0][:len(data)], data) ||
			!bytes.Equal(f.Memory[1][:len(data)], data) {
			t.Errorf("finished %v, or a half does not hold the keyscanner", f.Finished)
		}
		if strings.Contains(out, "Esc") {
			t.Errorf("asked for Esc:\n%s", out)
		}
	})
	bubble(t, "--firmware", func(t *testing.T) {
		// --firmware reads Dygma's files from a folder instead.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		u.opts.Command = "uninstall"
		u.releases.stock, u.releases.stockDir = stockFiles(t), "/Users/alice/Dygma"
		u.d.after = "stock"
		u.opts.Firmware = "/Users/alice/Dygma"
		out, err := u.run(Uninstall, "y\n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Continue? [y/N] y", "Read Dygma's firmware from /Users/alice/Dygma. Checked.",
			"Done. Your keyboard runs Dygma's firmware again.")
		if strings.Contains(out, "Downloading") {
			t.Errorf("downloaded:\n%s", out)
		}

		u.opts.Firmware = "/Users/alice/Downloads"
		_, err = u.run(Uninstall, "y\n")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a folder without the files: %v", err)
		}
	})
	bubble(t, "answered no", func(t *testing.T) {
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		u.releases.stock = stockFiles(t)
		out, err := u.run(Uninstall, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Continue? [y/N] ", "Nothing was written.")
		if slices.Contains(u.d.opened, studioPort.Path) && slices.Contains(u.d.opened, bootPort.Path) {
			t.Errorf("opened %q", u.d.opened)
		}
	})
	bubble(t, "from Dygma's firmware", func(t *testing.T) {
		// A Neuron already on Dygma's firmware goes straight to the halves,
		// whose updater may wait for Esc.
		u := newUserTest(t, "stock", "uninstall")
		u.releases.stock = stockFiles(t)
		u.d.stock.ReadyAfter = 2
		out, err := u.run(Uninstall, "y\n\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "Found your Defy. Its Neuron already runs Dygma's firmware.",
			"This puts Dygma's 2.3.0 firmware back on the halves too.",
			"Step 2 of 3: writing, about 2 minutes. Do not unplug anything.", escLine,
			"  Writing the right half  [##########################] 100%", "Step 3 of 3: finish")
		if strings.Contains(out, "Writing the Neuron") {
			t.Errorf("the Neuron was written:\n%s", out)
		}
	})
	bubble(t, "no network", func(t *testing.T) {
		u := newUserTest(t, "stock", "uninstall")
		u.releases.err = errNoNetwork
		_, err := u.run(Uninstall, "y\n")
		wantError(t, err, "Could not reach GitHub. Check your internet connection, or use --firmware with files you downloaded.")
	})
}

func TestUninstallFromTheBootloader(t *testing.T) {
	// A Neuron showing its UF2 loader's drive is put back as one in its
	// bootloader is, entered from the loader at the write.
	for _, tt := range []struct{ mode, found string }{
		{"boot", "Found your Defy. Its Neuron is waiting in its bootloader, after a write that did not finish."},
		{"loader", "Found your Defy. Its Neuron is waiting for a UF2 file on its DEFY drive."},
	} {
		bubble(t, "Dygma's firmware starts, from "+tt.mode, func(t *testing.T) {
			u := newUserTest(t, tt.mode, "uninstall")
			u.releases.stock = stockFiles(t)
			u.d.after = "stock"
			out, err := u.run(Uninstall, "y\n\n")
			if err != nil {
				t.Fatal(err)
			}
			wantLines(t, out, tt.found,
				"This puts Dygma's 2.3.0 firmware back on the whole keyboard.",
				"ZMK's Bluetooth pairings and any changes made in ZMK Studio will be lost.",
				"Continue? [y/N] y",
				writeLines[0],
				"  Writing the right half  [##########################] 100%",
				"  Writing the left half   [##########################] 100%",
				"Step 3 of 3: finish",
				"Done. Your keyboard runs Dygma's firmware again.")
			if tt.mode == "loader" && !u.d.loader.Requested {
				t.Error("the loader was not asked for the bootloader")
			}
			wantErase(t, u.d.boot, firmware.NeuronSettingsEnd)
		})
	}
	bubble(t, "Dygma's firmware does not start", func(t *testing.T) {
		// Dygma's firmware does not start: the halves wait for the next
		// run.
		u, _, _ := zmkUpdate(t, release.DefaultRepo, 0xa5, 0xa5)
		u.opts.Command = "uninstall"
		u.releases.stock = stockFiles(t)
		u.d.after = "boot"
		out, err := u.run(Uninstall, "y\n\n")
		wantError(t, err, "Dygma's firmware is written to the Neuron, but it did not start within 10 seconds. Unplug "+
			"the Neuron and plug it back in without holding its button, then run\n  defy-installer uninstall\nagain: "+
			"it goes on with the halves.")
		if strings.Contains(out, "half  [") || len(u.d.stock.Packets) > 0 {
			t.Errorf("the halves were written:\n%s", out)
		}
	})
}
