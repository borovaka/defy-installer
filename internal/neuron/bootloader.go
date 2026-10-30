// SPDX-License-Identifier: MIT

package neuron

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
)

// Dygma's n2_bootloader 1.0.0 lies contiguous from bootloaderStart. Its
// CRC32 must match before anything is erased or written.
const (
	bootloaderStart  = 0x77000
	bootloaderLength = 0x6F54
	bootloaderCRC32  = 0x080F9450
)

const (
	commandTimeout = 2 * time.Second
	// One erase of up to 113 pages; docs/neuron-flash.md allows 60 s.
	eraseTimeout = 60 * time.Second
	startTimeout = time.Second
	// maxReply is the longest reply read without a line end.
	maxReply = 16
)

// EntryGuidance says how to enter the bootloader with the Neuron's button.
const EntryGuidance = "Disconnect the halves' cables from the Neuron and unplug it. Of its two buttons, hold the one " +
	"farther from the USB socket, plug the Neuron back in while holding it and release after about 5 seconds; it " +
	"then shows up as USB 35ef:0013."

// recoveryGuidance says how to recover a Neuron that may hold a partial
// image; Write says why.
const recoveryGuidance = "Keep the Neuron plugged in and write it again: it waits in its bootloader until it is " +
	"unplugged, and after that it needs its button to reach the bootloader again. To put Dygma's firmware back " +
	"instead, run defy-installer uninstall. Do not use Bazecor to repair it: Bazecor writes only part of the " +
	"Neuron's flash."

var (
	// errRejected is a command the bootloader answered with ERR.
	errRejected = errors.New("the bootloader answered ERR")
	// errNoAck is a command the bootloader answered with neither ACK nor
	// ERR, or not at all in time.
	errNoAck = errors.New("no acknowledgement from the bootloader")
	// ErrPartialImage marks a write that stopped after its erase was sent.
	// The error's message ends with what to do next.
	ErrPartialImage = errors.New("the Neuron may hold a partial image")
)

// BootloaderClient writes the Neuron through Dygma's n2_bootloader 1.0.0,
// over the bootloader's serial port.
//
// Its methods check their context between commands and between pages,
// never within one page's upload, write and check, so an interrupt leaves
// the bootloader waiting for its next command.
type BootloaderClient struct {
	rw io.ReadWriter
}

// NewBootloaderClient returns a BootloaderClient that talks over rw, the
// bootloader's open port. A read from rw must wait a short time at most
// and then fail with an error matching os.ErrDeadlineExceeded, as
// usbserial.Conn's reads do.
func NewBootloaderClient(rw io.ReadWriter) *BootloaderClient {
	return &BootloaderClient{rw: rw}
}

// command sends cmd and waits up to timeout for the reply, which must be
// ACK. It reads one byte at a time, so it never takes a byte of a later
// reply.
func (b *BootloaderClient) command(cmd string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if _, err := b.rw.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("%s: %w", cmd, err)
	}
	var reply []byte
	var buf [1]byte
	for time.Now().Before(deadline) && len(reply) <= maxReply {
		n, err := b.rw.Read(buf[:])
		if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			return fmt.Errorf("%s: %w", cmd, err)
		}
		if n == 0 {
			continue
		}
		reply = append(reply, buf[0])
		if buf[0] != '\n' {
			continue
		}
		switch string(reply) {
		case "ACK\r\n":
			return nil
		case "ERR\r\n":
			return fmt.Errorf("%s: %w", cmd, errRejected)
		}
		return fmt.Errorf("%s: %w: it answered %q", cmd, errNoAck, reply)
	}
	return fmt.Errorf("%s: %w (received %q)", cmd, errNoAck, reply)
}

// upload puts data into the bootloader's buffer.
func (b *BootloaderClient) upload(data []byte) error {
	if err := b.command(fmt.Sprintf("U%08X#", len(data)), commandTimeout); err != nil {
		return err
	}
	if _, err := b.rw.Write(data); err != nil {
		return fmt.Errorf("uploading %d bytes: %w", len(data), err)
	}
	return nil
}

// Identify checks, by its CRC32, that the bootloader is Dygma's
// n2_bootloader 1.0.0.
func (b *BootloaderClient) Identify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := b.compare(bootloaderStart, bootloaderLength, bootloaderCRC32)
	if errors.Is(err, errRejected) || errors.Is(err, errNoAck) {
		return fmt.Errorf("this is not Dygma's n2_bootloader 1.0.0, so nothing was written: %w", err)
	}
	return err
}

// compare asks whether the CRC32 of n bytes of flash from addr is crc; the
// bootloader answers ERR when it is not.
func (b *BootloaderClient) compare(addr uint32, n int, crc uint32) error {
	return b.command(fmt.Sprintf("C%08X,%08X,%08X#", addr, n, crc), commandTimeout)
}

// checkedRange is the one gate for every erase and write sent: whole pages
// within NeuronCodeStart..NeuronSettingsEnd. It returns the range as the
// commands give it.
func checkedRange(addr uint32, length int) (string, error) {
	end := uint64(addr) + uint64(length)
	if addr%firmware.NeuronPageSize != 0 || length%firmware.NeuronPageSize != 0 || length <= 0 ||
		addr < firmware.NeuronCodeStart || end > firmware.NeuronSettingsEnd {
		return "", fmt.Errorf("refusing to erase or write 0x%05x..0x%05x: only whole pages within 0x%05x..0x%05x "+
			"are written, so nothing was sent for it", addr, end, firmware.NeuronCodeStart, firmware.NeuronSettingsEnd)
	}
	return fmt.Sprintf("%08X,%08X", addr, length), nil
}

func (b *BootloaderClient) erase(addr uint32, length int) error {
	span, err := checkedRange(addr, length)
	if err != nil {
		return err
	}
	// Always with a length: E with an address alone erases up to 0x77000,
	// Dygma's settings included.
	return b.command("E"+span+"#", eraseTimeout)
}

// writePage uploads, writes and checks one page.
func (b *BootloaderClient) writePage(p firmware.Page) error {
	span, err := checkedRange(p.Addr, len(p.Data))
	if err != nil {
		return err
	}
	if err := b.upload(p.Data); err != nil {
		return err
	}
	if err := b.command("W"+span+"#", commandTimeout); err != nil {
		return err
	}
	return b.compare(p.Addr, len(p.Data), crc32.ChecksumIEEE(p.Data))
}

// Progress is told how a write is going: with no pages done once the
// bootloader is identified and before the erase, then after each page is
// written and checked.
type Progress func(done, total int)

// Write erases the ZMK code, and the ZMK settings unless keepSettings is
// set, then writes and checks every page of im by address, with the vector
// page at NeuronCodeStart last. It identifies the bootloader first, and
// refuses an image that reaches into settings it keeps before sending
// anything. Start starts the firmware afterwards.
//
// Until the vector page is written, the bootloader finds erased vectors and
// starts nothing, so the button brings it back for another try.
func (b *BootloaderClient) Write(ctx context.Context, im *firmware.NeuronImage, keepSettings bool, progress Progress) error {
	end, err := im.EraseEnd(keepSettings)
	if err != nil {
		return err
	}
	return b.writeRegion(ctx, end, vectorsLast(im.Pages), progress)
}

// WriteStock puts Dygma's firmware back: it erases the ZMK code and
// settings, then writes and checks Dygma's application, then S140 with its
// vector page at NeuronCodeStart last. It identifies the bootloader first.
// Start starts the firmware afterwards.
func (b *BootloaderClient) WriteStock(ctx context.Context, stock *firmware.StockNeuron, progress Progress) error {
	// S140's vectors at 0x1000 start the chain to Dygma's application at
	// 0x27000, so that page goes last, as in Write.
	pages := append(byAddress(stock.Pages), vectorsLast(stock.SoftDevice.Pages)...)
	return b.writeRegion(ctx, firmware.NeuronSettingsEnd, pages, progress)
}

// writeRegion identifies the bootloader, erases NeuronCodeStart..eraseEnd
// and writes pages in order.
func (b *BootloaderClient) writeRegion(ctx context.Context, eraseEnd uint32, pages []firmware.Page, progress Progress) error {
	if progress == nil {
		progress = func(int, int) {}
	}
	if err := b.Identify(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	progress(0, len(pages))
	err := b.erase(firmware.NeuronCodeStart, int(eraseEnd-firmware.NeuronCodeStart))
	for i := 0; err == nil && i < len(pages); i++ {
		if err = ctx.Err(); err == nil {
			err = b.writePage(pages[i])
		}
		if err == nil {
			progress(i+1, len(pages))
		}
	}
	if err != nil {
		return fmt.Errorf("%w; %w. %s", err, ErrPartialImage, recoveryGuidance)
	}
	return nil
}

func byAddress(pages []firmware.Page) []firmware.Page {
	return slices.SortedFunc(slices.Values(pages), func(a, b firmware.Page) int { return cmp.Compare(a.Addr, b.Addr) })
}

// vectorsLast returns pages by address, with the page at NeuronCodeStart,
// which holds the vectors the bootloader starts, moved to the end.
func vectorsLast(pages []firmware.Page) []firmware.Page {
	sorted := byAddress(pages)
	if i := slices.IndexFunc(sorted, func(p firmware.Page) bool { return p.Addr == firmware.NeuronCodeStart }); i >= 0 {
		vectors := sorted[i]
		sorted = append(slices.Delete(sorted, i, i+1), vectors)
	}
	return sorted
}

// Start asks the bootloader to start the firmware. The bootloader resets
// on it, so the port may fail instead of acknowledging. An acknowledgement
// alone does not show that the firmware started; Entry.WaitForFirmware
// does.
func (b *BootloaderClient) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.command("S#", startTimeout)
}

// SoftDeviceCheck is how S140 on the Neuron compares with Dygma's, page by
// page.
type SoftDeviceCheck struct {
	matching, differing []uint32
}

// Matches reports whether every page matches.
func (c SoftDeviceCheck) Matches() bool {
	return len(c.differing) == 0
}

func (c SoftDeviceCheck) String() string {
	total := len(c.matching) + len(c.differing)
	if c.Matches() {
		return fmt.Sprintf("S140 matches Dygma's S140 7.2.0 on all %d pages", total)
	}
	s := fmt.Sprintf("S140 differs on %d of %d pages (absent or not 7.2.0)", len(c.differing), total)
	if len(c.matching) > 0 {
		pages := make([]string, len(c.differing))
		for i, addr := range c.differing {
			pages[i] = fmt.Sprintf("0x%05x", addr)
		}
		s += ": " + strings.Join(pages, ", ")
	}
	return s
}

// CheckS140 compares each page of S140 on the Neuron with sd through the
// bootloader's CRC command alone; it erases and writes nothing. It
// identifies the bootloader first.
func (b *BootloaderClient) CheckS140(ctx context.Context, sd *firmware.SoftDevice) (SoftDeviceCheck, error) {
	var c SoftDeviceCheck
	if err := b.Identify(ctx); err != nil {
		return c, err
	}
	for _, p := range byAddress(sd.Pages) {
		err := ctx.Err()
		if err == nil {
			err = b.compare(p.Addr, len(p.Data), crc32.ChecksumIEEE(p.Data))
		}
		switch {
		case err == nil:
			c.matching = append(c.matching, p.Addr)
		case errors.Is(err, errRejected):
			c.differing = append(c.differing, p.Addr)
		default:
			return SoftDeviceCheck{}, fmt.Errorf("%w; the check stopped, and nothing was erased or written", err)
		}
	}
	return c, nil
}
