// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// GuidedError is an error of install, update, uninstall or status, worded
// for the user and ending with what to do next. Err is the error behind
// it.
type GuidedError struct {
	Message string
	Err     error
}

func (e *GuidedError) Error() string {
	return e.Message
}

func (e *GuidedError) Unwrap() error {
	return e.Err
}

// guided returns a GuidedError for err with the message given.
func guided(err error, format string, args ...any) *GuidedError {
	return &GuidedError{Message: fmt.Sprintf(format, args...), Err: err}
}

// issues is where to ask for help.
const issues = "https://github.com/borovaka/defy-installer/issues"

// unplugged says what a Neuron that was waiting in its bootloader needs
// once it has been unplugged: the bootloader finds the vector page erased,
// since that page is written last, and starts nothing.
const unplugged = "Unplugged, it needs its button to reach the bootloader again."

// wholeNeuron says what finishing from the bootloader costs: the state of
// the Neuron's settings is not known, so install writes all of the Neuron.
const wholeNeuron = "That writes the whole Neuron again, which clears its Bluetooth pairings and any changes " +
	"made in ZMK Studio."

// guide returns err worded for the user, ending with what to do next.
func (r *run) guide(err error) error {
	if err == nil {
		return nil
	}
	g, ok := errors.AsType[*GuidedError](err)
	if !ok {
		g = r.explain(err)
	}
	if r.opts.Verbose && g.Err != nil && !strings.Contains(g.Message, g.Err.Error()) {
		return &GuidedError{Message: "Details: " + g.Err.Error() + "\n" + g.Message, Err: g.Err}
	}
	return g
}

// explain words an error that has no wording of its own, and says what to
// do next. The error of a halves write that did not finish shows only
// with --verbose, since the next step is the same whatever it was.
func (r *run) explain(err error) *GuidedError {
	// A Neuron waiting in its bootloader is finished by install, which
	// update runs too, or by uninstall. after says what then happens.
	finish, after := "install", wholeNeuron+" "+unplugged
	if r.opts.Command == "uninstall" {
		finish, after = "uninstall", unplugged
	}
	notFound, isNotFound := errors.AsType[*release.NotFoundError](err)
	tooOld, isTooOld := errors.AsType[*release.TooOldError](err)
	changed, isChanged := errors.AsType[*release.ChangedError](err)
	_, isURL := errors.AsType[*url.Error](err)
	stopped := errors.Is(err, context.Canceled)
	switch {
	case stopped && r.phase == phaseNeuron && errors.Is(err, neuron.ErrPartialImage):
		return guided(err, "Stopped. The Neuron is waiting in its bootloader. Keep it plugged in and run\n  %s\n"+
			"again to finish. %s", r.rerun(finish), after)
	case stopped && r.phase == phaseNeuron:
		return guided(err, "Stopped. If the Neuron is waiting in its bootloader, keep it plugged in and run\n  %s\n"+
			"again to finish. %s", r.rerun(finish), after)
	case stopped && r.phase == phaseHalves:
		return guided(err, "Stopped. A half may hold part of its firmware. %s", r.halvesAgain())
	case stopped && r.phase == phaseNone:
		return guided(err, "Stopped. Nothing was written.")
	case stopped && r.phase == phaseStart:
		return guided(err, "Stopped. Nothing was written. %s", r.replugFirst())
	case stopped:
		return guided(err, "Stopped.")
	case errors.Is(err, neuron.ErrPartialImage):
		return guided(err, "The Neuron's write stopped part way, so the Neuron is waiting in its bootloader. Keep it "+
			"plugged in and run\n  %s\nagain to finish. %s", r.rerun(finish), after)
	case errors.Is(err, neuron.ErrOnePort):
		return guided(err, "The Neuron still shows only one of its two ports after %s, so it was not written. Wait a "+
			"few seconds, then run\n  %s\nagain.", inWords(onePortWait), r.rerun(r.opts.Command))
	case errors.Is(err, errNoInput):
		return guided(err, "The installer waits for Enter at each step that needs your hands, and its input ended.\n"+
			"Run it again in a terminal.")
	case errors.Is(err, usbserial.ErrPermission) && runtime.GOOS == "linux":
		return guided(err, "Linux does not let you open the keyboard's port. Run\n  sudo usermod -aG dialout $USER\n"+
			"or on Arch Linux\n  sudo usermod -aG uucp $USER\nthen log out and back in, and try again. To run it once "+
			"without that, use sudo:\n  sudo %s", r.rerun(r.opts.Command))
	case errors.Is(err, usbserial.ErrPermission):
		return guided(err, "This computer does not let you open the keyboard's port. Unplug the Neuron, plug it back "+
			"in and try again.\nIf that does not help, run the command again with --verbose and report what it prints "+
			"at %s.", issues)
	case isTooOld:
		return guided(err, "This firmware needs defy-installer %s or newer. Download it from\n%s",
			tooOld.MinInstaller, release.InstallerReleases)
	case isChanged:
		return guided(err, "Release %s of %s changed while it downloaded, so nothing was written.\nWait a minute, "+
			"then run\n  %s\nagain.", changed.Tag, changed.Repo,
			r.rerun(r.opts.Command))
	case isNotFound && notFound.Kind == release.NoRelease && strings.EqualFold(notFound.Repo, release.DefaultRepo):
		return guided(err, "%s has no release on GitHub yet. Use --firmware with files you downloaded.", notFound.Repo)
	case isNotFound && notFound.Kind == release.NoRelease:
		return guided(err, "%s has no firmware release yet. Its first build may still be running:\n"+
			"https://github.com/%s/actions\nIf the repository is private, download the firmware zip from the latest "+
			"run and use\n  defy-installer update --firmware firmware.zip", notFound.Repo, notFound.Repo)
	case isNotFound && notFound.Kind == release.NoTag:
		return guided(err, "%s has no release tagged %s. Check the tag, or follow the latest release again:\n"+
			"  defy-installer update --from %s", notFound.Repo, notFound.Tag, following(release.Source{Repo: notFound.Repo}))
	case isNotFound:
		return guided(err, "Release %s of %s has no %s, so its build may have failed. Check\n"+
			"https://github.com/%s/actions", notFound.Tag, notFound.Repo, notFound.File, notFound.Repo)
	case isURL:
		return guided(err, "Could not reach GitHub. Check your internet connection, or use --firmware with files "+
			"you downloaded.")
	}
	const askForHelp = "If this keeps happening, run the command again with --verbose and report what it prints at " +
		issues + "."
	switch r.phase {
	case phaseStart:
		return guided(err, "%s\nNothing was written. %s", err, r.replugFirst())
	case phaseNeuron:
		return guided(err, "%s\nIf the Neuron is waiting in its bootloader, keep it plugged in and run\n  %s\n"+
			"again to finish. %s", err, r.rerun(finish), after)
	case phaseHalves:
		return guided(err, "Writing the halves did not finish. %s", r.halvesAgain())
	case phaseWritten:
		return guided(err, "%s\nThe keyboard was written. %s", err, askForHelp)
	}
	return guided(err, "%s\nNothing was written. %s", err, askForHelp)
}

// replugFirst says what to do after a start from the UF2 loader's drive
// that did not finish. The Neuron may be waiting in its bootloader, which
// a replug leaves for the loader again; a run that found it there would
// write the whole Neuron and clear its settings.
func (r *run) replugFirst() string {
	return fmt.Sprintf("Unplug the Neuron and plug it back in, then run\n  %s\nagain.", r.rerun(r.opts.Command))
}

// halvesAgain says how to write the halves again after a write of them
// that did not finish.
func (r *run) halvesAgain() string {
	if r.opts.Command == "uninstall" {
		// Dygma's firmware holds the halves until the Neuron restarts.
		return fmt.Sprintf("Unplug the Neuron from this computer and plug it back in, then run\n  %s\n"+
			"again with both halves cabled to the Neuron and switched off.", r.rerun("uninstall"))
	}
	return fmt.Sprintf("Keep both halves cabled to the Neuron and switched off, and run\n  %s\nagain to write "+
		"them.", r.rerun(r.opts.Command))
}

// rerun is the command line that runs command again on the build this run
// chose, with --from or --firmware whatever chose it: a Neuron left in its
// bootloader reports nothing, and the settings file names the build only
// after a run that worked. For uninstall it keeps the --firmware folder.
func (r *run) rerun(command string) string {
	line := "defy-installer " + command
	switch {
	case command == "uninstall" && r.opts.Firmware != "":
		return line + " --firmware " + quoteArg(r.opts.Firmware)
	case command == "uninstall":
		return line
	case r.chosen != nil && r.chosen.src.Path != "":
		return line + " --firmware " + quoteArg(r.chosen.src.Path)
	case r.chosen != nil:
		return line + " --from " + r.chosen.src.String()
	case r.opts.Firmware != "":
		return line + " --firmware " + quoteArg(r.opts.Firmware)
	case !r.opts.From.IsZero():
		return line + " --from " + r.opts.From.String()
	}
	return line
}

// quoteArg quotes a path, when a shell would split it or read its
// characters, for the shell of the system the installer runs on.
func quoteArg(path string) string {
	return quoteFor(runtime.GOOS, path)
}

// quoteFor is quoteArg for goos. On macOS and Linux single quotes keep
// every character as it is; a single quote in the path ends them, is
// written with a backslash, and starts them again. On Windows double
// quotes keep a path whole in both PowerShell and cmd, which has no single
// quotes, and a Windows path cannot hold a "; PowerShell still reads a $
// or a backtick inside them.
func quoteFor(goos, path string) string {
	if goos == "windows" {
		if strings.ContainsAny(path, " \t'$&();|<>*?[]{}`!^%,=@#") {
			return `"` + path + `"`
		}
		return path
	}
	if strings.ContainsAny(path, " \t\n'\"$&();|<>*?[]{}`!\\#~") {
		return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	}
	return path
}
