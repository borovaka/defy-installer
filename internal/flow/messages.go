// SPDX-License-Identifier: MIT

package flow

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/updater"
)

// saveSource saves --from or --firmware in the settings file once the
// keyboard runs the build, and returns what to say about what later
// updates install.
func (r *run) saveSource(c *candidate) ([]string, error) {
	s := r.opts.Settings
	if s.Path == "" || r.opts.From.IsZero() && r.opts.Firmware == "" {
		return nil, nil
	}
	before := *s
	var err error
	if r.opts.Firmware != "" {
		err = s.SaveFirmware(r.opts.Firmware)
	} else {
		err = s.SaveKeymap(r.opts.From)
	}
	if err != nil {
		return nil, guided(err, "The keyboard runs the new firmware, but saving the choice in %s failed: %v\n"+
			"Fix the file, or give --from or --firmware again with each update.", s.Path, err)
	}
	if s.Keymap == before.Keymap && s.Firmware == before.Firmware {
		return nil, nil
	}
	r.detail.say("Saved in %s.", s.Path)
	const update = `From now on, "defy-installer update" `
	src := c.ch.src
	switch {
	case src.Path != "":
		// The build's manifest names the repository it came from, if any.
		var built *updater.Version
		if m := c.manifest(); m != nil {
			built = &updater.Version{Module: m.Module, Source: m.Source, Commit: m.Commit}
		}
		return []string{update + "installs from " + src.Path + ".", r.backToGitHub(built)}, nil
	case src.Tag != "":
		name := src.Repo
		if src.IsDefault() {
			name = "the default layout"
		}
		return []string{fmt.Sprintf("%sstays on %s of %s.", update, src.Tag, name),
			"To follow the latest again: defy-installer update --from " + following(src)}, nil
	case src.IsDefault():
		return []string{update + "installs the latest release of the default layout."}, nil
	}
	return []string{update + "installs the latest build of " + src.Repo + ".",
		"For the default layout again: defy-installer update --from default"}, nil
}

// done prints lines, after a blank line when blank is set, then saves
// --from or --firmware and says what later updates install. A failed save
// is returned after the lines are printed.
func (r *run) done(c *candidate, blank bool, lines ...string) error {
	saved, err := r.saveSource(c)
	if len(lines) > 0 && lines[0] == "Done." && len(saved) > 0 {
		lines[0] += " " + saved[0]
		saved = saved[1:]
	}
	if blank && len(lines)+len(saved) > 0 {
		r.ui.blank()
	}
	for _, line := range append(lines, saved...) {
		r.ui.say("%s", line)
	}
	return err
}

// zmkName names ZMK at a module version, such as ZMK v0.2.0, or ZMK when
// the version is not known.
func zmkName(module string) string {
	if module == "" || module == updater.UnknownModule {
		return "ZMK"
	}
	return "ZMK " + module
}

// shortCommit is a commit as the messages give it: its first seven
// characters, with -dirty for a changed tree.
func shortCommit(commit string) string {
	hash, dirty := strings.CutSuffix(commit, "-dirty")
	hash = hash[:min(len(hash), 7)]
	if dirty {
		hash += "-dirty"
	}
	return hash
}

// runsZMK says what a Neuron running ZMK runs, as VERSION reads it.
func runsZMK(v *updater.Version) string {
	switch {
	case v == nil:
		return "It runs ZMK."
	case isDefaultSource(v.Source):
		return fmt.Sprintf("It runs %s with the default layout.", zmkName(v.Module))
	case v.Source == updater.LocalSource:
		return fmt.Sprintf("It runs %s, a local build.", zmkName(v.Module))
	case v.Commit == "":
		return fmt.Sprintf("It runs ZMK from %s.", v.Source)
	}
	return fmt.Sprintf("It runs ZMK from %s, commit %s.", v.Source, shortCommit(v.Commit))
}

// waiting says what a Neuron that runs no firmware waits in: its
// bootloader, or with mode neuron.Loader its UF2 loader.
func waiting(mode neuron.Mode) string {
	if mode == neuron.Loader {
		return "Its Neuron is waiting for a UF2 file on its DEFY drive."
	}
	return "Its Neuron is waiting in its bootloader, after a write that did not finish."
}

// builtWhen says when a build's commit was made, by the calendar where now
// is: today, yesterday, or on its date. It is empty for a commit without a
// time.
func builtWhen(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.In(now.Location())
	y, m, d := now.Date()
	// Noon of the day before, which exists on every day a clock changes.
	yy, ym, yd := time.Date(y, m, d-1, 12, 0, 0, 0, now.Location()).Date()
	switch ty, tm, td := t.Date(); {
	case ty == y && tm == m && td == d:
		return "today"
	case ty == yy && tm == ym && td == yd:
		return "yesterday"
	}
	return "on " + t.Format("2 January 2006")
}

// fromCommit says which commit a build came from, and when it was made.
func fromCommit(m *release.Manifest) string {
	if m.Commit == "" {
		return "without a commit"
	}
	if when := builtWhen(m.CommitTime, time.Now()); when != "" {
		return fmt.Sprintf("built %s from commit %s", when, shortCommit(m.Commit))
	}
	return "commit " + shortCommit(m.Commit)
}

// releaseLine says which release installs, before its images download: in
// a first install, or in an update from v.
func releaseLine(v *updater.Version, ch choice, rel *release.Release, first bool) string {
	m := rel.Manifest
	module := zmkName(m.Module)
	same := v != nil && strings.EqualFold(v.Source, m.Source)
	if ch.src.IsDefault() {
		const download = "Downloading it from GitHub..."
		switch {
		case first:
			return fmt.Sprintf("This installs %s with the default layout. %s", module, download)
		case same && ch.src.Tag != "":
			return fmt.Sprintf("%s with the default layout, release %s. %s", module, rel.Tag, download)
		case same && v.Module != m.Module:
			return fmt.Sprintf("%s is out. %s", module, download)
		case same:
			return fmt.Sprintf("The latest release is %s. %s", module, download)
		}
		return fmt.Sprintf("The default layout: %s. %s", module, download)
	}
	repo := ch.src.Repo
	switch {
	case first:
		return fmt.Sprintf("This installs ZMK from %s, %s, module %s. Downloading...", repo, fromCommit(m), m.Module)
	case same && ch.src.Tag != "":
		return fmt.Sprintf("Release %s of %s is commit %s. Downloading...", rel.Tag, repo, shortCommit(m.Commit))
	case same:
		when := ""
		if w := builtWhen(m.CommitTime, time.Now()); w != "" {
			when = ", built " + w
		}
		return fmt.Sprintf("The latest build of %s is commit %s%s. Downloading...", repo, shortCommit(m.Commit), when)
	}
	return fmt.Sprintf("Your keymap: %s, %s, module %s. Downloading...", repo, fromCommit(m), m.Module)
}

// localLine says what a build on this computer holds, once it is read.
func localLine(path string, b *release.Build) string {
	name := filepath.Base(path)
	m := b.Manifest
	if m == nil {
		return fmt.Sprintf("Read %s: %s, %s and %s, without a manifest. Checked.", name,
			filepath.Base(b.Neuron.Name), filepath.Base(b.Left.Name), filepath.Base(b.Right.Name))
	}
	what := m.Source
	switch {
	case isDefaultSource(m.Source):
		what = "the default layout"
	case m.Source == updater.LocalSource:
		what = "a local build"
	}
	if m.Commit != "" {
		what += ", commit " + shortCommit(m.Commit)
	}
	return fmt.Sprintf("Read %s: %s, module %s. Checked.", name, what, m.Module)
}

// capitalise returns s with its first letter in upper case.
func capitalise(s string) string {
	first, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(first)) + s[size:]
}

// inWords gives a wait as the messages do, such as 2 minutes or 30
// seconds.
func inWords(d time.Duration) string {
	n, unit := int(d/time.Second), "second"
	if d >= time.Minute && d%time.Minute == 0 {
		n, unit = int(d/time.Minute), "minute"
	}
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", n, unit)
}
