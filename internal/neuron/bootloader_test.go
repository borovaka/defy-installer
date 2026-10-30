// SPDX-License-Identifier: MIT

package neuron

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
	nt "github.com/borovaka/defy-installer/internal/neuron/neurontest"
)

var identification = fmt.Sprintf("C%08X,%08X,%08X#", bootloaderStart, bootloaderLength, bootloaderCRC32)

// ramp is the bytes 0x00 to 0xff, n times over.
func ramp(n int) []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return bytes.Repeat(b, n)
}

func zmkImage(t testing.TB, spans ...ft.Span) *firmware.NeuronImage {
	t.Helper()
	im, err := firmware.ParseNeuronImage("zmk.hex", ft.Hex(spans...))
	if err != nil {
		t.Fatal(err)
	}
	return im
}

// pagesOf lays spans out in the Neuron's pages and returns those from
// NeuronCodeStart up, and where the data ends.
func pagesOf(spans ...ft.Span) ([]firmware.Page, uint32) {
	laid, end := ft.Pages(firmware.NeuronPageSize, spans...)
	var pages []firmware.Page
	for _, p := range laid {
		if p.Addr >= firmware.NeuronCodeStart {
			pages = append(pages, firmware.Page(p))
		}
	}
	return pages, end
}

// stockNeuron builds Dygma's firmware from stand-ins for its files, as
// ParseStockNeuron would without the pins, which only the real files pass.
func stockNeuron(t testing.TB, app, s140 []ft.Span) *firmware.StockNeuron {
	t.Helper()
	pages, end := pagesOf(app...)
	sdPages, sdEnd := pagesOf(s140...)
	return &firmware.StockNeuron{Name: "Wireless_neuron.hex", End: end, Pages: pages,
		SoftDevice: &firmware.SoftDevice{Name: "s140.hex", End: sdEnd, Pages: sdPages}}
}

// s140End is where the real S140 file's data ends.
const s140End = 0x26634

// testStock has an application from 0x27000 with a gap, and an S140 of
// the real one's extent, whose file starts with MBR records.
func testStock(t testing.TB) *firmware.StockNeuron {
	mbr := make([]byte, 0xB00)
	for i := range mbr {
		mbr[i] = byte(i*13 + 1)
	}
	return stockNeuron(t,
		[]ft.Span{ft.At(0x27000, ft.NeuronProgram(0x2800, 5, 0x27000)), ft.At(0x2A000, ramp(2))},
		[]ft.Span{ft.At(0, mbr), ft.At(0x1000, ft.NeuronProgram(s140End-0x1000, 9, 0x1000))})
}

func addrs(pages []firmware.Page) []uint32 {
	var a []uint32
	for _, p := range pages {
		a = append(a, p.Addr)
	}
	return a
}

func erased(b []byte) bool {
	return bytes.Count(b, []byte{0xff}) == len(b)
}

// unchanged checks that the MBR and everything from Dygma's settings up
// are as they were.
func unchanged(t *testing.T, f *nt.Bootloader, before []byte) {
	t.Helper()
	if !bytes.Equal(f.Flash[:0x1000], before[:0x1000]) {
		t.Error("the MBR changed")
	}
	if !bytes.Equal(f.Flash[0x72000:], before[0x72000:]) {
		t.Error("flash from 0x72000 up changed")
	}
}

func commandsStarting(f *nt.Bootloader, prefix string) []string {
	var found []string
	for _, c := range f.Commands {
		if strings.HasPrefix(c, prefix) {
			found = append(found, c)
		}
	}
	return found
}

func wantError(t *testing.T, err error, contains ...string) {
	t.Helper()
	if err == nil {
		t.Errorf("no error, want one containing %q", contains)
		return
	}
	for _, s := range contains {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not contain %q", err, s)
		}
	}
}

func TestWriteGoesByAddressWithTheVectorPageLast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := ft.NeuronProgram(0x37EDC-0x1000, 3, 0x1000)
		second := ramp(3)
		im := zmkImage(t, ft.At(0x1000, first), ft.At(0x37EE0, second))
		f := nt.New(t)
		before := bytes.Clone(f.Flash)
		b := NewBootloaderClient(f)
		if err := b.Write(t.Context(), im, false, nil); err != nil {
			t.Fatal(err)
		}
		if err := b.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		if f.Commands[0] != identification || f.Commands[1] != "E00001000,00071000#" {
			t.Errorf("began with %q", f.Commands[:2])
		}
		if f.Writes[len(f.Writes)-1] != 0x1000 {
			t.Errorf("the last page written is 0x%x", f.Writes[len(f.Writes)-1])
		}
		if got := slices.Sorted(slices.Values(f.Writes)); !slices.Equal(got, addrs(im.Pages)) {
			t.Errorf("wrote %x, want %x", got, addrs(im.Pages))
		}
		if f.Commands[len(f.Commands)-1] != "S#" || !f.Started {
			t.Error("not started")
		}
		if !bytes.Equal(f.Flash[0x1000:0x1000+len(first)], first) || !bytes.Equal(f.Flash[0x37EE0:0x37EE0+len(second)], second) {
			t.Error("the image is not in flash")
		}
		if !erased(f.Flash[0x37EDC:0x37EE0]) {
			t.Error("the gap is not erased")
		}
		unchanged(t, f, before)
		if checks := commandsStarting(f, "C")[1:]; len(checks) != len(im.Pages) {
			t.Errorf("%d checks for %d pages", len(checks), len(im.Pages))
		}
	})
}

func TestWriteErasesTheCodeAndSettingsOrTheCodeAlone(t *testing.T) {
	im := zmkImage(t, ft.At(0x1000, ft.NeuronProgram(8192, 3, 0x1000)))
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprint("keepSettings=", keep), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := nt.New(t)
				before := bytes.Clone(f.Flash)
				if err := NewBootloaderClient(f).Write(t.Context(), im, keep, nil); err != nil {
					t.Fatal(err)
				}
				want := "E00001000,00071000#"
				if keep {
					want = "E00001000,0006B000#"
				}
				if got := commandsStarting(f, "E"); !slices.Equal(got, []string{want}) {
					t.Errorf("erased with %q, want %q", got, want)
				}
				if !slices.Equal(f.Writes, []uint32{0x2000, 0x1000}) {
					t.Errorf("wrote %x", f.Writes)
				}
				unchanged(t, f, before)
				if keep && !bytes.Equal(f.Flash[0x6C000:], before[0x6C000:]) {
					t.Error("the ZMK settings changed")
				}
				if !keep && !erased(f.Flash[0x6C000:0x72000]) {
					t.Error("the ZMK settings are not erased")
				}
			})
		})
	}
}

func TestKeepingSettingsRefusesAnOverlappingImageBeforeAnyCommand(t *testing.T) {
	im := zmkImage(t, ft.At(0x1000, ft.NeuronProgram(4096, 3, 0x1000)), ft.At(0x6C000, make([]byte, 16)))
	f := nt.New(t)
	err := NewBootloaderClient(f).Write(t.Context(), im, true, nil)
	wantError(t, err, "reaches into the ZMK settings")
	if len(f.Commands) != 0 {
		t.Errorf("sent %q", f.Commands)
	}
}

func TestOtherBootloaderIsNeverErasedOrWritten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := nt.New(t)
		f.Other = true
		err := NewBootloaderClient(f).Write(t.Context(), zmkImage(t, ft.At(0x1000, ft.NeuronProgram(4096, 3, 0x1000))), false, nil)
		wantError(t, err, "not Dygma's n2_bootloader 1.0.0, so nothing was written")
		if !errors.Is(err, errRejected) || errors.Is(err, ErrPartialImage) {
			t.Errorf("got %v", err)
		}
		if !slices.Equal(f.Commands, []string{identification}) {
			t.Errorf("sent %q", f.Commands)
		}
	})
}

func TestWriteErrorsAndMissingRepliesGiverecoveryGuidance(t *testing.T) {
	im := zmkImage(t, ft.At(0x1000, ft.NeuronProgram(8192, 3, 0x1000)))
	for _, tt := range []struct {
		failOn, silentOn string
		want             error
		waited           time.Duration
	}{
		{failOn: "W", want: errRejected},
		{failOn: "C00002000", want: errRejected},
		{silentOn: "E", want: errNoAck, waited: 60 * time.Second},
		{silentOn: "U", want: errNoAck, waited: 2 * time.Second},
	} {
		t.Run(tt.failOn+tt.silentOn, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := nt.New(t)
				f.FailOn, f.SilentOn = tt.failOn, tt.silentOn
				start := time.Now()
				err := NewBootloaderClient(f).Write(t.Context(), im, false, nil)
				if !errors.Is(err, tt.want) || !errors.Is(err, ErrPartialImage) {
					t.Errorf("got %v", err)
				}
				wantError(t, err, recoveryGuidance)
				if waited := time.Since(start); waited != tt.waited {
					t.Errorf("waited %v, want %v", waited, tt.waited)
				}
				if f.Started || slices.Contains(f.Writes, 0x1000) {
					t.Errorf("wrote %x, started %v", f.Writes, f.Started)
				}
			})
		})
	}
}

func TestWriteIsInterruptedBetweenPagesOnly(t *testing.T) {
	stock := testStock(t)
	synctest.Test(t, func(t *testing.T) {
		f := nt.New(t)
		ctx, cancel := context.WithCancel(t.Context())
		f.Handled = func(cmd string) {
			if strings.HasPrefix(cmd, "U") && len(f.Writes) == 2 {
				cancel()
			}
		}
		err := NewBootloaderClient(f).WriteStock(ctx, stock, nil)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrPartialImage) {
			t.Errorf("got %v", err)
		}
		wantError(t, err, recoveryGuidance)
		// The page under way when the interrupt came is written and checked.
		if !slices.Equal(f.Writes, []uint32{0x27000, 0x28000, 0x29000}) {
			t.Errorf("wrote %x", f.Writes)
		}
		if last := f.Commands[len(f.Commands)-1]; !strings.HasPrefix(last, "C00029000,") {
			t.Errorf("the last command is %q", last)
		}
		if f.Started || !erased(f.Flash[0x1000:0x2000]) {
			t.Error("the vector page is not erased")
		}
	})
}

func TestWriteInterruptedBeforeTheEraseSendsNothingMore(t *testing.T) {
	im := zmkImage(t, ft.At(0x1000, ft.NeuronProgram(4096, 3, 0x1000)))
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		f := nt.New(t)
		f.Handled = func(string) { cancel() }
		err := NewBootloaderClient(f).Write(ctx, im, false, nil)
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrPartialImage) {
			t.Errorf("got %v", err)
		}
		if !slices.Equal(f.Commands, []string{identification}) {
			t.Errorf("sent %q", f.Commands)
		}
	})
}

func TestEveryEraseAndWriteIsWholePagesWithinZMKCodeAndSettings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := nt.New(t)
		b := NewBootloaderClient(f)
		for _, r := range []struct{ addr, length uint32 }{
			{0x0, 0x1000}, {0x0, 0x72000}, {0x800, 0x1000}, {0x1000, 0x800}, {0x1800, 0x1000},
			{0x1000, 0}, {0x71000, 0x2000}, {0x72000, 0x1000}, {0x75000, 0x1000},
			{0x77000, 0x1000}, {0x7F000, 0x1000}, {0xFFFFF000, 0x2000},
		} {
			wantError(t, b.erase(r.addr, int(r.length)), "nothing was sent")
			wantError(t, b.writePage(firmware.Page{Addr: r.addr, Data: make([]byte, r.length)}), "nothing was sent")
			if len(f.Commands) != 0 {
				t.Fatalf("0x%x+0x%x: sent %q", r.addr, r.length, f.Commands)
			}
		}
		if err := b.erase(0x1000, 0x71000); err != nil {
			t.Fatal(err)
		}
		if err := b.writePage(firmware.Page{Addr: 0x71000, Data: make([]byte, 4096)}); err != nil {
			t.Fatal(err)
		}
		if f.Commands[0] != "E00001000,00071000#" || !slices.Equal(f.Writes, []uint32{0x71000}) {
			t.Errorf("sent %q, wrote %x", f.Commands, f.Writes)
		}
	})
}

func TestStockRestoreWritesTheApplicationThenS140WithItsVectorPageLast(t *testing.T) {
	stock := testStock(t)
	synctest.Test(t, func(t *testing.T) {
		f := nt.New(t)
		before := bytes.Clone(f.Flash)
		type step struct{ done, total int }
		var progress []step
		b := NewBootloaderClient(f)
		err := b.WriteStock(t.Context(), stock, func(done, total int) { progress = append(progress, step{done, total}) })
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		if f.Commands[0] != identification || f.Commands[1] != "E00001000,00071000#" {
			t.Errorf("began with %q", f.Commands[:2])
		}
		if erases := commandsStarting(f, "E"); len(erases) != 1 {
			t.Errorf("erased with %q", erases)
		}
		if f.Commands[len(f.Commands)-1] != "S#" {
			t.Error("not started")
		}
		want := []uint32{0x27000, 0x28000, 0x29000, 0x2A000}
		for a := uint32(0x2000); a < 0x27000; a += 0x1000 {
			want = append(want, a)
		}
		want = append(want, 0x1000)
		if !slices.Equal(f.Writes, want) {
			t.Errorf("wrote %x, want %x", f.Writes, want)
		}
		for _, p := range append(slices.Clone(stock.Pages), stock.SoftDevice.Pages...) {
			if !bytes.Equal(f.Flash[p.Addr:p.Addr+4096], p.Data) {
				t.Errorf("0x%x is not the image's", p.Addr)
			}
			i := slices.Index(f.Commands, fmt.Sprintf("W%08X,00001000#", p.Addr))
			if want := fmt.Sprintf("C%08X,00001000,%08X#", p.Addr, crc32.ChecksumIEEE(p.Data)); i < 0 || f.Commands[i+1] != want {
				t.Errorf("0x%x is not checked after its write", p.Addr)
			}
		}
		unchanged(t, f, before)
		if !erased(f.Flash[0x2B000:0x72000]) {
			t.Error("0x2b000..0x72000 is not erased")
		}
		if len(progress) == 0 || progress[0] != (step{0, 42}) || progress[len(progress)-1] != (step{42, 42}) {
			t.Errorf("progress %v", progress)
		}
	})
}

func TestInterruptedStockRestoreNeverWritesTheVectorPage(t *testing.T) {
	stock := testStock(t)
	for _, tt := range []struct{ failOn, silentOn string }{
		{failOn: "W00010000"}, {failOn: "C00027000"}, {silentOn: "U"},
	} {
		t.Run(tt.failOn+tt.silentOn, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := nt.New(t)
				f.FailOn, f.SilentOn = tt.failOn, tt.silentOn
				err := NewBootloaderClient(f).WriteStock(t.Context(), stock, nil)
				if !errors.Is(err, ErrPartialImage) {
					t.Errorf("got %v", err)
				}
				wantError(t, err, recoveryGuidance)
				if slices.Contains(f.Writes, 0x1000) || !erased(f.Flash[0x1000:0x2000]) || f.Started {
					t.Errorf("wrote %x, started %v", f.Writes, f.Started)
				}
			})
		})
	}
}

func TestS140CheckComparesEveryPageWithoutErasingOrWriting(t *testing.T) {
	sd := testStock(t).SoftDevice
	synctest.Test(t, func(t *testing.T) {
		f := nt.New(t)
		for _, p := range sd.Pages {
			copy(f.Flash[p.Addr:], p.Data)
		}
		f.Flash[0x5100] ^= 0xff
		check, err := NewBootloaderClient(f).CheckS140(t.Context(), sd)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{identification}
		for _, p := range sd.Pages {
			want = append(want, fmt.Sprintf("C%08X,00001000,%08X#", p.Addr, crc32.ChecksumIEEE(p.Data)))
		}
		if !slices.Equal(f.Commands, want) {
			t.Errorf("sent %q", f.Commands)
		}
		if !slices.Equal(check.differing, []uint32{0x5000}) || len(check.matching) != 37 || check.Matches() {
			t.Errorf("got %+v", check)
		}
		if got := check.String(); got != "S140 differs on 1 of 38 pages (absent or not 7.2.0): 0x05000" {
			t.Errorf("got %q", got)
		}

		f.Flash[0x5100] ^= 0xff
		check, err = NewBootloaderClient(f).CheckS140(t.Context(), sd)
		if err != nil {
			t.Fatal(err)
		}
		if got := check.String(); !check.Matches() || got != "S140 matches Dygma's S140 7.2.0 on all 38 pages" {
			t.Errorf("got %q", got)
		}
		if len(f.Writes) != 0 {
			t.Errorf("wrote %x", f.Writes)
		}
	})
}

func TestS140CheckStopsOnSilence(t *testing.T) {
	sd := testStock(t).SoftDevice
	synctest.Test(t, func(t *testing.T) {
		f := nt.New(t)
		f.SilentOn = "C00010000"
		_, err := NewBootloaderClient(f).CheckS140(t.Context(), sd)
		if !errors.Is(err, errNoAck) {
			t.Errorf("got %v", err)
		}
		wantError(t, err, "no acknowledgement", "nothing was erased or written")
		if len(f.Commands) != 17 || !strings.HasPrefix(f.Commands[16], "C00010000") {
			t.Errorf("sent %q", f.Commands)
		}
		for _, c := range f.Commands {
			if c[0] != 'C' {
				t.Errorf("sent %q", c)
			}
		}
	})
}

func TestStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := nt.New(t)
		if err := NewBootloaderClient(f).Start(t.Context()); err != nil || !f.Started {
			t.Errorf("got %v, started %v", err, f.Started)
		}

		f = nt.New(t)
		f.SilentOn = "S"
		start := time.Now()
		err := NewBootloaderClient(f).Start(t.Context())
		if !errors.Is(err, errNoAck) {
			t.Errorf("got %v", err)
		}
		if waited := time.Since(start); waited != time.Second {
			t.Errorf("waited %v", waited)
		}
	})
}

// line is a port whose device has sent reply, and whose reads fail with
// readErr when it is set.
type line struct {
	written []byte
	reply   []byte
	readErr error
}

func (l *line) Read(p []byte) (int, error) {
	if l.readErr != nil {
		return 0, l.readErr
	}
	if len(l.reply) == 0 {
		time.Sleep(50 * time.Millisecond)
		return 0, os.ErrDeadlineExceeded
	}
	n := copy(p, l.reply)
	l.reply = l.reply[n:]
	return n, nil
}

func (l *line) Write(p []byte) (int, error) {
	l.written = append(l.written, p...)
	return len(p), nil
}

func TestCommandReadsItsReplyAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &line{reply: []byte("ACK\r\nERR\r\n")}
		b := NewBootloaderClient(l)
		if err := b.command("S#", time.Second); err != nil {
			t.Fatal(err)
		}
		if string(l.reply) != "ERR\r\n" {
			t.Errorf("left %q", l.reply)
		}
		if err := b.command("S#", time.Second); !errors.Is(err, errRejected) {
			t.Errorf("got %v", err)
		}
		wantError(t, b.command("S#", time.Second), "S#: ")
	})
}

func TestCommandRefusesOtherReplies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		long := strings.Repeat("A", 17) + "\r\n"
		l := &line{reply: []byte(long)}
		start := time.Now()
		err := NewBootloaderClient(l).command("S#", time.Second)
		if !errors.Is(err, errNoAck) || time.Since(start) != 0 {
			t.Errorf("got %v after %v", err, time.Since(start))
		}
		if string(l.reply) != "\r\n" {
			t.Errorf("read past 17 bytes, left %q", l.reply)
		}

		l = &line{reply: []byte("NAK\r\n")}
		err = NewBootloaderClient(l).command("S#", time.Second)
		if !errors.Is(err, errNoAck) || errors.Is(err, errRejected) {
			t.Errorf("got %v", err)
		}
		wantError(t, err, `"NAK\r\n"`)

		l = &line{reply: []byte("AC")}
		err = NewBootloaderClient(l).command("S#", time.Second)
		if !errors.Is(err, errNoAck) {
			t.Errorf("got %v", err)
		}
		wantError(t, err, `received "AC"`)
	})
}

func TestCommandPortErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gone := errors.New("the device is gone")
		l := &line{readErr: gone}
		err := NewBootloaderClient(l).command("S#", time.Second)
		if !errors.Is(err, gone) || errors.Is(err, errNoAck) {
			t.Errorf("got %v", err)
		}
		if err := NewBootloaderClient(l).Identify(t.Context()); !errors.Is(err, gone) || strings.Contains(err.Error(), "not Dygma's") {
			t.Errorf("identification: got %v", err)
		}
	})
}
