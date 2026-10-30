// SPDX-License-Identifier: MIT

package flow

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/borovaka/defy-installer/internal/updater"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

// fakeUpdater is the shared fake of the half updater's port.
type fakeUpdater = ut.Updater[updater.Opcode]

// withPairing is the half updater of Neuron firmware with PAIRING and
// RESET PAIRING, which no recording covers, reporting pairing. With pairing
// nil, it is firmware older than PAIRING, which refuses both.
func withPairing(t *testing.T, pairing []byte) *fakeUpdater {
	f := ut.New[updater.Opcode](t)
	f.Pairing = pairing
	return f
}

// atUpdater is the fake's updater port.
var atUpdater = Halves{Port: updaterPort.Path, Timeout: DefaultHalvesTimeout}

// The lines a write says about the pairing reset.
const (
	resetBefore = "This session resets the split pairing before it writes the halves. It clears the Neuron's " +
		"split bonds and erases both halves' settings, which hold their Bluetooth identity and bonds."
	resetDone = "Both halves written and checked, and the split pairing reset. The Neuron's firmware and its " +
		"other settings, such as its pairings with computers, are unchanged."
	resetAfter = "Both halves start without settings. Each makes a new Bluetooth identity and pairs with the " +
		"Neuron as a new device, with its cable in or out."
	unchanged = "Both halves written and checked. The Neuron's firmware and settings are unchanged."
	noReadout = "This Neuron firmware cannot report or reset the split pairing, so it is left as it is."
)

var sessionStart = []updater.Opcode{updater.OpHello, updater.OpPairing, updater.OpOpen, updater.OpProbe, updater.OpProbe}

// TestHalvesWriteResetsAnUnpairedNeuron: a write resets the split pairing
// when the Neuron is not paired with both halves, or with --reset-pairing.
func TestHalvesWriteResetsAnUnpairedNeuron(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pairing []byte
		flag    bool // --reset-pairing
		resets  bool
		want    string
	}{
		{"paired", []byte{1, 2, 3, 3}, false, false, "The Neuron is paired with both halves, so the pairing is kept."},
		{"both empty", []byte{1, 2, 0, 0}, false, true, "The Neuron is not paired with both halves: slot 1 empty, slot 2 empty."},
		{"no bond", []byte{1, 2, 3, 1}, false, true,
			"The Neuron is not paired with both halves: slot 1 paired, slot 2 with a half's address but no bond."},
		{"one empty", []byte{1, 2, 0, 3}, false, true, "The Neuron is not paired with both halves: slot 1 empty, slot 2 paired."},
		{"--reset-pairing", []byte{1, 2, 3, 3}, true, true,
			"The Neuron is paired with both halves; --reset-pairing resets the pairing anyway."},
	} {
		bubble(t, tt.name, func(t *testing.T) {
			d := newDefy(t, "zmk")
			d.updater = withPairing(t, tt.pairing)
			left, right := recordedHalves(t)
			if err := WriteHalves(t.Context(), d.env(), atUpdater, left, right, tt.flag); err != nil {
				t.Fatal(err)
			}
			said, unsaid, resets := []string{tt.want, unchanged}, []string{resetBefore, resetDone, resetAfter}, 0
			if tt.resets {
				said, unsaid, resets = []string{tt.want, resetBefore, resetDone, resetAfter}, []string{unchanged}, 1
			}
			wantOutput(t, d.out.String(), said...)
			wantNoOutput(t, d.out.String(), unsaid...)
			if d.updater.Count(updater.OpResetPairing) != resets || d.updater.Count(updater.OpFinish) != 2 {
				t.Errorf("ran %v", d.updater.Executed)
			}
		})
	}
}

func TestResetPairingNeedsTheReadout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Firmware older than PAIRING refuses it; the flow then clears DTR
		// and closes the port, with no session.
		d := newDefy(t, "zmk")
		d.updater = withPairing(t, nil)
		left, right := recordedHalves(t)
		err := WriteHalves(t.Context(), d.env(), atUpdater, left, right, true)
		if !errors.Is(err, updater.ErrNoPairing) {
			t.Errorf("got %v", err)
		}
		wantOutput(t, fmt.Sprint(err), "--reset-pairing cannot run",
			"Update the Neuron first, with advanced neuron write --keep-settings. Nothing was written.")
		if !slices.Equal(d.updater.Executed, sessionStart[:2]) || !d.updater.Closed {
			t.Errorf("ran %v; closed %v", d.updater.Executed, d.updater.Closed)
		}
		wantNoOutput(t, d.out.String(), resettingHalves, noReadout)
	})
}

func TestInvalidPairingStopsBeforeTheSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDefy(t, "zmk")
		d.updater = withPairing(t, []byte{1, 2, 3})
		left, right := recordedHalves(t)
		err := WriteHalves(t.Context(), d.env(), atUpdater, left, right, false)
		wantError(t, err, "Nothing was written.")
		if !slices.Equal(d.updater.Executed, sessionStart[:2]) || !d.updater.Closed {
			t.Errorf("ran %v; closed %v", d.updater.Executed, d.updater.Closed)
		}
		wantNoOutput(t, d.out.String(), resettingHalves)
	})
}

func TestFailureAfterAPairingResetSaysSo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDefy(t, "zmk")
		d.updater = withPairing(t, []byte{1, 2, 0, 0})
		d.updater.Reject[updater.OpFinish] = -5
		left, right := recordedHalves(t)
		err := WriteHalves(t.Context(), d.env(), atUpdater, left, right, false)
		wantOutput(t, fmt.Sprint(err), "updater FINISH failed with status -5",
			"write both halves again", "the next write resets the pairing again")
		if d.updater.Resets != 1 || !d.updater.Closed {
			t.Errorf("reset %d times; closed %v", d.updater.Resets, d.updater.Closed)
		}
	})
}

func TestProbeReportsThePairing(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pairing []byte
		want    string
	}{
		{"paired", []byte{1, 2, 3, 3}, "The Neuron is paired with both halves.\n"},
		{"not paired", []byte{1, 2, 1, 0}, "The Neuron is not paired with both halves: slot 1 with a half's address but no bond, " +
			"slot 2 empty. A halves write resets the pairing.\n"},
	} {
		bubble(t, tt.name, func(t *testing.T) {
			d := newDefy(t, "zmk")
			d.updater = withPairing(t, tt.pairing)
			if err := ProbeHalves(t.Context(), d.env(), atUpdater); err != nil {
				t.Fatal(err)
			}
			if want := slices.Concat(sessionStart, []updater.Opcode{updater.OpClose}); !slices.Equal(d.updater.Executed, want) {
				t.Errorf("ran %v, want %v", d.updater.Executed, want)
			}
			wantOutput(t, d.out.String(), tt.want, "Both factory bootloaders read. Nothing was written.",
				"left half: hardware 1, seal not valid, program version 0x00000000, program CRC 0x00000000.")
		})
	}
}
