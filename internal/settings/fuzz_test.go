// SPDX-License-Identifier: MIT

package settings

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/borovaka/defy-installer/internal/release"
)

// FuzzEdit checks the settings editor. A save gives a file that reads as
// the values saved, or refuses and leaves the file as it was. Values the
// settings take, saved in place of values the file holds, are never
// refused, and leave the text around them as it was.
//
// It runs on its seeds with go test. To search further:
//
//	go test -run '^$' -fuzz '^FuzzEdit$' -fuzztime 1m ./internal/settings
func FuzzEdit(f *testing.F) {
	for _, tc := range saveInPlace {
		var keymap, firmware string
		for _, c := range tc.changes {
			if c.key == keymapKey {
				keymap = c.value
			} else {
				firmware = c.value
			}
		}
		f.Add(tc.in, keymap, firmware)
	}
	for value := range saveQuotes {
		f.Add("firmware: /old\n", "", value)
	}
	// More files and values: other quotes, nulls, appends and keys.
	f.Add("keymap: 'default'\nfirmware: 'C:\\old'   # Windows\n", "", `C:\Users\Alice Smith\firmware (1).zip`)
	f.Add("firmware: 'x'\n", "", "/a\u00a0b")
	f.Add("firmware: \"C:\\\\a \\\"b\\\"\" # c\n", "", "/x")
	f.Add("firmware: '/o''brien' # c\n", "", "/x")
	f.Add("keymap: \"default\"\n", "alice/my-defy", "")
	f.Add("firmware: /old\n", "", "/home/alice/defy/firmware_v2.zip")
	f.Add("keymap: ~\nfirmware: null # none\n", "alice/x", "/fw")
	f.Add("keymap: default # end", "", "/fw")
	f.Add("# only comments\n", "alice/x", "")
	f.Add("keymap:\tx/y\t# tab\n", "alice/z", "")
	f.Add("\"keymap\": x/y\n", "alice/z", "")
	f.Add("firmware: /a#b # comment\n", "", "/c")
	f.Add("firmware: \"/\u00e4\u00f6\u00fc\" # \u00e4\nkeymap: x/y # set\n", "alice/z", "/\u00fc")
	// The parser counts NEL, LS and PS as line breaks.
	f.Add("# a\u2028# b\u0085# c\u2029# d\nfirmware: '/x'\nkeymap: x/y\n", "alice/z", "/y")
	f.Fuzz(func(t *testing.T, text, keymap, firmware string) {
		dir := t.TempDir()
		path := filepath.Join(dir, FileName)
		d, err := parse(path, []byte(text))
		if err != nil {
			return
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		// The firmware path as SaveFirmware takes it, absolute.
		if firmware != "" {
			firmware = filepath.Join(dir, firmware)
		}
		src, badKeymap := release.ParseSource(keymap)
		valid := (keymap == "" || badKeymap == nil) && utf8.ValidString(firmware) &&
			!strings.ContainsFunc(firmware, notInPath)
		before, err := d.resolved()
		if err != nil {
			return
		}
		inPlace := true
		var changes []change
		for _, c := range []change{{keymapKey, keymap}, {firmwareKey, firmware}} {
			if before[c.key] != c.value {
				changes = append(changes, c)
				_, ok := d.settings[c.key]
				inPlace = inPlace && ok
			}
		}

		s := &File{Path: path}
		err = s.save(changes...)
		out, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err != nil {
			if valid && inPlace {
				t.Fatalf("saving %q in\n%q\nrefused: %v", changes, text, err)
			}
			if string(out) != text {
				t.Fatalf("a refused save changed\n%q\nto\n%q", text, out)
			}
			return
		}
		if !valid {
			t.Fatalf("saved %q in\n%q", changes, text)
		}
		back, err := read(path)
		if err != nil {
			t.Fatalf("saving %q in\n%q\ngives\n%q, which does not read: %v", changes, text, out, err)
		}
		if back.Keymap != src || back.Firmware != firmware || *back != *s {
			t.Fatalf("saving %q in\n%q\ngives\n%q, which reads as %+v", changes, text, out, back)
		}
		if !inPlace || len(changes) == 0 {
			return
		}
		// The text before the first value changed and after the last.
		first, last := len(d.text), 0
		for _, c := range changes {
			first, last = min(first, d.settings[c.key].start), max(last, d.settings[c.key].end)
		}
		first += len(d.bom)
		last += len(d.bom)
		if !bytes.HasPrefix(out, []byte(text[:first])) || !bytes.HasSuffix(out, []byte(text[last:])) {
			t.Fatalf("saving %q in\n%q\nchanges other text:\n%q", changes, text, out)
		}
	})
}
