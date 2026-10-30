// SPDX-License-Identifier: MIT

package firmware

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"testing"

	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
)

func TestNeuronImage(t *testing.T) {
	raw := ft.Hex(ft.At(NeuronCodeStart, ft.NeuronProgram(0x1800, 3, NeuronCodeStart)), ft.At(0x5000, []byte{1, 2, 3}))
	im, err := ParseNeuronImage("zmk.hex", raw)
	if err != nil {
		t.Fatal(err)
	}
	if im.End != 0x5003 || im.SHA256 != sha256.Sum256(raw) {
		t.Errorf("end 0x%x", im.End)
	}
	var addrs []uint32
	for _, p := range im.Pages {
		addrs = append(addrs, p.Addr)
	}
	if want := []uint32{0x1000, 0x2000, 0x5000}; !slices.Equal(addrs, want) {
		t.Errorf("pages %x, want %x", addrs, want)
	}
	if tail := im.Pages[1].Data[0x800:]; !bytes.Equal(tail, bytes.Repeat([]byte{0xff}, 0x800)) {
		t.Error("the gap after the program is not erased")
	}
}

// An image built with the UF2 loader starts with the loader's vector
// table, whose reset vector points into the loader, and has ZMK behind it
// at 0xD000. It passes the checks an older image, with ZMK at 0x1000,
// passes, and keeps the settings as well.
func TestNeuronImageWithTheLoader(t *testing.T) {
	const zmkStart = 0xD000
	raw := ft.Hex(ft.At(NeuronCodeStart, ft.NeuronProgram(0x2800, 3, NeuronCodeStart)),
		ft.At(zmkStart, ft.NeuronProgram(0x9000, 5, zmkStart)))
	im, err := ParseNeuronImage("neuron-hybrid.hex", raw)
	if err != nil {
		t.Fatal(err)
	}
	if im.End != zmkStart+0x9000 || len(im.Pages) != 3+9 || im.Pages[0].Addr != NeuronCodeStart ||
		im.Pages[3].Addr != zmkStart {
		t.Errorf("end 0x%x, %d pages", im.End, len(im.Pages))
	}
	if end, err := im.EraseEnd(true); end != NeuronSettingsStart || err != nil {
		t.Errorf("keeping the settings: 0x%x, %v", end, err)
	}
	// ZMK alone, as the loader's UF2 file holds it, is not an image the
	// bootloader can start.
	_, err = ParseNeuronImage("zmk.hex", ft.Hex(ft.At(zmkStart, ft.NeuronProgram(0x9000, 5, zmkStart))))
	wantError(t, err, "no vector table at 0x1000")
}

func TestNeuronImageLayoutIsChecked(t *testing.T) {
	code := ft.NeuronProgram(64, 3, NeuronCodeStart)
	zeros := make([]byte, 16)
	for name, spans := range map[string][]ft.Span{
		"below the code":        {ft.At(0xFF0, ft.NeuronProgram(64, 3, NeuronCodeStart))},
		"in Dygma's settings":   {ft.At(NeuronCodeStart, code), ft.At(NeuronSettingsEnd, zeros)},
		"in Dygma's bootloader": {ft.At(NeuronCodeStart, code), ft.At(0x75000, zeros)},
		"no vector table":       {ft.At(0x2000, code)},
		"in the MBR":            {ft.At(0x0, zeros), ft.At(NeuronCodeStart, code)},
	} {
		if _, err := ParseNeuronImage(name, ft.Hex(spans...)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err := ParseNeuronImage("s140-layout.hex", ft.Hex(ft.At(NeuronStockStart, ft.NeuronProgram(4096, 3, NeuronStockStart))))
	wantError(t, err, "no vector table at 0x1000; a build that starts after S140")
}

func TestNeuronImageVectorsAreChecked(t *testing.T) {
	for _, v := range []struct{ stack, reset uint32 }{
		{0x30000000, 0x1101}, // stack outside RAM
		{0x20000000, 0x1101}, // stack at the bottom of RAM
		{0x2001FFFE, 0x1101}, // stack not word aligned
		{0x20020000, 0x1100}, // reset not Thumb
		{0x20020000, 0x80001},
		{0x20020000, 0x27101}, // reset past the image's end
	} {
		data := ft.NeuronProgram(4096, 3, NeuronCodeStart)
		binary.LittleEndian.PutUint32(data, v.stack)
		binary.LittleEndian.PutUint32(data[4:], v.reset)
		if _, err := ParseNeuronImage("vectors.hex", ft.Hex(ft.At(NeuronCodeStart, data))); err == nil {
			t.Errorf("stack 0x%x, reset 0x%x: accepted", v.stack, v.reset)
		}
	}
	_, err := ParseNeuronImage("binary.hex", []byte{0xff, 0xfe})
	wantError(t, err, "must be an Intel HEX file")
}

func TestEraseEnd(t *testing.T) {
	small, err := ParseNeuronImage("small.hex", ft.Hex(ft.At(NeuronCodeStart, ft.NeuronProgram(8192, 3, NeuronCodeStart))))
	if err != nil {
		t.Fatal(err)
	}
	for keep, want := range map[bool]uint32{false: NeuronSettingsEnd, true: NeuronSettingsStart} {
		if end, err := small.EraseEnd(keep); end != want || err != nil {
			t.Errorf("keep %v: 0x%x, %v", keep, end, err)
		}
	}
	// An image may fill the settings on a full write, never when they are
	// kept.
	full, err := ParseNeuronImage("full.hex", ft.Hex(ft.At(NeuronCodeStart, ft.NeuronProgram(4096, 3, NeuronCodeStart)),
		ft.At(NeuronSettingsStart, make([]byte, 16))))
	if err != nil {
		t.Fatal(err)
	}
	if end, err := full.EraseEnd(false); end != NeuronSettingsEnd || err != nil {
		t.Errorf("full write: 0x%x, %v", end, err)
	}
	_, err = full.EraseEnd(true)
	wantError(t, err, "reaches into the ZMK settings")
}

// stockPair stands in for Dygma's two files: the application with a gap,
// and an S140 file with MBR records and S140's real extent.
func stockPair(t *testing.T) (app, s140 []byte) {
	app = ft.Hex(ft.At(NeuronStockStart, ft.NeuronProgram(0x2800, 5, NeuronStockStart)),
		ft.At(NeuronStockStart+0x3000, bytes.Repeat(sequence(256), 2)))
	mbr := make([]byte, 0xB00)
	for i := range mbr {
		mbr[i] = byte(i*13 + 1)
	}
	s140 = ft.Hex(ft.At(0, mbr), ft.At(NeuronCodeStart, ft.NeuronProgram(s140End-NeuronCodeStart, 9, NeuronCodeStart)))
	pin(t, &stockNeuronSHA256, app)
	pin(t, &s140SHA256, s140)
	return app, s140
}

const s140End = 0x26634

func sequence(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func TestStockNeuron(t *testing.T) {
	app, s140 := stockPair(t)
	stock, err := ParseStockNeuron("Wireless_neuron.hex", app, "s140.hex", s140)
	if err != nil {
		t.Fatal(err)
	}
	var addrs []uint32
	for _, p := range stock.Pages {
		addrs = append(addrs, p.Addr)
	}
	if want := []uint32{0x27000, 0x28000, 0x29000, 0x2A000}; !slices.Equal(addrs, want) || stock.End != 0x2A200 {
		t.Errorf("pages %x, end 0x%x", addrs, stock.End)
	}
	if !bytes.Equal(stock.Pages[2].Data[0x800:], bytes.Repeat([]byte{0xff}, 0x800)) {
		t.Error("the application's gap is not erased")
	}
	sd := stock.SoftDevice
	if len(sd.Pages) != 38 || sd.Pages[0].Addr != NeuronCodeStart || sd.Pages[37].Addr != 0x26000 || sd.End != s140End {
		t.Errorf("S140: %d pages, end 0x%x", len(sd.Pages), sd.End)
	}
	if tail := sd.Pages[37].Data[s140End-0x26000:]; !bytes.Equal(tail, bytes.Repeat([]byte{0xff}, len(tail))) {
		t.Error("S140's last page is not erased after its end")
	}
}

func TestStockNeuronPinsAreRequired(t *testing.T) {
	app, s140 := stockPair(t)
	_, err := ParseStockNeuron("s140.hex", s140, "s140.hex", s140)
	wantError(t, err, "is not Dygma's 2.3.0 Wireless_neuron.hex")
	_, err = ParseStockNeuron("app.hex", app, "app.hex", app)
	wantError(t, err, "is not Dygma's n2_s140_nrf52_7.2.0_softdevice.hex")
	_, err = ParseStockNeuron("app.hex", append(app, '\n'), "s140.hex", s140)
	wantError(t, err, "only the pinned 2.3.0 release file is accepted")
}

func TestS140LayoutIsChecked(t *testing.T) {
	code := ft.NeuronProgram(0x2000, 9, NeuronCodeStart)
	zeros := make([]byte, 16)
	for name, spans := range map[string][]ft.Span{
		"a record across the MBR's end": {ft.At(0xFF8, zeros), ft.At(0x1008, code[8:])},
		"data past S140":                {ft.At(0, zeros), ft.At(NeuronCodeStart, code), ft.At(NeuronStockStart, zeros)},
		"no vector table":               {ft.At(0, zeros), ft.At(0x2000, code)},
		"only the MBR":                  {ft.At(0, zeros)},
	} {
		raw := ft.Hex(spans...)
		pin(t, &s140SHA256, raw)
		if _, err := parseSoftDevice(name, raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStockApplicationLayoutIsChecked(t *testing.T) {
	_, s140 := stockPair(t)
	zeros := make([]byte, 16)
	for _, spans := range [][]ft.Span{
		{ft.At(0x26000, zeros), ft.At(NeuronStockStart, zeros)},
		{ft.At(0x71FF0, make([]byte, 32))},
	} {
		raw := ft.Hex(spans...)
		pin(t, &stockNeuronSHA256, raw)
		_, err := ParseStockNeuron("app.hex", raw, "s140.hex", s140)
		wantError(t, err, "0x27000..0x72000")
	}
}

func TestDygmaFirmwareIsNotAZMKImage(t *testing.T) {
	app, _ := stockPair(t)
	_, err := ParseNeuronImage("Wireless_neuron.hex", app)
	wantError(t, err, "is Dygma's 2.3.0 Neuron firmware")
}
