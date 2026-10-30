// SPDX-License-Identifier: MIT

// Package settings reads and writes defy-installer.yaml, which names the
// keymap or the firmware files that install and update use.
//
// Each setting is on a line of its own, such as keymap: 'alice/my-defy'. A
// save rewrites only the text of each value that changes, and appends a
// setting the file lacks; every other byte stays, comments, line breaks and
// a byte order mark included. A missing file is created from the template
// the installer's archives ship.
package settings

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/borovaka/defy-installer/internal/release"
)

// FileName is the settings file's name.
const FileName = "defy-installer.yaml"

// template is the settings file as the installer's archives ship it. A
// missing file is created from it.
//
//go:embed defy-installer.yaml
var template []byte

// File is the settings file and the values it holds.
type File struct {
	// Path is the file in use or, when there is none, where saving a
	// value creates it.
	Path string
	// Exists is whether Path held the file when it was last read or saved.
	Exists bool
	// Keymap is the keymap the file names, as release.ParseSource reads
	// it, or the zero Source when the file leaves it empty.
	Keymap release.Source
	// Firmware is a zip or a folder of firmware files, or empty. It is an
	// absolute path: the file may give one relative to its own folder, or
	// starting at ~ for the user's home folder.
	Firmware string
}

// executable, userConfigDir and userHomeDir locate the settings file and
// the paths in it; tests replace them.
var (
	executable    = os.Executable
	userConfigDir = os.UserConfigDir
	userHomeDir   = os.UserHomeDir
)

// Load finds the settings file and reads it. It looks next to the
// executable, with symbolic links resolved where they can be, which is
// where the installer's archives put the file, and then in the user's
// config folder: ~/Library/Application Support/defy-installer on macOS,
// %AppData%\defy-installer on Windows and ~/.config/defy-installer on
// Linux. When neither holds the file, the File is empty, with its Path in
// the config folder.
func Load() (*File, error) {
	if exe, err := executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		f, err := read(filepath.Join(filepath.Dir(exe), FileName))
		if err != nil || f.Exists {
			return f, err
		}
	}
	dir, err := userConfigDir()
	if err != nil {
		return nil, fmt.Errorf("no %s next to the installer, and no config folder for one: %w", FileName, err)
	}
	return read(filepath.Join(dir, "defy-installer", FileName))
}

// read reads and checks the settings file at path. A missing file gives an
// empty File. An unknown setting, a setting given twice, a setting that is
// not on a line of its own with a single plain or quoted value, a keymap
// that release.ParseSource does not read, or a firmware path with a
// control character is an error that names the file and the line.
func read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &File{Path: path}, nil
	} else if err != nil {
		return nil, err
	}
	d, err := parse(path, data)
	if err != nil {
		return nil, err
	}
	values, err := d.resolved()
	if err != nil {
		return nil, err
	}
	return &File{Path: path, Exists: true, Keymap: keymapOf(values[keymapKey]), Firmware: values[firmwareKey]}, nil
}

// keymapOf reads a keymap that parse has checked. An empty one is the
// zero Source.
func keymapOf(value string) release.Source {
	src, err := release.ParseSource(value)
	if err != nil {
		return release.Source{}
	}
	return src
}

// resolved returns the values d holds as a File holds them, with the
// firmware path made absolute: a path relative to the settings file's
// folder, or a leading ~ for the user's home folder. On Windows, a path
// such as \Users\alice\firmware.zip is on the settings file's drive.
func (d *document) resolved() (map[string]string, error) {
	values := d.values()
	fw := values[firmwareKey]
	if fw == "" || filepath.IsAbs(fw) {
		return values, nil
	}
	prefix := fmt.Sprintf("%s:%d: firmware %q", d.path, d.settings[firmwareKey].node.Line, fw)
	if fw == "~" || strings.HasPrefix(fw, "~") && os.IsPathSeparator(fw[1]) {
		home, err := userHomeDir()
		if err != nil {
			return nil, fmt.Errorf("%s starts at your home folder, ~, and the installer cannot find it: %v; give the "+
				"full path", prefix, err)
		}
		values[firmwareKey] = filepath.Join(home, fw[1:])
		return values, nil
	}
	if filepath.VolumeName(fw) != "" {
		return nil, fmt.Errorf("%s names a drive but not the folder on it; give the full path, like "+
			"'C:\\Users\\alice\\firmware.zip'", prefix)
	}
	dir, err := filepath.Abs(filepath.Dir(d.path))
	if err != nil {
		return nil, err
	}
	if os.IsPathSeparator(fw[0]) {
		dir = filepath.VolumeName(dir)
	}
	values[firmwareKey] = filepath.Join(dir, fw)
	return values, nil
}

// SaveKeymap saves src, a release on GitHub, as the keymap, and empties
// firmware, so that the keymap decides what installs. It writes src as
// src.String() gives it, unless the file's keymap already reads as src.
func (f *File) SaveKeymap(src release.Source) error {
	if _, err := release.ParseSource(src.String()); err != nil {
		return err
	}
	return f.save(change{keymapKey, src.String()}, change{firmwareKey, ""})
}

// SaveFirmware saves path, the absolute path of a zip or a folder, which
// then installs instead of the keymap. The keymap stays.
func (f *File) SaveFirmware(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("the firmware path %s is not absolute", path)
	}
	if strings.ContainsFunc(path, notInPath) {
		return fmt.Errorf("the firmware path %q holds a control character, which the settings file does not take", path)
	}
	return f.save(change{firmwareKey, path})
}

func (f *File) save(changes ...change) error {
	data, err := os.ReadFile(f.Path)
	exists := err == nil
	if errors.Is(err, fs.ErrNotExist) {
		data = template
	} else if err != nil {
		return err
	}
	d, err := parse(f.Path, data)
	if err != nil {
		return err
	}
	// A value that reads as the one to save stays as it is written.
	have, err := d.resolved()
	if err != nil {
		return err
	}
	var todo []change
	want := d.values()
	for _, c := range changes {
		if !readsAs(c.key, have[c.key], c.value) {
			todo = append(todo, c)
			want[c.key] = c.value
		}
	}
	if len(todo) > 0 {
		out, err := d.edit(todo)
		if err != nil {
			return err
		}
		// The new text must read back as the values wanted.
		check, err := parse(f.Path, out)
		if err == nil {
			have, err = check.resolved()
		}
		if err != nil {
			return fmt.Errorf("saving the settings would break the file, so it is left as it was; edit it by hand: %w", err)
		}
		got := check.values()
		for _, key := range settingKeys {
			if got[key] != want[key] {
				return fmt.Errorf("saving the settings in %s does not give %s %q, so it is left as it was; edit it by hand",
					f.Path, key, want[key])
			}
		}
		if err := writeFile(f.Path, out, exists); err != nil {
			return err
		}
		exists = true
	}
	f.Exists, f.Keymap, f.Firmware = exists, keymapOf(have[keymapKey]), have[firmwareKey]
	return nil
}

// readsAs reports whether have, a setting's value as resolved, reads as
// value: the same keymap, or the same text.
func readsAs(key, have, value string) bool {
	if key == keymapKey && have != "" && value != "" {
		return keymapOf(have) == keymapOf(value)
	}
	return have == value
}

// writeFile replaces the file at path with data, through a temporary file
// in the same folder, so that the file is either the old one or the new
// one. A symbolic link at path is followed, and the file it points to
// replaced. A new file's folder is created.
func writeFile(path string, data []byte, exists bool) error {
	mode := fs.FileMode(0o644)
	if exists {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+FileName+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("saving %s: %w", path, err)
	}
	return nil
}
