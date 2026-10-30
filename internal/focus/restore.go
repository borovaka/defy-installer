// SPDX-License-Identifier: MIT

package focus

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
)

// auditedVersion is the Neuron firmware package whose updater Restore was
// checked against; Restore refuses any other before it resets anything.
const auditedVersion = "v2.3.0"

const (
	readyTimeout = 60 * time.Second
	readyPoll    = 250 * time.Millisecond
)

// ReconnectGuidance says what to do once Dygma's firmware has started its
// update of the halves: the update holds them until the Neuron restarts.
const ReconnectGuidance = "Unplug the Neuron from this computer and plug it back in, which restarts it and " +
	"releases the halves."

// restoreCommands are the commands Restore uses that the Neuron's help
// must list. Dygma's 2.3.0 answers upgrade.isReady too, but its help does
// not list it.
var restoreCommands = []string{
	"hardware.version", "version", "upgrade.start", "upgrade.keyscanner.isConnected",
	"upgrade.keyscanner.begin", "upgrade.keyscanner.getInfo", "upgrade.keyscanner.sendWrite",
	"upgrade.keyscanner.validate", "upgrade.keyscanner.finish",
}

// half pairs a side with Focus's number for it: right 0 and left 1, the
// reverse of the ZMK half updater's numbers.
type half struct {
	side firmware.Side
	id   int
}

var halves = [...]half{{firmware.Right, 0}, {firmware.Left, 1}}

// halfInfo is a half's factory bootloader information, from
// upgrade.keyscanner.getInfo.
type halfInfo struct {
	HardwareVersion, FlashStart, ProgramVersion, ProgramCRC uint32
}

// parseInfo reads getInfo's reply: four decimal numbers, then true.
func parseInfo(reply string) (halfInfo, error) {
	fields := strings.Fields(reply)
	if len(fields) != 5 || fields[4] != "true" {
		return halfInfo{}, fmt.Errorf("invalid keyscanner information %q", reply)
	}
	var v [4]uint32
	for i, field := range fields[:4] {
		if strings.ContainsFunc(field, func(r rune) bool { return r < '0' || r > '9' }) {
			return halfInfo{}, fmt.Errorf("invalid keyscanner information %q", reply)
		}
		n, err := strconv.ParseUint(field, 10, 32)
		if err != nil {
			return halfInfo{}, fmt.Errorf("keyscanner information %q exceeds 32-bit fields", reply)
		}
		v[i] = uint32(n)
	}
	info := halfInfo{v[0], v[1], v[2], v[3]}
	if info.HardwareVersion != 1 {
		return halfInfo{}, fmt.Errorf("unsupported keyscanner hardware version %d; expected 1", info.HardwareVersion)
	}
	if info.FlashStart != firmware.HalfPackageStart {
		return halfInfo{}, fmt.Errorf("unsupported keyscanner flash start %#x; expected %#x", info.FlashStart, firmware.HalfPackageStart)
	}
	return info, nil
}

func (c *client) info(ctx context.Context) (halfInfo, error) {
	reply, err := c.request(ctx, "upgrade.keyscanner.getInfo", nil)
	if err != nil {
		return halfInfo{}, err
	}
	return parseInfo(reply)
}

// packet is upgrade.keyscanner.sendWrite's payload: a page's address and
// length, the page, and its CRC-32.
func packet(page firmware.Page) ([]byte, error) {
	if err := firmware.CheckHalfPage(page); err != nil {
		return nil, err
	}
	p := binary.LittleEndian.AppendUint32(nil, page.Addr)
	p = binary.LittleEndian.AppendUint32(p, uint32(len(page.Data)))
	p = append(p, page.Data...)
	return binary.LittleEndian.AppendUint32(p, crc32.ChecksumIEEE(page.Data)), nil
}

// identify checks that the port is a Defy wireless Neuron running Dygma's
// 2.3.0 firmware with the commands Restore uses.
func identify(ctx context.Context, c *client) error {
	help, err := c.request(ctx, "help", nil)
	if err != nil {
		return err
	}
	listed := strings.Fields(help)
	var missing []string
	for _, command := range restoreCommands {
		if !slices.Contains(listed, command) {
			missing = append(missing, command)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the Neuron lacks the Focus commands %s", strings.Join(missing, ", "))
	}
	hardware, err := c.request(ctx, "hardware.version", nil)
	if err != nil {
		return err
	}
	if hardware != "Defy-wireless" {
		return fmt.Errorf("expected a stock Defy-wireless Neuron, received %q", hardware)
	}
	version, err := c.request(ctx, "version", nil)
	if err != nil {
		return err
	}
	if version != auditedVersion {
		return fmt.Errorf("unsupported Neuron firmware version %q; expected %q. Nothing was reset or written",
			version, auditedVersion)
	}
	return nil
}

// Restore writes image, which must be Dygma's keyscanner, to both halves
// through the Neuron's updater, then checks the version and CRC each half
// reports. The caller checks the image with firmware.HalfImage.RequireStock.
//
// Before the updater starts it identifies the Neuron; a failure there
// leaves everything as it was. The updater resets both halves into their
// factory bootloaders, and Dygma's firmware may ask for the left half's
// top-left Esc key, which w waits for; it does not ask while ZMK runs on
// the halves. progress may be nil.
func Restore(ctx context.Context, rw io.ReadWriter, image *firmware.HalfImage, progress firmware.HalfProgress, w ReadyWait) error {
	c := &client{rw: rw}
	if err := identify(ctx, c); err != nil {
		return err
	}
	if progress == nil {
		progress = func(firmware.Side, int, int) {}
	}
	if err := restore(ctx, c, image, progress, w); err != nil {
		return fmt.Errorf("%w. Dygma's firmware may still hold the halves for the update. %s Then run defy-installer "+
			"uninstall again to write both halves", err, ReconnectGuidance)
	}
	return nil
}

func restore(ctx context.Context, c *client, image *firmware.HalfImage, progress firmware.HalfProgress, w ReadyWait) error {
	if err := c.empty(ctx, "upgrade.start"); err != nil {
		return err
	}
	if err := waitReady(ctx, c, w); err != nil {
		return err
	}
	var original [len(halves)]halfInfo
	// Begin and read both halves before writing either, so a half that does
	// not answer stops the restore before any write.
	for i, h := range halves {
		if err := c.require(ctx, fmt.Sprintf("upgrade.keyscanner.isConnected %d", h.id), nil); err != nil {
			return err
		}
		if err := c.require(ctx, fmt.Sprintf("upgrade.keyscanner.begin %d", h.id), nil); err != nil {
			return err
		}
		var err error
		if original[i], err = c.info(ctx); err != nil {
			return err
		}
	}
	pages := image.Pages()
	total := len(pages) * firmware.HalfPageSize
	for i, h := range halves {
		if err := c.require(ctx, fmt.Sprintf("upgrade.keyscanner.begin %d", h.id), nil); err != nil {
			return err
		}
		current, err := c.info(ctx)
		if err != nil {
			return err
		}
		if current != original[i] {
			return fmt.Errorf("the %v half's keyscanner information changed since it was first read", h.side)
		}
		for j, page := range pages {
			p, err := packet(page)
			if err == nil {
				err = c.require(ctx, "upgrade.keyscanner.sendWrite", p)
			}
			if err != nil {
				return err
			}
			if done := (j + 1) * firmware.HalfPageSize; done < total {
				progress(h.side, done, total)
			}
		}
		if err := c.require(ctx, "upgrade.keyscanner.validate", nil); err != nil {
			return err
		}
		written, err := c.info(ctx)
		if err != nil {
			return err
		}
		if written.HardwareVersion != current.HardwareVersion || written.ProgramVersion != image.Version ||
			written.ProgramCRC != image.ProgramCRC {
			return fmt.Errorf("the %v half reports version 0x%08x and CRC 0x%08x, not the file's 0x%08x and 0x%08x",
				h.side, written.ProgramVersion, written.ProgramCRC, image.Version, image.ProgramCRC)
		}
		if err := c.require(ctx, "upgrade.keyscanner.finish", nil); err != nil {
			return err
		}
		progress(h.side, total, total)
	}
	return nil
}

// ReadyWait says how long to wait for Dygma's updater to get ready, which
// it does at once unless it waits for the left half's top-left Esc key.
// The zero value waits 60 s, then fails.
type ReadyWait struct {
	// Waiting, if set, runs once, when the updater first answers that it
	// is not ready.
	Waiting func()
	// Limit is how long the updater may stay not ready; zero is 60 s.
	Limit time.Duration
	// Expired, if set, runs each time Limit passes with the updater not
	// ready. The wait starts again if it returns nil, and stops with its
	// error otherwise. Without it, the wait fails at the limit.
	Expired func(context.Context) error
}

// waitReady waits for the updater to be ready: Dygma's firmware waits there
// for the Esc key when the left half asks for it.
func waitReady(ctx context.Context, c *client, w ReadyWait) error {
	limit := cmp.Or(w.Limit, readyTimeout)
	deadline := time.Now().Add(limit)
	for {
		ready, err := c.boolean(ctx, "upgrade.isReady", nil)
		if err != nil || ready {
			return err
		}
		if w.Waiting != nil {
			w.Waiting()
			w.Waiting = nil
		}
		if !time.Now().Before(deadline) && w.Expired != nil {
			if err := w.Expired(ctx); err != nil {
				return err
			}
			deadline = time.Now().Add(limit)
			continue
		}
		if !time.Now().Before(deadline) {
			return errors.New("the Neuron did not get ready for the update in time. If it waits for the Esc key, " +
				"press the top-left key on the left half once, then try again")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(readyPoll, time.Until(deadline))):
		}
	}
}
