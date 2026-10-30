// SPDX-License-Identifier: MIT

package updater

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

func TestPairingReadsTheSlots(t *testing.T) {
	for _, tt := range []struct {
		reply  []byte
		want   [2]Slot
		paired bool
	}{
		{[]byte{1, 2, 3, 3}, [2]Slot{SlotAddress | SlotBond, SlotAddress | SlotBond}, true},
		{[]byte{1, 2, 3, 1}, [2]Slot{SlotAddress | SlotBond, SlotAddress}, false},
		{[]byte{1, 2, 0, 3}, [2]Slot{0, SlotAddress | SlotBond}, false},
		{[]byte{1, 2, 0, 0}, [2]Slot{}, false},
		{[]byte{1, 2, 2, 3}, [2]Slot{SlotBond, SlotAddress | SlotBond}, false},
	} {
		p, err := parsePairing(tt.reply)
		if err != nil || p.Slots != tt.want || p.Paired() != tt.paired {
			t.Errorf("%x: got %v, paired %v, %v", tt.reply, p.Slots, p.Paired(), err)
		}
	}
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Pairing = []byte{1, 2, 3, 1}
		c := helloClient(t, device)
		p, err := c.Pairing(t.Context())
		if err != nil || p.Slots != [2]Slot{SlotAddress | SlotBond, SlotAddress} {
			t.Fatalf("got %v, %v", p.Slots, err)
		}
		// PAIRING opened no session, and the client goes on.
		if err := c.open(t.Context()); err != nil {
			t.Fatal(err)
		}
		wantOps(t, "requests", device.Executed, OpHello, OpPairing, OpOpen)
	})
}

func TestInvalidPairingReplyIsAnError(t *testing.T) {
	for _, tt := range []struct {
		reply []byte
		want  string
	}{
		{nil, "0 bytes, not 4"},
		{[]byte{1, 2, 3}, "3 bytes, not 4"},
		{[]byte{1, 2, 3, 3, 0}, "5 bytes, not 4"},
		{[]byte{2, 2, 3, 3}, "gives version 2 with 2 slots, not version 1 with 2"},
		{[]byte{1, 3, 3, 3, 3}, "gives version 1 with 3 slots, not version 1 with 2"},
		{[]byte{1, 2, 3, 4}, "slot 2 flags 0x04; version 1 defines bits 0 and 1"},
		{[]byte{1, 2, 0x83, 3}, "slot 1 flags 0x83"},
	} {
		_, err := parsePairing(tt.reply)
		wantError(t, err, tt.want)
	}
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Pairing = []byte{1, 2, 3, 4}
		c := helloClient(t, device)
		if _, err := c.Pairing(t.Context()); err == nil || errors.Is(err, ErrNoPairing) || c.failed {
			t.Errorf("got %v; failed %v", err, c.failed)
		}
		// The reply came whole and changed no session, but the pairing is
		// not known, so no reset can go.
		left, right := testImages(t)
		wantError(t, flash(t.Context(), c, left, right, FlashOptions{ResetPairing: true}), "needs PAIRING answered first")
		if err := c.open(t.Context()); err != nil {
			t.Error(err)
		}
		wantOps(t, "requests", device.Executed, OpHello, OpPairing, OpOpen)
	})
}

func TestOlderFirmwareHasNoPairing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		c := helloClient(t, device)
		_, err := c.Pairing(t.Context())
		refused, ok := errors.AsType[*DeviceError](err)
		if !errors.Is(err, ErrNoPairing) || !ok || refused.Opcode != OpPairing || refused.Status != -enotconn {
			t.Fatalf("got %v", err)
		}
		if c.failed {
			t.Error("the refusal made the client unusable")
		}
		// No RESET PAIRING goes to firmware that did not answer PAIRING,
		// and no session opens for one.
		left, right := testImages(t)
		wantError(t, flash(t.Context(), c, left, right, FlashOptions{ResetPairing: true}), "needs PAIRING answered first")
		wantOps(t, "requests", device.Executed, OpHello, OpPairing)
		// A write without the reset goes on as before.
		if err := flash(t.Context(), c, left, right, FlashOptions{}); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(ops(device.Requests), OpResetPairing) || !slices.Equal(device.Finished, []byte{0, 1}) {
			t.Errorf("requests %v; finished %v", ops(device.Requests), device.Finished)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// No reply is not a refusal: that firmware may have PAIRING.
		device := newFake(t)
		c := helloClient(t, device)
		device.Silent = true
		if _, err := c.Pairing(t.Context()); !errors.Is(err, errNoReply) || errors.Is(err, ErrNoPairing) {
			t.Errorf("got %v", err)
		}
	})
}

func TestFlashWithPairingReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Pairing = []byte{1, 2, 1, 0}
		c := helloClient(t, device)
		if _, err := c.Pairing(t.Context()); err != nil {
			t.Fatal(err)
		}
		left, right := testImages(t)
		if err := flash(t.Context(), c, left, right, FlashOptions{ResetPairing: true}); err != nil {
			t.Fatal(err)
		}
		// After both PROBEs and before the first START, once.
		wantOps(t, "first requests", device.Executed[:7],
			OpHello, OpPairing, OpOpen, OpProbe, OpProbe, OpResetPairing, OpStart)
		if n := device.Count(OpResetPairing); n != 1 || device.Resets != 1 {
			t.Errorf("ran RESET PAIRING %d times", n)
		}
		if r := device.Requests[5]; r.Session != 0xabcdef01 || r.Sequence != 4 || len(r.Payload) != 0 {
			t.Errorf("RESET PAIRING went with session %#x, sequence %d and %d bytes", r.Session, r.Sequence, len(r.Payload))
		}
		if !slices.Equal(device.Finished, []byte{0, 1}) || device.Opened {
			t.Errorf("finished %v; session open %v", device.Finished, device.Opened)
		}
		for i, image := range []*firmware.HalfImage{left, right} {
			if !bytes.Equal(device.Memory[i], image.TransferData()) {
				t.Errorf("side %d holds %d bytes, not the image's %d", i, len(device.Memory[i]), len(image.TransferData()))
			}
		}
		p, err := c.Pairing(t.Context())
		if err != nil || p.Slots != [2]Slot{} {
			t.Errorf("after the reset: %v, %v", p.Slots, err)
		}
	})
}

func TestResetPairingFailureStopsFlash(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*fakeUpdater)
		want   string
	}{
		{"refused", func(d *fakeUpdater) { d.Reject[OpResetPairing] = -5 }, "updater RESET PAIRING failed with status -5"},
		{"unexpected reply", func(d *fakeUpdater) {
			d.Filter = func(r request, b []byte) []byte {
				if r.Op == OpResetPairing {
					return ut.Reply(r, []byte{0})
				}
				return b
			}
		}, "unexpected updater RESET PAIRING reply 00"},
	} {
		synctest.Test(t, func(t *testing.T) {
			device := newFake(t)
			device.Pairing = []byte{1, 2, 0, 0}
			tt.change(device)
			c := helloClient(t, device)
			if _, err := c.Pairing(t.Context()); err != nil {
				t.Fatal(err)
			}
			left, right := testImages(t)
			wantError(t, flash(t.Context(), c, left, right, FlashOptions{ResetPairing: true}), tt.want)
			wantOps(t, tt.name, device.Executed, OpHello, OpPairing, OpOpen, OpProbe, OpProbe, OpResetPairing)
			if !c.failed {
				t.Errorf("%s: the client is still usable", tt.name)
			}
		})
	}
}

func TestSlowResetPairingIsWaitedFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Sixteen slow sector erases: one request is enough.
		device := newFake(t)
		device.Pairing = []byte{1, 2, 0, 0}
		device.Slow = map[Opcode]time.Duration{OpResetPairing: 6400 * time.Millisecond}
		c := helloClient(t, device)
		if _, err := c.Pairing(t.Context()); err != nil {
			t.Fatal(err)
		}
		left, right := testImages(t)
		if err := flash(t.Context(), c, left, right, FlashOptions{ResetPairing: true}); err != nil {
			t.Fatal(err)
		}
		if sent := count(ops(device.Requests), OpResetPairing); sent != 1 || device.Resets != 1 {
			t.Errorf("sent RESET PAIRING %d times, ran it %d", sent, device.Resets)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// Slower than the timeout: the resend waits for the first reply,
		// and the erase runs once.
		device := newFake(t)
		device.Pairing = []byte{1, 2, 0, 0}
		device.Slow = map[Opcode]time.Duration{OpResetPairing: resetPairingTimeout + time.Second}
		c := helloClient(t, device)
		if _, err := c.Pairing(t.Context()); err != nil {
			t.Fatal(err)
		}
		left, right := testImages(t)
		if err := flash(t.Context(), c, left, right, FlashOptions{ResetPairing: true}); err != nil {
			t.Fatal(err)
		}
		if sent := count(ops(device.Requests), OpResetPairing); sent != 2 || device.Resets != 1 {
			t.Errorf("sent RESET PAIRING %d times, ran it %d", sent, device.Resets)
		}
	})
}
