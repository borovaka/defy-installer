// SPDX-License-Identifier: MIT

package settings

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/borovaka/defy-installer/internal/release"
)

// writeSettings writes a settings file with text into a new folder.
func writeSettings(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	writeText(t, path, text)
	return path
}

func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// source is s as release.ParseSource reads it.
func source(t *testing.T, s string) release.Source {
	t.Helper()
	src, err := release.ParseSource(s)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// written returns the values the file at path holds, as written in it.
func written(t *testing.T, path string) map[string]string {
	t.Helper()
	d, err := parse(path, []byte(readText(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	return d.values()
}

func TestTemplate(t *testing.T) {
	d, err := parse("template", template)
	if err != nil {
		t.Fatal(err)
	}
	if values := d.values(); values[keymapKey] != "" || values[firmwareKey] != "" || len(d.settings) != 2 {
		t.Errorf("values %q", values)
	}
	// The Windows path the template gives reads as it is written.
	example := `C:\Users\alice\firmware.zip`
	text := strings.Replace(string(template), "firmware: ''", "firmware: '"+example+"'", 1)
	if d, err := parse("template", []byte(text)); err != nil || d.values()[firmwareKey] != example {
		t.Errorf("the template's example: %v", err)
	}
}

// saveInPlace are saves that change the text of the values that change
// and nothing else.
var saveInPlace = []struct {
	name    string
	in      string
	changes []change
	out     string
}{
	{
		name: "the template",
		in:   string(template), changes: []change{{keymapKey, "alice/my-defy"}, {firmwareKey, ""}},
		out: strings.Replace(string(template), `keymap: ''`, `keymap: 'alice/my-defy'`, 1),
	},
	{
		name: "the template, a Windows path",
		in:   string(template), changes: []change{{firmwareKey, `C:\Users\alice\Downloads\firmware (1).zip`}},
		out: strings.Replace(string(template), `firmware: ''`, `firmware: 'C:\Users\alice\Downloads\firmware (1).zip'`, 1),
	},
	{
		name:    "plain, with comments and blank lines",
		in:      "# top\n\n\nkeymap: default # mine\n\n  # indented comment\nfirmware: /old/firmware.zip\n\n# end\n",
		changes: []change{{keymapKey, "alice/x@v0.2.0"}, {firmwareKey, ""}},
		out:     "# top\n\n\nkeymap: alice/x@v0.2.0 # mine\n\n  # indented comment\nfirmware: ''\n\n# end\n",
	},
	{
		name:    "single quotes, doubled",
		in:      "firmware: ''\n",
		changes: []change{{firmwareKey, "/home/o'brien/firmware"}},
		out:     "firmware: '/home/o''brien/firmware'\n",
	},
	{
		name:    "double quotes",
		in:      "firmware: \"/old\"  # note\nkeymap: \"\"\n",
		changes: []change{{firmwareKey, `C:\fw "new"\a.zip`}},
		out:     "firmware: \"C:\\\\fw \\\"new\\\"\\\\a.zip\"  # note\nkeymap: \"\"\n",
	},
	{
		name:    "an empty null",
		in:      "keymap:\nfirmware:   # none yet\n",
		changes: []change{{keymapKey, "default"}, {firmwareKey, "/fw"}},
		out:     "keymap: default\nfirmware: /fw   # none yet\n",
	},
	{
		name:    "CRLF",
		in:      "# a\r\nkeymap: default\r\n\r\n",
		changes: []change{{keymapKey, "alice/x"}, {firmwareKey, "/fw"}},
		out:     "# a\r\nkeymap: alice/x\r\n\r\nfirmware: /fw\r\n",
	},
	{
		name:    "a byte order mark",
		in:      "\xef\xbb\xbf# a\nkeymap: 'x/y'\n",
		changes: []change{{keymapKey, "alice/z"}},
		out:     "\xef\xbb\xbf# a\nkeymap: 'alice/z'\n",
	},
	{
		name:    "appended to an empty file",
		in:      "",
		changes: []change{{keymapKey, "alice/x"}, {firmwareKey, "C:\\fw"}},
		out:     "keymap: alice/x\nfirmware: 'C:\\fw'\n",
	},
	{
		name:    "appended with the settings' indent",
		in:      "  keymap: a/b\n  # trailing\n",
		changes: []change{{firmwareKey, "/fw"}},
		out:     "  keymap: a/b\n  # trailing\n  firmware: /fw\n",
	},
}

func TestSaveInPlace(t *testing.T) {
	for _, tc := range saveInPlace {
		t.Run(tc.name, func(t *testing.T) {
			path := writeSettings(t, tc.in)
			f, err := read(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.save(tc.changes...); err != nil {
				t.Fatal(err)
			}
			if got := readText(t, path); got != tc.out {
				t.Errorf("%q\nwant\n%q", got, tc.out)
			}
			values := written(t, path)
			for _, c := range tc.changes {
				if values[c.key] != c.value || !f.Exists {
					t.Errorf("%s is %q after saving %q", c.key, values[c.key], c.value)
				}
			}
			if back, err := read(path); err != nil || *back != *f {
				t.Errorf("reads back as %+v, %v; want %+v", back, err, f)
			}
		})
	}
}

// saveQuotes are values that read back as something else without quotes,
// with the text they are saved as in place of a plain value.
var saveQuotes = map[string]string{
	"true":            `'true'`,
	"C:\\fw.zip":      `'C:\fw.zip'`,
	"/a: b":           `'/a: b'`,
	" /lead":          `' /lead'`,
	"-/x":             `'-/x'`,
	"it's":            `'it''s'`,
	"/Users/Zo\u00eb": "'/Users/Zo\u00eb'",
	"/a\U0001F600b":   `"/a\U0001F600b"`,
	"/plain/v1.0+x":   "/plain/v1.0+x",
}

func TestSaveQuotes(t *testing.T) {
	for value, text := range saveQuotes {
		t.Run(value, func(t *testing.T) {
			path := writeSettings(t, "firmware: /old\n")
			f := &File{Path: path}
			if err := f.save(change{firmwareKey, value}); err != nil {
				t.Fatal(err)
			}
			if got := readText(t, path); got != "firmware: "+text+"\n" {
				t.Errorf("%q, want %q", got, "firmware: "+text+"\n")
			}
			if got := written(t, path)[firmwareKey]; got != value {
				t.Errorf("reads back as %q", got)
			}
		})
	}
	f := &File{Path: writeSettings(t, "firmware: /old\n")}
	if err := f.save(change{firmwareKey, "/a\xffb"}); err == nil {
		t.Error("saved a value that is not UTF-8")
	}
}

func TestSaveRefuses(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, "keymap: default\n")
	f, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		save func() error
		want string
	}{
		{func() error { return f.SaveKeymap(release.Source{Repo: "alice"}) }, `"alice" is not default or owner/repo`},
		{func() error { return f.SaveFirmware(filepath.Join("relative", "firmware.zip")) }, "is not absolute"},
		{func() error { return f.SaveFirmware(filepath.Join(dir, "new\nfirmware.zip")) },
			`firmware.zip" holds a control character, which the settings file does not take`},
		{func() error { return f.SaveFirmware(filepath.Join(dir, "a\u2028b")) }, "holds a control character"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if err := tc.save(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v", err)
			}
		})
	}
	// A save reads the file again, and refuses to change it once it
	// breaks the rules.
	writeText(t, path, "{keymap: default}\n")
	if err := f.SaveKeymap(source(t, "alice/x")); err == nil || !strings.Contains(err.Error(), path+":1: the settings are in braces") {
		t.Errorf("error %v", err)
	}
	if got := readText(t, path); got != "{keymap: default}\n" {
		t.Errorf("the file holds %q", got)
	}
}

func TestSaveKeymapAndFirmware(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	f := &File{Path: path}
	fw := filepath.Join(t.TempDir(), "firmware.zip")
	if err := f.SaveFirmware(fw); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveKeymap(source(t, "alice/my-defy@v1")); err != nil {
		t.Fatal(err)
	}
	if f.Keymap != source(t, "alice/my-defy@v1") || f.Firmware != "" {
		t.Errorf("%+v", f)
	}
	if err := f.SaveFirmware(fw); err != nil {
		t.Fatal(err)
	}
	if back, err := read(path); err != nil || back.Keymap != source(t, "alice/my-defy@v1") || back.Firmware != fw {
		t.Errorf("reads back as %+v, %v", back, err)
	}
}

// A keymap is written as release.Source.String gives it.
func TestSaveKeymapWritesTheSource(t *testing.T) {
	path := writeSettings(t, "keymap: alice/x\n")
	f := &File{Path: path}
	if err := f.SaveKeymap(source(t, "Default@v0.1.0")); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, path); got != "keymap: default@v0.1.0\n" {
		t.Errorf("%q", got)
	}
	if err := f.SaveKeymap(release.Local("/x.zip")); err == nil {
		t.Error("saved a path as the keymap")
	}
}

// A save reads the file again, so a change made since it was read stays.
func TestSaveRereads(t *testing.T) {
	path := writeSettings(t, "keymap: default\n")
	f, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, path, "# added\nkeymap: default\n")
	if err := f.SaveKeymap(source(t, "alice/x")); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, path); got != "# added\nkeymap: alice/x\n" {
		t.Errorf("%q", got)
	}
}

// A save that changes nothing writes nothing, and creates no file.
func TestSaveWithoutChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings", FileName)
	f := &File{Path: path}
	if err := f.save(change{firmwareKey, ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) || f.Exists {
		t.Errorf("created %s: %v", path, err)
	}
	// A keymap that reads as the one saved stays as it is written.
	for _, text := range []string{"default", "Default", "borovaka/zmk-defy-wireless"} {
		t.Run(text, func(t *testing.T) {
			path := writeSettings(t, "keymap: "+text+" # same\n")
			f := &File{Path: path}
			if err := f.SaveKeymap(release.Default()); err != nil {
				t.Fatal(err)
			}
			if got := readText(t, path); got != "keymap: "+text+" # same\n" || !f.Exists || f.Keymap != release.Default() {
				t.Errorf("%q, %+v", got, f)
			}
		})
	}
	// A relative path that names the same file stays as it is written.
	path = writeSettings(t, "firmware: build/fw.zip # mine\n")
	f = &File{Path: path}
	if err := f.SaveFirmware(filepath.Join(filepath.Dir(path), "build", "fw.zip")); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, path); got != "firmware: build/fw.zip # mine\n" {
		t.Errorf("%q", got)
	}
}

func TestSaveReplacesTheFile(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(real, []byte("keymap: default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.Symlink(real, path); err != nil {
		t.Skip("no symbolic links:", err)
	}
	f, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SaveKeymap(source(t, "alice/x")); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(path); err != nil || target != real {
		t.Errorf("the link points to %q, %v", target, err)
	}
	if got := readText(t, real); got != "keymap: alice/x\n" {
		t.Errorf("%q", got)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("the folder holds %d files", len(entries))
	}
}

func TestReadRefuses(t *testing.T) {
	const hint = "; write Windows paths in single quotes, like firmware: 'C:\\Users\\alice\\firmware.zip'"
	for in, want := range map[string]string{
		"keymap: default\n\n# x\nkeymp: alice/x\n": `:4: unknown setting "keymp"; the settings are keymap and firmware`,
		"? [a]\n: b\n":        ":1: unknown setting; the settings are keymap and firmware",
		"<<: {keymap: x/y}\n": `:1: unknown setting "<<"`,
		"# a\r\n# b\r\nkeymap: alice\r\n": `:3: keymap "alice": it has no / between the owner and the repository; ` +
			"leave it empty, or write it like alice/my-defy, or default@v0.1.0 to stay on one release",
		"keymap: https://github.com/alice/my-defy\n":  "the installer takes the repository's owner/repo, not its address",
		"keymap: alice/x@\n":                          "the tag after @ is empty",
		"keymap: 123\n":                               `keymap "123": it has no / between`,
		"keymap: default\nfirmware: x\nkeymap: a/b\n": ":3: keymap is set twice, here and on line 1; keep one",
		"firmware:\n  - a\n":                          `:2: firmware is a list; give it one value, plain or in quotes, like firmware: ''`,
		"firmware: {a: b}\n":                          ":1: firmware is a group of settings; give it one value",
		"keymap: |\n  default\n":                      ":1: keymap is a block of text, after | or >; give it one value",
		"keymap: >\n  default\n":                      ":1: keymap is a block of text",
		"keymap: !!str default\n":                     ":1: keymap has a tag, !!str; give it one value",
		"keymap: &a default\nfirmware: *a\n":          ":1: keymap has an anchor, &a; give it one value",
		"- keymap\n":                                  ":1: this is not a setting; put each setting on a line of its own, like keymap: 'default'",
		"default\n":                                   ":1: this is not a setting",
		"keymap: default\n---\nkeymap: alice/x\n":     ":2: a second YAML document starts here; the settings file holds one",
		"keymap: d\xe9fault\n":                        "is not UTF-8 text; save it as UTF-8",
		"\xff\xfek\x00e\x00y\x00":                     "is not UTF-8 text",

		// Each setting on a line of its own.
		"{firmware: \"/a\", keymap: x/y}\n": ":1: the settings are in braces; put each setting on a line of its own, like keymap: 'default'",
		"firmware: /a\n  /b\nkeymap: x/y\n": ":1: firmware runs over more than one line; put each setting on a line of its own, like firmware: ''",
		"keymap:\n  default\n":              ":1: keymap runs over more than one line",
		"keymap: \"x/\u2028y\"\n":           ":1: keymap runs over more than one line",

		// A Windows path in double quotes.
		"keymap: default\nfirmware: \"C:\\Users\"\n": ":2: did not find expected hexdecimal number" + hint,
		"firmware: \"C:\\data\"\n":                   ":1: found unknown escape character" + hint,
		"firmware: \"D:\\new\\firmware.zip\"\n":      `:1: firmware "D:\new\firmware.zip" holds a control character, as a backslash in double quotes gives` + hint,
		"firmware: \"C:\\Lab\"\n":                    `:1: firmware "C:\u2028ab" holds a control character`,
		"firmware: \"C:\\temp\"\n":                   `:1: firmware "C:\temp" holds a control character`,
		"firmware: '/a\tb'\n":                        `:1: firmware "/a\tb" holds a control character; delete it`,

		// The parser's lines.
		"keymap: \"default\n":                 ":2: found unexpected end of stream",
		"keymap: a/b\nfirmware: ]\n":          ":2: did not find expected node content",
		"keymap: ]\n":                         ":1: did not find expected node content",
		"keymap: a/b\n...\nb\n":               ":3: did not find expected <document start>",
		"keymap: *x\n":                        ": unknown anchor 'x' referenced",
		"keymap: a\x01b\n":                    ":1: the file holds the character U+0001, which YAML does not allow; delete it",
		"# a\r\n# b\u0085# c\nkeymap: \x7f\n": ":4: the file holds the character U+007F",
		"keymap: '\ufffe'\n":                  ":1: the file holds the character U+FFFE",
	} {
		t.Run(in, func(t *testing.T) {
			path := writeSettings(t, in)
			_, err := read(path)
			if strings.HasPrefix(want, ":") {
				want = path + want
			}
			if err == nil || !strings.HasPrefix(err.Error(), path) || !strings.Contains(err.Error(), want) {
				t.Errorf("error %v, want %q", err, want)
			}
		})
	}
}

// A firmware path that is not absolute is taken from the settings file's
// folder, and ~ from the user's home folder.
func TestReadResolvesTheFirmwarePath(t *testing.T) {
	home := t.TempDir()
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = os.UserHomeDir })
	path := writeSettings(t, "")
	dir := filepath.Dir(path)
	abs := filepath.Join(t.TempDir(), "firmware.zip")

	tests := map[string]string{
		"firmware.zip":           filepath.Join(dir, "firmware.zip"),
		"build/firmware.zip":     filepath.Join(dir, "build", "firmware.zip"),
		"'../firmware.zip'":      filepath.Join(filepath.Dir(dir), "firmware.zip"),
		"~/Downloads/fw.zip":     filepath.Join(home, "Downloads", "fw.zip"),
		"'~'":                    home,
		"~alice/fw.zip":          filepath.Join(dir, "~alice", "fw.zip"),
		"'" + abs + "'":          abs,
		"''":                     "",
		"'firmware (1).zip' # x": filepath.Join(dir, "firmware (1).zip"),
	}
	if runtime.GOOS == "windows" {
		tests[`'\fw.zip'`] = filepath.VolumeName(dir) + `\fw.zip`
		tests[`'~\fw.zip'`] = filepath.Join(home, "fw.zip")
	}
	for value, want := range tests {
		t.Run(value, func(t *testing.T) {
			writeText(t, path, "firmware: "+value+"\n")
			f, err := read(path)
			if err != nil {
				t.Fatal(err)
			}
			if f.Firmware != want {
				t.Errorf("firmware %q, want %q", f.Firmware, want)
			}
		})
	}

	if runtime.GOOS == "windows" {
		writeText(t, path, "firmware: 'C:fw.zip'\n")
		_, err := read(path)
		if err == nil || !strings.Contains(err.Error(), path+`:1: firmware "C:fw.zip" names a drive but not the folder on it`) {
			t.Errorf("error %v", err)
		}
	}
	userHomeDir = func() (string, error) { return "", errors.New("$HOME is not defined") }
	writeText(t, path, "\nfirmware: ~/fw.zip\n")
	_, err := read(path)
	if err == nil || !strings.Contains(err.Error(), path+`:2: firmware "~/fw.zip" starts at your home folder, ~, `+
		"and the installer cannot find it: $HOME is not defined; give the full path") {
		t.Errorf("error %v", err)
	}
}

func TestLoad(t *testing.T) {
	// The executable's folder with its links resolved, as macOS's
	// temporary folder is a link.
	exeDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	exe := filepath.Join(exeDir, "defy-installer")
	writeText(t, exe, "")
	executable = func() (string, error) { return exe, nil }
	userConfigDir = func() (string, error) { return configDir, nil }
	t.Cleanup(func() { executable, userConfigDir = os.Executable, os.UserConfigDir })
	inConfig := filepath.Join(configDir, "defy-installer", FileName)

	// Neither: the config folder, where saving creates the file.
	f, err := Load()
	if err != nil || f.Path != inConfig || f.Exists || !f.Keymap.IsZero() || f.Firmware != "" {
		t.Fatalf("%+v, %v", f, err)
	}
	if err := f.SaveKeymap(source(t, "alice/x")); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, inConfig); got != strings.Replace(string(template), `keymap: ''`, `keymap: 'alice/x'`, 1) {
		t.Errorf("created\n%s", got)
	}

	// The config folder's file.
	if f, err = Load(); err != nil || f.Path != inConfig || !f.Exists || f.Keymap != source(t, "alice/x") {
		t.Errorf("%+v, %v", f, err)
	}

	// The file next to the executable comes first.
	beside := filepath.Join(exeDir, FileName)
	writeText(t, beside, "keymap: default\n")
	if f, err = Load(); err != nil || f.Path != beside || f.Keymap != source(t, "default") {
		t.Errorf("%+v, %v", f, err)
	}

	// Through a symbolic link to the executable.
	link := filepath.Join(t.TempDir(), "defy-installer")
	if err := os.Symlink(exe, link); err == nil {
		executable = func() (string, error) { return link, nil }
		if f, err = Load(); err != nil || f.Path != beside {
			t.Errorf("through a link: %+v, %v", f, err)
		}
	}

	// An executable whose links cannot be resolved, as when it was
	// deleted while it runs: the folder it was in.
	executable = func() (string, error) { return filepath.Join(exeDir, "deleted"), nil }
	if f, err = Load(); err != nil || f.Path != beside {
		t.Errorf("a deleted executable: %+v, %v", f, err)
	}

	// Without the executable's path.
	executable = func() (string, error) { return "", errors.New("no executable") }
	if f, err = Load(); err != nil || f.Path != inConfig {
		t.Errorf("%+v, %v", f, err)
	}

	// Without a config folder either.
	userConfigDir = func() (string, error) { return "", errors.New("$HOME is not defined") }
	if _, err = Load(); err == nil || !strings.Contains(err.Error(), "$HOME is not defined") {
		t.Errorf("error %v", err)
	}

	// A broken file is an error, not a reason to look further.
	executable = func() (string, error) { return exe, nil }
	writeText(t, beside, "keymp: x\n")
	if _, err = Load(); err == nil || !strings.Contains(err.Error(), beside+":1: unknown setting") {
		t.Errorf("error %v", err)
	}
}
