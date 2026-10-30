// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/updater"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// clears is what the messages for a Neuron left in its bootloader say
// that finishing with install costs.
const clears = "That writes the whole Neuron again, which clears its Bluetooth pairings and any changes made in ZMK " +
	"Studio. "

func TestErrorsSayWhatToDoNext(t *testing.T) {
	r := &run{opts: Options{Command: "update"}, chosen: &choice{src: release.Source{Repo: "alice/my-defy"}}}
	for _, tt := range []struct {
		err   error
		phase phase
		want  string
	}{
		{&release.NotFoundError{Kind: release.NoRelease, Repo: "alice/my-defy"}, phaseNone,
			"alice/my-defy has no firmware release yet. Its first build may still be running:\n" +
				"https://github.com/alice/my-defy/actions\nIf the repository is private, download the firmware zip from " +
				"the latest run and use\n  defy-installer update --firmware firmware.zip"},
		{&release.NotFoundError{Kind: release.NoTag, Repo: "alice/my-defy", Tag: "v9"}, phaseNone,
			"alice/my-defy has no release tagged v9. Check the tag, or follow the latest release again:\n" +
				"  defy-installer update --from alice/my-defy"},
		{&release.TooOldError{Version: "v0.1.0", MinInstaller: "v0.3.0"}, phaseNone,
			"This firmware needs defy-installer v0.3.0 or newer. Download it from\n" + release.InstallerReleases},
		{errNoNetwork, phaseNone,
			"Could not reach GitHub. Check your internet connection, or use --firmware with files you downloaded."},
		{fmt.Errorf("%w; %w", context.Canceled, neuron.ErrPartialImage), phaseNeuron,
			"Stopped. The Neuron is waiting in its bootloader. Keep it plugged in and run\n" +
				"  defy-installer install --from alice/my-defy\nagain to finish. " + clears + "Unplugged, it needs its " +
				"button to reach the bootloader again."},
		{context.Canceled, phaseNeuron, "Stopped. If the Neuron is waiting in its bootloader, keep it plugged in " +
			"and run\n  defy-installer install --from alice/my-defy\nagain to finish. " + clears + "Unplugged, it " +
			"needs its button to reach the bootloader again."},
		{context.Canceled, phaseNone, "Stopped. Nothing was written."},
		// A start from the UF2 loader's drive may leave the Neuron in its
		// bootloader, which a replug leaves again, keeping its settings.
		{context.Canceled, phaseStart, "Stopped. Nothing was written. Unplug the Neuron and plug it back in, then " +
			"run\n  defy-installer update --from alice/my-defy\nagain."},
		{errors.New("no ACK"), phaseStart, "no ACK\nNothing was written. Unplug the Neuron and plug it back in, then " +
			"run\n  defy-installer update --from alice/my-defy\nagain."},
		{context.Canceled, phaseWritten, "Stopped."},
		{fmt.Errorf("x: %w", context.Canceled), phaseHalves, "Stopped. A half may hold part of its firmware. Keep " +
			"both halves cabled to the Neuron and switched off, and run\n  defy-installer update --from " +
			"alice/my-defy\nagain to write them."},
		{fmt.Errorf("E: %w", neuron.ErrPartialImage), phaseNeuron,
			"The Neuron's write stopped part way, so the Neuron is waiting in its bootloader. Keep it plugged in and " +
				"run\n  defy-installer install --from alice/my-defy\nagain to finish. " + clears + "Unplugged, it " +
				"needs its button to reach the bootloader again."},
		{errors.New("no bootloader"), phaseNeuron, "no bootloader\nIf the Neuron is waiting in its bootloader, keep " +
			"it plugged in and run\n  defy-installer install --from alice/my-defy\nagain to finish. " + clears +
			"Unplugged, it needs its button to reach the bootloader again."},
		// Writing the halves gives one plain next step; the error behind
		// it shows with --verbose.
		{&updater.DeviceError{Opcode: updater.OpPage, Status: -5}, phaseHalves, "Writing the halves did not finish. " +
			"Keep both halves cabled to the Neuron and switched off, and run\n  defy-installer update --from " +
			"alice/my-defy\nagain to write them."},
		{&release.ChangedError{Repo: "alice/my-defy", Tag: "firmware"}, phaseNone, "Release firmware of alice/my-defy " +
			"changed while it downloaded, so nothing was written.\nWait a minute, then run\n  defy-installer update " +
			"--from alice/my-defy\nagain."},
		{fmt.Errorf("x: %w", neuron.ErrOnePort), phaseNeuron, "The Neuron still shows only one of its two ports after " +
			"5 seconds, so it was not written. Wait a few seconds, then run\n  defy-installer update --from " +
			"alice/my-defy\nagain."},
		{errors.New("something new"), phaseNone, "something new\nNothing was written. If this keeps happening, run " +
			"the command again with --verbose and report what it prints at " + issues + "."},
		{errors.New("something after"), phaseWritten, "something after\nThe keyboard was written. If this keeps " +
			"happening, run the command again with --verbose and report what it prints at " + issues + "."},
	} {
		r.phase = tt.phase
		err := r.guide(tt.err)
		if err.Error() != tt.want || !errors.Is(err, tt.err) {
			t.Errorf("%v:\ngot  %q\nwant %q", tt.err, err, tt.want)
		}
	}
	permission := fmt.Errorf("opening /dev/ttyACM0: %w", usbserial.ErrPermission)
	want := "This computer does not let you open the keyboard's port. Unplug the Neuron, plug it back in and try again."
	if runtime.GOOS == "linux" {
		want = "Linux does not let you open the keyboard's port. Run\n  sudo usermod -aG dialout $USER\n" +
			"or on Arch Linux\n  sudo usermod -aG uucp $USER\nthen log out and back in, and try again. To run it " +
			"once without that, use sudo:\n  sudo defy-installer update"
	}
	if err := r.guide(permission); !errors.Is(err, usbserial.ErrPermission) || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("got %v", err)
	}

	// Uninstall says to restart the Neuron, which releases the halves, and
	// keeps the folder of Dygma's files, in quotes when it has a space.
	r = &run{opts: Options{Command: "uninstall", Firmware: filepath.Join(t.TempDir(), "dygma 2.3.0")}}
	r.phase = phaseHalves
	want = "Writing the halves did not finish. Unplug the Neuron from this computer and plug it back in, then run\n" +
		"  defy-installer uninstall --firmware " + quoteArg(r.opts.Firmware) + "\nagain with both halves cabled to the " +
		"Neuron and switched off."
	if err := r.guide(errors.New("upgrade.keyscanner.sendWrite: no reply")); err.Error() != want {
		t.Errorf("got  %q\nwant %q", err, want)
	}
	// Uninstall finishes a Neuron left in its bootloader itself, which
	// clears ZMK's settings anyway.
	r.phase = phaseNeuron
	want = "Stopped. The Neuron is waiting in its bootloader. Keep it plugged in and run\n  defy-installer uninstall " +
		"--firmware " + quoteArg(r.opts.Firmware) + "\nagain to finish. Unplugged, it needs its button to reach the " +
		"bootloader again."
	if err := r.guide(fmt.Errorf("%w; %w", context.Canceled, neuron.ErrPartialImage)); err.Error() != want {
		t.Errorf("got  %q\nwant %q", err, want)
	}
	r = &run{opts: Options{Command: "update"}, chosen: &choice{src: release.Source{Repo: "alice/my-defy"}}}
	// --verbose keeps the error behind the words.
	r.opts.Verbose = true
	if err := r.guide(errNoNetwork); !strings.HasPrefix(err.Error(), "Details: Get ") {
		t.Errorf("got %v", err)
	}
}

// follow sets opts to run the command line msg gives on a line of its
// own, as a user would copy it: the command, then --from or --firmware.
func follow(t *testing.T, opts *Options, msg error) {
	t.Helper()
	for line := range strings.Lines(msg.Error()) {
		args, ok := strings.CutPrefix(strings.TrimSpace(line), "defy-installer ")
		if !ok || !strings.HasPrefix(line, "  ") {
			continue
		}
		command, flags, _ := strings.Cut(args, " ")
		opts.Command, opts.From, opts.Firmware = command, release.Source{}, ""
		if from, ok := strings.CutPrefix(flags, "--from "); ok {
			opts.From = source(t, from)
		} else if path, ok := strings.CutPrefix(flags, "--firmware "); ok {
			opts.Firmware = unquote(t, path)
		}
		return
	}
	t.Fatalf("%q gives no command line", msg)
}

// unquote reads back a path quoteArg quoted.
func unquote(t *testing.T, arg string) string {
	t.Helper()
	if inner, ok := strings.CutPrefix(arg, "'"); ok {
		return strings.ReplaceAll(strings.TrimSuffix(inner, "'"), `'\''`, "'")
	}
	return strings.Trim(arg, `"`)
}

func TestCommandLines(t *testing.T) {
	for _, tt := range []struct{ goos, path, want string }{
		{"darwin", "builds/alice", "builds/alice"},
		{"darwin", "Downloads/firmware (1).zip", "'Downloads/firmware (1).zip'"},
		{"linux", "/home/a/$HOME `id` \"x\"", "'/home/a/$HOME `id` \"x\"'"},
		{"linux", "/home/a/it's", `'/home/a/it'\''s'`},
		{"windows", `C:\builds\alice`, `C:\builds\alice`},
		{"windows", `C:\Users\a\Downloads\firmware (1).zip`, `"C:\Users\a\Downloads\firmware (1).zip"`},
	} {
		if got := quoteFor(tt.goos, tt.path); got != tt.want {
			t.Errorf("%s %q: %s, want %s", tt.goos, tt.path, got, tt.want)
		}
	}
	if path := filepath.Join("Downloads", "it's (1).zip"); unquote(t, quoteArg(path)) != path {
		t.Errorf("%q reads back as %q", path, unquote(t, quoteArg(path)))
	}
	// Before a build is chosen, the flags given name it.
	r := &run{opts: Options{Command: "update", From: release.Source{Repo: "alice/my-defy"}}}
	if got := r.rerun("install"); got != "defy-installer install --from alice/my-defy" {
		t.Errorf("got %q", got)
	}
	r = &run{opts: Options{Command: "update"}, chosen: &choice{src: release.Local(filepath.Join("a b", "fw.zip"))}}
	if got, want := r.rerun("update"), "defy-installer update --firmware "+quoteArg(filepath.Join("a b", "fw.zip")); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := (&run{opts: Options{Command: "uninstall"}}).rerun("uninstall"); got != "defy-installer uninstall" {
		t.Errorf("got %q", got)
	}
}
