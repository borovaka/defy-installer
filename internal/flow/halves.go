// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/updater"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// Halves says which Neuron's half updater a halves flow uses.
type Halves struct {
	// Port is the updater's port. Empty means the one that answers
	// HELLO among the Neuron's ports.
	Port string
	// Serial is the Neuron's USB serial number. Empty means the one ZMK
	// Neuron connected, or the one at Port.
	Serial string
	// Timeout is the updater's reply timeout, under 5 s.
	Timeout time.Duration
}

// DefaultHalvesTimeout is the usual Halves.Timeout.
const DefaultHalvesTimeout = updater.DefaultTimeout

// WriteHalves writes both halves through the ZMK Neuron's half updater,
// with a split pairing reset as halvesSession decides; resetPairing resets
// the pairing even when it is complete.
func WriteHalves(ctx context.Context, env Env, h Halves, left, right *firmware.HalfImage, resetPairing bool) error {
	images := [...]*firmware.HalfImage{left, right}
	for i, side := range sides {
		if err := images[i].RequireApplication(side); err != nil {
			return err
		}
	}
	for _, image := range images {
		env.say("%s: %v half's package, %d bytes, version 0x%08x, program CRC 0x%08x.",
			image.Name, image.Side, len(image.Data), image.Version, image.ProgramCRC)
	}
	_, reset, err := halvesSession(ctx, env, h, left, right, halvesWrite{resetPairing: resetPairing, explain: true})
	if err != nil {
		return err
	}
	if reset {
		env.say("Both halves written and checked, and the split pairing reset. The Neuron's firmware and its " +
			"other settings, such as its pairings with computers, are unchanged.")
	} else {
		env.say("Both halves written and checked. The Neuron's firmware and settings are unchanged.")
	}
	env.say("Typing resumes now. Images with radio firmware install it as each half starts; " +
		"allow about half a minute before checking typing.")
	if reset {
		env.say("Both halves start without settings. Each makes a new Bluetooth identity and pairs with the " +
			"Neuron as a new device, with its cable in or out.")
	}
	return nil
}

// halvesWrite says how halvesSession writes the halves.
type halvesWrite struct {
	// resetPairing resets the split pairing even when the Neuron is
	// paired with both halves.
	resetPairing bool
	// unlessCurrent writes nothing when the session's PROBEs find both
	// halves holding the images, unless the pairing is reset.
	unlessCurrent bool
	// progress, if set, also reports each half's write.
	progress firmware.HalfProgress
	// opening, if set, runs once the pairing is read, before the session
	// opens.
	opening func()
	// explain adds to an error what it left of the halves: nothing
	// written, or a session that has ended, and what to write again.
	explain bool
}

// halvesSession writes both halves in one session of the half updater h
// names, and gives the detail on env. Before the session it reads the
// Neuron's split pairing. When the Neuron is not paired with both halves,
// or w.resetPairing is set, the session resets the pairing before the
// writes. Neuron firmware that cannot report the pairing gets the writes
// alone, and w.resetPairing then fails before the session. It reports
// whether it wrote the halves, and whether it reset the pairing.
func halvesSession(ctx context.Context, env Env, h Halves, left, right *firmware.HalfImage, w halvesWrite) (wrote, reset bool, err error) {
	err = withUpdater(ctx, env, h, func(c *updater.Client) error {
		var err error
		if reset, err = pairingReset(ctx, env, c, w.resetPairing); err != nil {
			if w.explain {
				err = fmt.Errorf("%w. %s", err, nothingWritten)
			}
			return err
		}
		env.say(resettingHalves)
		if w.opening != nil {
			w.opening()
		}
		wrote, err = updater.Flash(ctx, c, left, right, updater.FlashOptions{
			ResetPairing:  reset,
			UnlessCurrent: w.unlessCurrent,
			Progress: func(side firmware.Side, done, total int) {
				if w.progress != nil {
					w.progress(side, done, total)
				}
				switch {
				case done == total:
					env.say("%v half: written; its CRC, factory validation and metadata match.", side)
				case done%halfDetailStep == 0:
					env.say("%v half: %d of %d bytes written and checked.", side, done, total)
				}
			},
		})
		if err != nil && w.explain {
			err = fmt.Errorf("%w. %s %s", err, releasedHalves, writeAgain(reset))
		}
		return err
	})
	if err != nil {
		return false, false, err
	}
	return wrote, reset, nil
}

// halfDetailStep is how many bytes a half's write goes between detail
// lines.
const halfDetailStep = 16 << 10

// nothingWritten follows an error before the session.
const nothingWritten = "Nothing was written."

// resettingHalves is said just before the session opens.
const resettingHalves = "Resetting the cabled halves into their bootloaders; typing pauses until this ends."

// releasedHalves follows an error in a session of the advanced commands,
// once the updater's port has closed.
const releasedHalves = "Closing the updater's port ended the session and released the halves."

// writeAgain says, after an error in a session of the advanced commands,
// what to write again; reset is set when the session reset the pairing.
func writeAgain(reset bool) string {
	s := "If writes had started, write both halves again: a half with a partial image runs nothing until then."
	if reset {
		s += " If the pairing reset had started, the Neuron is no longer paired with both halves, and the next " +
			"write resets the pairing again."
	}
	return s
}

// pairingReset reads the Neuron's split pairing before a write, says what
// the write does about it, and reports whether the write resets it. force
// resets a pairing that is complete.
func pairingReset(ctx context.Context, env Env, c *updater.Client, force bool) (bool, error) {
	pairing, err := c.Pairing(ctx)
	switch {
	case errors.Is(err, updater.ErrNoPairing) && force:
		return false, fmt.Errorf("--reset-pairing cannot run: %w. Update the Neuron first, with advanced neuron write --keep-settings", err)
	case errors.Is(err, updater.ErrNoPairing):
		env.say("This Neuron firmware cannot report or reset the split pairing, so it is left as it is.")
		return false, nil
	case err != nil:
		return false, err
	}
	switch {
	case !pairing.Paired():
		env.say("%s.", describePairing(pairing))
	case force:
		env.say("%s; --reset-pairing resets the pairing anyway.", describePairing(pairing))
	default:
		env.say("%s, so the pairing is kept.", describePairing(pairing))
		return false, nil
	}
	env.say("This session resets the split pairing before it writes the halves. It clears the Neuron's split " +
		"bonds and erases both halves' settings, which hold their Bluetooth identity and bonds.")
	return true, nil
}

// describePairing says whether the Neuron is paired with both halves, and
// if not, what its split slots hold.
func describePairing(p updater.Pairing) string {
	if p.Paired() {
		return "The Neuron is paired with both halves"
	}
	return fmt.Sprintf("The Neuron is not paired with both halves: slot 1 %v, slot 2 %v", p.Slots[0], p.Slots[1])
}

// ProbeHalves reads the Neuron's split pairing, then both halves' factory
// bootloaders, through the ZMK Neuron's half updater. Nothing is written.
func ProbeHalves(ctx context.Context, env Env, h Halves) error {
	const nothingSent = "This probe sent no erase or write."
	var info []updater.HalfInfo
	err := withUpdater(ctx, env, h, func(c *updater.Client) error {
		pairing, err := c.Pairing(ctx)
		switch {
		case errors.Is(err, updater.ErrNoPairing):
			env.say("This Neuron firmware cannot report or reset the split pairing.")
		case err != nil:
			return fmt.Errorf("%w. %s", err, nothingSent)
		case pairing.Paired():
			env.say("%s.", describePairing(pairing))
		default:
			env.say("%s. A halves write resets the pairing.", describePairing(pairing))
		}
		env.say(resettingHalves)
		if info, err = updater.ProbeHalves(ctx, c); err != nil {
			return fmt.Errorf("%w. %s %s", err, releasedHalves, nothingSent)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, half := range info {
		seal := "valid"
		if !half.SealValid {
			seal = "not valid"
		}
		env.say("%v half: hardware %d, seal %s, program version 0x%08x, program CRC 0x%08x.",
			half.Side, half.HardwareVersion, seal, half.ProgramVersion, half.ProgramCRC)
	}
	env.say("Both factory bootloaders read. Nothing was written.")
	return nil
}

// connectUpdater finds the half updater and starts using it.
func connectUpdater(ctx context.Context, env Env, h Halves) (*updater.Connection, error) {
	serial, err := neuronSerial(ctx, env, h)
	if err != nil {
		return nil, err
	}
	conn, err := updater.Connect(ctx, env.List, env.updaterOpener(true), serial, h.Port, h.Timeout, env.Rand)
	if err != nil {
		return nil, err
	}
	env.say("Found the half updater at %s, on the Neuron with USB serial number %s.", conn.Port.Path, serial)
	return conn, nil
}

// withUpdater finds the half updater h names, runs use with its client,
// then clears DTR and closes the updater's port, which ends any session
// and releases the halves. A failure to close is an error of its own,
// joined to use's.
func withUpdater(ctx context.Context, env Env, h Halves, use func(*updater.Client) error) error {
	conn, err := connectUpdater(ctx, env, h)
	if err != nil {
		return err
	}
	err = use(conn.Client)
	if closeErr := conn.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("closing the updater's port %s: %w", conn.Port.Path, closeErr))
	}
	return err
}

// useUpdater is withUpdater for a user's command, on the Neuron running
// ZMK with USB serial number serial.
func (r *run) useUpdater(ctx context.Context, serial string, use func(*updater.Client) error) error {
	return withUpdater(ctx, r.detail, Halves{Serial: serial, Timeout: DefaultHalvesTimeout}, use)
}

// neuronSerial is h's USB serial number, or the one ZMK Neuron's.
func neuronSerial(ctx context.Context, env Env, h Halves) (string, error) {
	if h.Serial != "" {
		return h.Serial, nil
	}
	ports, err := env.List(ctx)
	if err != nil {
		return "", err
	}
	if h.Port != "" {
		p, err := usbserial.Find(ports, h.Port)
		if err != nil {
			return "", err
		}
		return p.Serial, nil
	}
	switch zmk := inMode(neurons(ports), neuron.ZMK); len(zmk) {
	case 0:
		return "", errors.New("no Neuron running ZMK found; connect it by USB")
	case 1:
		return zmk[0].ports[0].Serial, nil
	}
	return "", errors.New("more than one Neuron running ZMK is connected; choose one with --serial")
}
