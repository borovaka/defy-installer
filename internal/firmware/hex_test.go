// SPDX-License-Identifier: MIT

package firmware

import (
	"bytes"
	"strings"
	"testing"

	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
)

func TestParseHex(t *testing.T) {
	first := bytes.Repeat([]byte{0x11}, 40)
	second := bytes.Repeat([]byte{0x22}, 8)
	mem, err := parseHex(ft.Hex(ft.At(0x1000, first), ft.At(0x1030, second)), 0x1000, 0x2000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if start := mem.spans[0].addr; start != 0x1000 || mem.End() != 0x1038 {
		t.Errorf("start 0x%x, end 0x%x", start, mem.End())
	}
	if got, ok := mem.Bytes(0x1024, 4); !ok || !bytes.Equal(got, first[:4]) {
		t.Errorf("Bytes(0x1024, 4) = %x, %v", got, ok)
	}
	// 0x1028..0x1030 is a gap.
	for _, addr := range []uint32{0xfff, 0x1028, 0x102f, 0x1038} {
		if mem.Has(addr) {
			t.Errorf("data at 0x%x", addr)
		}
	}
	if _, ok := mem.Bytes(0x1026, 4); ok {
		t.Error("Bytes across a gap")
	}
}

func TestPagesPadGapsWithErasedBytes(t *testing.T) {
	first := ft.NeuronProgram(0x37EDC-NeuronCodeStart, 3, NeuronCodeStart)
	second := bytes.Repeat([]byte{0x5a}, 768)
	mem, err := parseHex(ft.Hex(ft.At(NeuronCodeStart, first), ft.At(0x37EE0, second)), NeuronCodeStart, NeuronSettingsEnd, 0)
	if err != nil {
		t.Fatal(err)
	}
	pages := mem.Pages(NeuronPageSize)
	flash := bytes.Repeat([]byte{0xff}, NeuronSettingsEnd)
	for i, p := range pages {
		if p.Addr != NeuronCodeStart+uint32(i)*NeuronPageSize || len(p.Data) != NeuronPageSize {
			t.Fatalf("page %d at 0x%x, %d bytes", i, p.Addr, len(p.Data))
		}
		copy(flash[p.Addr:], p.Data)
	}
	if last := pages[len(pages)-1].Addr; last != 0x38000 {
		t.Errorf("last page 0x%x", last)
	}
	if !bytes.Equal(flash[NeuronCodeStart:0x37EDC], first) ||
		!bytes.Equal(flash[0x37EDC:0x37EE0], []byte{0xff, 0xff, 0xff, 0xff}) ||
		!bytes.Equal(flash[0x37EE0:0x37EE0+768], second) {
		t.Error("pages do not hold the image")
	}
}

func TestParseHexBounds(t *testing.T) {
	data := bytes.Repeat([]byte{1}, 0x20)
	for _, addr := range []uint32{0, 0xFE0, 0xFF0, 0x1FF0, 0x2000} {
		if _, err := parseHex(ft.Hex(ft.At(addr, data)), 0x1000, 0x2000, 0); err == nil {
			t.Errorf("data at 0x%x accepted", addr)
		}
	}
	for _, addr := range []uint32{0x1000, 0x1FE0} {
		if _, err := parseHex(ft.Hex(ft.At(addr, data)), 0x1000, 0x2000, 0); err != nil {
			t.Errorf("data at 0x%x: %v", addr, err)
		}
	}
}

func TestParseHexRejectsMalformedFiles(t *testing.T) {
	valid := string(ft.Hex(ft.At(0x1000, ft.NeuronProgram(64, 3, 0x1000))))
	body := strings.TrimSuffix(valid, "\n")
	eof := ft.HexRecord(0, 1, nil)
	for name, text := range map[string]string{
		"bad checksum":            body[:len(body)-1] + "1",
		"no EOF":                  body[:strings.LastIndex(body, "\n")],
		"data after EOF":          valid + eof,
		"not hex":                 "not hex",
		"no data":                 ft.HexRecord(0, 4, []byte{0x10, 0x00}) + "\n" + eof,
		"unknown record type":     ft.HexRecord(0, 7, nil) + "\n" + eof,
		"extended address offset": ft.HexRecord(1, 4, []byte{0x10, 0x00}) + "\n" + eof,
		"odd digits":              ":0",
		"length mismatch":         ":0500000000FB",
	} {
		if _, err := parseHex([]byte(text), 0, 0xFFFFFFFF, 0); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseHexRejectsOverlaps(t *testing.T) {
	rows := strings.Split(string(ft.Hex(ft.At(0x1000, ft.NeuronProgram(64, 3, 0x1000)))), "\n")
	// Repeat the first data record, with its extended address, further on.
	rows = append(rows[:4], append([]string{rows[0], rows[1]}, rows[4:]...)...)
	_, err := parseHex([]byte(strings.Join(rows, "\n")), 0, 0x2000, 0)
	wantError(t, err, "line 6: overlapping data at 0x1000")
}

func TestParseHexLineEndings(t *testing.T) {
	text := string(ft.Hex(ft.At(0x1000, ft.NeuronProgram(64, 3, 0x1000))))
	for _, ending := range []string{"\r\n", "\r"} {
		mem, err := parseHex([]byte(strings.ReplaceAll(text, "\n", ending)), 0, 0x2000, 0)
		if err != nil || mem.End() != 0x1040 {
			t.Errorf("%q: %v", ending, err)
		}
	}
}

func TestParseHexSkipsRecordsBelow(t *testing.T) {
	text := ft.Hex(ft.At(0, bytes.Repeat([]byte{1}, 0x20)), ft.At(0x1000, ft.NeuronProgram(64, 3, 0x1000)))
	mem, err := parseHex(text, 0x1000, 0x2000, 0x1000)
	if err != nil || mem.spans[0].addr != 0x1000 {
		t.Fatalf("got %v", err)
	}
	// A record that reaches past skip is kept, and then lies out of bounds.
	text = ft.Hex(ft.At(0xFF8, bytes.Repeat([]byte{1}, 16)), ft.At(0x1008, ft.NeuronProgram(64, 3, 0x1000)))
	if _, err := parseHex(text, 0x1000, 0x2000, 0x1000); err == nil {
		t.Error("record across skip accepted")
	}
}

func FuzzParseHex(f *testing.F) {
	f.Add(ft.Hex(ft.At(0x1000, ft.NeuronProgram(64, 3, 0x1000)), ft.At(0x2000, []byte{1, 2, 3})))
	f.Add([]byte(":020000040001F9\n:0100000000FF\n:00000001FF\n"))
	f.Fuzz(func(t *testing.T, text []byte) {
		mem, err := parseHex(text, 0x100, 0x20000, 0)
		if err != nil {
			return
		}
		pages := mem.Pages(NeuronPageSize)
		if start := mem.spans[0].addr; start < 0x100 || mem.End() > 0x20000 || len(pages) == 0 {
			t.Errorf("start 0x%x, end 0x%x, %d pages", start, mem.End(), len(pages))
		}
	})
}
