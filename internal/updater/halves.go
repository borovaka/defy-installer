// SPDX-License-Identifier: MIT

package updater

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"github.com/borovaka/defy-installer/internal/firmware"
)

// halves are the sides in the order a session takes them, which is also
// the order of HALF VERSIONS's and BATTERY's records.
var halves = [...]firmware.Side{firmware.Left, firmware.Right}

// sideID is the updater's number for a half, its place in halves: 0 for
// left and 1 for right, the reverse of the packages' metadata.
func sideID(side firmware.Side) (byte, error) {
	if i := slices.Index(halves[:], side); i >= 0 {
		return byte(i), nil
	}
	return 0, fmt.Errorf("the updater takes the left or the right half, not %v", side)
}

// HalfInfo is what PROBE reads from a half: its factory bootloader's
// hardware version, and the seal of the application it holds.
type HalfInfo struct {
	Side            firmware.Side
	SealValid       bool
	HardwareVersion uint32
	ProgramVersion  uint32
	ProgramCRC      uint32
}

const halfInfoSize = 20

// parseHalfInfo reads PROBE's reply: the side, whether the seal is valid,
// two zero bytes, then the hardware version, the flash start, and the
// program's version and CRC-32 from the seal.
func parseHalfInfo(side firmware.Side, id byte, reply []byte) (HalfInfo, error) {
	if len(reply) != halfInfoSize {
		return HalfInfo{}, fmt.Errorf("updater PROBE reply is %d bytes, not %d", len(reply), halfInfoSize)
	}
	le := binary.LittleEndian
	if reply[0] != id || reply[1] > 1 || le.Uint16(reply[2:]) != 0 {
		return HalfInfo{}, fmt.Errorf("updater PROBE reply for side %d gives side %d, seal flag %d and reserved %#x",
			id, reply[0], reply[1], le.Uint16(reply[2:]))
	}
	info := HalfInfo{
		Side:            side,
		SealValid:       reply[1] == 1,
		HardwareVersion: le.Uint32(reply[4:]),
		ProgramVersion:  le.Uint32(reply[12:]),
		ProgramCRC:      le.Uint32(reply[16:]),
	}
	if start := le.Uint32(reply[8:]); info.HardwareVersion != 1 || start != firmware.HalfPackageStart {
		return HalfInfo{}, fmt.Errorf("the %v half has hardware version %d and its application at %#x; "+
			"the updater supports version 1 at %#x", side, info.HardwareVersion, start, firmware.HalfPackageStart)
	}
	return info, nil
}

// holds reports whether the half holds image's program: a valid seal with
// image's program CRC-32.
func (h HalfInfo) holds(image *firmware.HalfImage) bool {
	return h.SealValid && h.ProgramCRC == image.ProgramCRC
}

// ProbeError is a PROBE the updater refused or did not answer: the half
// did not answer through its factory bootloader, as when its cable is out
// or it is switched on.
type ProbeError struct {
	Side firmware.Side
	Err  error
}

func (e *ProbeError) Error() string {
	return fmt.Sprintf("%v half: %v", e.Side, e.Err)
}

func (e *ProbeError) Unwrap() error {
	return e.Err
}

// probeError returns err from a PROBE of side as a *ProbeError when the
// updater refused the PROBE or did not answer it, and as it is otherwise.
func probeError(side firmware.Side, err error) error {
	if _, refused := errors.AsType[*DeviceError](err); refused || errors.Is(err, errNoReply) {
		return &ProbeError{Side: side, Err: err}
	}
	return err
}

// openAndProbe opens a session, then probes the left half and the right.
// A PROBE refused or not answered is a *ProbeError.
func openAndProbe(ctx context.Context, c *Client) ([]HalfInfo, error) {
	if err := c.open(ctx); err != nil {
		return nil, err
	}
	var info []HalfInfo
	for _, side := range halves {
		half, err := c.probe(ctx, side)
		if err != nil {
			return nil, probeError(side, err)
		}
		info = append(info, half)
	}
	return info, nil
}

// ProbeHalves opens a session, probes the left half, then the right, and
// ends the session. It writes nothing to either half. A PROBE refused or
// not answered is a *ProbeError.
func ProbeHalves(ctx context.Context, c *Client) ([]HalfInfo, error) {
	info, err := openAndProbe(ctx, c)
	if err == nil {
		err = c.endSession(ctx)
	}
	if err != nil {
		return nil, err
	}
	return info, nil
}

// FlashOptions say how Flash writes the halves. The zero value writes
// both halves without a pairing reset.
type FlashOptions struct {
	// ResetPairing sends RESET PAIRING after the PROBEs and before the
	// first START: the Neuron clears its split pairing and both halves'
	// settings, then the halves are written. The client must have read
	// PAIRING.
	//
	// The Neuron clears its own side first, so once RESET PAIRING has
	// started, PAIRING reports the Neuron not paired with both halves,
	// even if the reset or a write then fails.
	ResetPairing bool
	// UnlessCurrent ends the session after its PROBEs, writing nothing,
	// when both halves already hold the images' programs: valid seals
	// with the images' program CRC-32s. A session with a pairing reset
	// writes the halves whatever they hold.
	UnlessCurrent bool
	// Progress, if set, reports each half's write.
	Progress firmware.HalfProgress
}

// Flash writes both halves in one session: it checks that the images are
// ZMK builds for their sides, opens the session, probes both halves, then
// writes and validates the left, then the right, and ends the session. It
// reports whether it wrote the halves.
//
// After an error the session stays open until the port closes, which ends
// it; Connection.Close does that. A half whose write started and did not
// finish needs another complete Flash. A PROBE refused or not answered is
// a *ProbeError, and an OPEN refused matches ErrHalvesBusy or
// ErrHalvesSilent.
func Flash(ctx context.Context, c *Client, left, right *firmware.HalfImage, opts FlashOptions) (bool, error) {
	images := [...]*firmware.HalfImage{left, right}
	for i, side := range halves {
		if images[i] == nil {
			return false, errors.New("flashing the halves needs both the left and the right image")
		}
		if err := images[i].RequireApplication(side); err != nil {
			return false, err
		}
	}
	if opts.ResetPairing && !c.pairingRead {
		return false, errors.New("resetting the split pairing needs PAIRING answered first")
	}
	info, err := openAndProbe(ctx, c)
	if err != nil {
		return false, err
	}
	if opts.UnlessCurrent && !opts.ResetPairing && info[0].holds(left) && info[1].holds(right) {
		return false, c.endSession(ctx)
	}
	if opts.ResetPairing {
		if err := c.resetPairing(ctx); err != nil {
			return false, err
		}
	}
	for i, side := range halves {
		if err := flashHalf(ctx, c, side, images[i], opts.Progress); err != nil {
			return false, fmt.Errorf("%v half: %w", side, err)
		}
	}
	return true, c.endSession(ctx)
}

func flashHalf(ctx context.Context, c *Client, side firmware.Side, image *firmware.HalfImage, progress firmware.HalfProgress) error {
	if err := c.start(ctx, side, image); err != nil {
		return err
	}
	pages := image.Pages()
	total := len(pages) * firmware.HalfPageSize
	for i, p := range pages {
		if err := c.page(ctx, p.Addr, p.Data); err != nil {
			return err
		}
		if done := (i + 1) * firmware.HalfPageSize; progress != nil && done < total {
			progress(side, done, total)
		}
	}
	if err := c.finish(ctx, image); err != nil {
		return err
	}
	if progress != nil {
		progress(side, total, total)
	}
	return nil
}
