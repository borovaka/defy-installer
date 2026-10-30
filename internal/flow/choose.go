// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/settings"
	"github.com/borovaka/defy-installer/internal/updater"
)

// origin is what chose the build an install or update writes.
type origin int

const (
	byFlag origin = iota
	bySettingsFirmware
	bySettingsKeymap
	byNeuron
	byDefault
)

// choice is the build an install or update writes, and what chose it.
type choice struct {
	src  release.Source
	from origin
}

// choose picks the build to install, the first of: --firmware or --from;
// the settings file's firmware, then its keymap; the source the Neuron
// reports, v, when it is a repository; the default. It keeps the choice
// for the messages that say how to run the command again.
func (r *run) choose(v *updater.Version) (choice, error) {
	ch, err := r.pick(v)
	if err == nil {
		r.chosen = &ch
	}
	return ch, err
}

// pick is choose without keeping the choice. Its errors, all about v, are
// *GuidedError.
func (r *run) pick(v *updater.Version) (choice, error) {
	s := r.opts.Settings
	switch {
	case r.opts.Firmware != "":
		return choice{release.Local(r.opts.Firmware), byFlag}, nil
	case !r.opts.From.IsZero():
		return choice{r.opts.From, byFlag}, nil
	case s.Firmware != "":
		return choice{release.Local(s.Firmware), bySettingsFirmware}, nil
	case !s.Keymap.IsZero():
		return choice{s.Keymap, bySettingsKeymap}, nil
	case v != nil && v.Source == updater.LocalSource:
		cmd := r.opts.Command
		return choice{}, guided(nil, "The keyboard runs a local build, which is not on GitHub, so the installer does not "+
			"replace it unless told what to install instead.\nFor the default layout: defy-installer %s --from default\n"+
			"For a build on this computer: defy-installer %s --firmware <zip or folder>", cmd, cmd)
	case v != nil:
		src, err := release.ParseSource(v.Source)
		if err != nil || src.Tag != "" {
			return choice{}, guided(err, "The Neuron reports that it was built from %q, which the installer cannot "+
				"follow.\nChoose what to install: defy-installer %s --from default, or --firmware <zip or folder>",
				v.Source, r.opts.Command)
		}
		return choice{src, byNeuron}, nil
	}
	return choice{release.Default(), byDefault}, nil
}

// settingsName names the settings file as the messages do.
func (r *run) settingsName() string {
	if r.opts.Settings.Path == "" {
		return settings.FileName
	}
	return filepath.Base(r.opts.Settings.Path)
}

// notices says when a saved path or a pinned tag decides what installs,
// and how to change it. The pinned tag is mentioned only when a newer
// release is out.
func (r *run) notices(ctx context.Context, ch choice, v *updater.Version) {
	switch ch.from {
	case bySettingsFirmware:
		r.ui.say("Installing from %s, saved in %s. It does not check GitHub.", ch.src.Path, r.settingsName())
		r.ui.say("%s", r.backToGitHub(v))
	case bySettingsKeymap:
		if ch.src.Tag == "" {
			return
		}
		latest, err := r.opts.Releases.LatestTag(ctx, ch.src.Repo)
		if err != nil || latest == ch.src.Tag {
			return
		}
		r.ui.say("Staying on %s, pinned in %s. The latest is %s.", ch.src.Tag, r.settingsName(), latest)
		r.ui.say("To follow the latest again: defy-installer update --from %s", following(ch.src))
	}
}

// backToGitHub says how to install from GitHub again instead of a saved
// path: from the keymap saved, or the one the Neuron reports, or the
// default.
func (r *run) backToGitHub(v *updater.Version) string {
	if k := r.opts.Settings.Keymap; !k.IsZero() {
		if k.IsDefault() {
			return "To use the default layout again: defy-installer update --from " + k.String()
		}
		return "To use your keymap repository again: defy-installer update --from " + k.String()
	}
	if v != nil && v.Source != updater.LocalSource && !isDefaultSource(v.Source) {
		if _, err := release.ParseSource(v.Source); err == nil {
			return "To use your keymap repository again: defy-installer update --from " + v.Source
		}
	}
	return "To install from GitHub again: defy-installer update --from default"
}

// following is src without its tag, as --from takes it.
func following(src release.Source) string {
	src.Tag = ""
	return src.String()
}

// isDefaultSource reports whether source, as VERSION or a manifest gives
// it, is the default layout's repository.
func isDefaultSource(source string) bool {
	return strings.EqualFold(source, release.DefaultRepo)
}

// neuronRuns reports whether the Neuron, as v reports it, runs the build m
// describes: the same source, commit and module version, none of them
// dirty. A keymap repository whose west.yml follows a branch builds another
// module at the same commit, as does a build from a changed module.
func neuronRuns(v *updater.Version, m *release.Manifest) bool {
	return v != nil && m != nil && m.Commit != "" && v.Commit == m.Commit && !v.Dirty() &&
		v.Source != updater.LocalSource && strings.EqualFold(v.Source, m.Source) &&
		v.Module == m.Module && !strings.HasSuffix(v.Module, "-dirty") && v.Module != updater.UnknownModule
}
