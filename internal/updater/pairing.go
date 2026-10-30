// SPDX-License-Identifier: MIT

package updater

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// resetPairingTimeout is how long RESET PAIRING's reply may take. Before it
// replies, the Neuron erases sixteen 4 KiB sectors on the halves, and serial
// NOR flash allows up to about 400 ms for each. A resend after it still
// reaches the Neuron within the updater's idle limit.
const resetPairingTimeout = 8 * time.Second

// ErrNoPairing reports Neuron firmware older than PAIRING, or built
// without it. It cannot report the split pairing, and it would refuse
// RESET PAIRING.
var ErrNoPairing = errors.New("this Neuron firmware cannot report or reset the split pairing")

// Slot is one of the Neuron's split slots, as PAIRING reads it: a byte of
// flags.
type Slot uint8

// The flags of a Slot.
const (
	// SlotAddress is set when the slot holds a half's address.
	SlotAddress Slot = 1 << iota
	// SlotBond is set when the Neuron holds a bond for that address.
	SlotBond
)

// Paired reports whether the slot holds a half's address and the Neuron
// holds a bond for it.
func (s Slot) Paired() bool {
	return s == SlotAddress|SlotBond
}

func (s Slot) String() string {
	switch s {
	case 0:
		return "empty"
	case SlotAddress:
		return "with a half's address but no bond"
	case SlotBond:
		return "with a bond but no address"
	case SlotAddress | SlotBond:
		return "paired"
	}
	return fmt.Sprintf("with flags 0x%02x", uint8(s))
}

// Pairing is what PAIRING reads: the Neuron's two split slots, one for each
// half.
type Pairing struct {
	Slots [2]Slot
}

// Paired reports whether the Neuron is paired with both halves: both slots
// hold a half's address and a bond for it.
func (p Pairing) Paired() bool {
	return p.Slots[0].Paired() && p.Slots[1].Paired()
}

const (
	pairingVersion = 1
	pairingSize    = 2 + len(Pairing{}.Slots)
)

// parsePairing reads PAIRING's reply: version 1, the slot count 2, then a
// byte of flags for each slot.
func parsePairing(reply []byte) (Pairing, error) {
	var p Pairing
	if len(reply) >= 2 && (reply[0] != pairingVersion || int(reply[1]) != len(p.Slots)) {
		return p, fmt.Errorf("updater PAIRING reply gives version %d with %d slots, not version %d with %d",
			reply[0], reply[1], pairingVersion, len(p.Slots))
	}
	if len(reply) != pairingSize {
		return p, fmt.Errorf("updater PAIRING reply is %d bytes, not %d", len(reply), pairingSize)
	}
	for i, flags := range reply[2:] {
		if Slot(flags)&^(SlotAddress|SlotBond) != 0 {
			return Pairing{}, fmt.Errorf("updater PAIRING reply gives slot %d flags 0x%02x; version %d defines bits 0 and 1",
				i+1, flags, pairingVersion)
		}
		p.Slots[i] = Slot(flags)
	}
	return p, nil
}

// Pairing sends PAIRING and reads the Neuron's split slots. Firmware
// without it gives an error matching ErrNoPairing.
func (c *Client) Pairing(ctx context.Context) (Pairing, error) {
	reply, err := c.query(ctx, OpPairing, c.timeout)
	if err != nil {
		return Pairing{}, err
	}
	p, err := parsePairing(reply)
	if err != nil {
		return Pairing{}, err
	}
	c.pairingRead = true
	return p, nil
}

// resetPairing sends RESET PAIRING, which goes after both PROBEs and before
// the first START, once per session. The Neuron clears its split bonds and
// slots, then erases both halves' settings, their Bluetooth identity and
// bonds among them. Firmware older than PAIRING refuses RESET PAIRING and
// closes the session, so Flash needs PAIRING answered first.
func (c *Client) resetPairing(ctx context.Context) error {
	reply, err := c.requestWithin(ctx, OpResetPairing, nil, resetPairingTimeout)
	if err != nil {
		return err
	}
	return c.expect(OpResetPairing, reply, nil)
}
