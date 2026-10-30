// SPDX-License-Identifier: MIT

package updater

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

func TestVersion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		commit := "8c04e7d0123456789abcdef0123456789abcdef0-dirty"
		device.Answers = map[Opcode]answer{OpVersion: {Payload: ut.VersionPayload("v0.2.0", "alice/my-defy", commit)}}
		c := helloClient(t, device)
		v, err := c.Version(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if v != (Version{Module: "v0.2.0", Source: "alice/my-defy", Commit: commit}) || !v.Dirty() {
			t.Errorf("got %+v", v)
		}
		if got := hex.EncodeToString(device.Requests[1].Wire); got[:32] != "4455010e000000000000000000000000" {
			t.Errorf("VERSION went as %s", got)
		}
		if err := c.open(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// Both refusals ErrUnsupported describes leave the client usable. PAIRING
// refused so gives ErrNoPairing instead.
func TestRequestsOlderFirmwareRefuses(t *testing.T) {
	for _, tt := range []struct {
		op  Opcode
		ask func(ctx context.Context, c *Client) error
		is  error
	}{
		{OpVersion, func(ctx context.Context, c *Client) error { _, err := c.Version(ctx); return err }, ErrUnsupported},
		{OpHalfVersions, func(ctx context.Context, c *Client) error { _, err := c.HalfVersions(ctx); return err }, ErrUnsupported},
		{OpSplitStatus, func(ctx context.Context, c *Client) error { _, err := c.SplitStatus(ctx); return err }, ErrUnsupported},
		{OpBattery, func(ctx context.Context, c *Client) error { _, err := c.Battery(ctx); return err }, ErrUnsupported},
		{OpClearKeymap, func(ctx context.Context, c *Client) error { return c.ClearKeymapChanges(ctx) }, ErrUnsupported},
		{OpPairing, func(ctx context.Context, c *Client) error { _, err := c.Pairing(ctx); return err }, ErrNoPairing},
	} {
		for _, status := range []int16{-enotconn, -enotsup} {
			synctest.Test(t, func(t *testing.T) {
				device := newFake(t)
				if status != -enotconn {
					device.Filter = func(r request, b []byte) []byte {
						if r.Op == tt.op {
							return ut.Response(r.Op, r.Session, r.Sequence, nil, status)
						}
						return b
					}
				}
				c := helloClient(t, device)
				err := tt.ask(t.Context(), c)
				refused, ok := errors.AsType[*DeviceError](err)
				if !errors.Is(err, tt.is) || !ok || refused.Status != status {
					t.Errorf("%v: got %v", tt.op, err)
				}
				if err := c.open(t.Context()); err != nil {
					t.Errorf("%v: the client is unusable: %v", tt.op, err)
				}
			})
		}
	}
}

func TestVersionRejectsBadReplies(t *testing.T) {
	long := string(make([]byte, 33))
	for _, tt := range []struct {
		reply []byte
		want  string
	}{
		{append([]byte{2, 0, 0, 0}, ut.VersionPayload("v", "s", "c")[4:]...), "starts 02000000, not format 1"},
		{append([]byte{1, 0, 1, 0}, ut.VersionPayload("v", "s", "c")[4:]...), "starts 01000100"},
		{ut.VersionPayload("v0.1.0", "local", "")[:9], "too short"},
		{ut.VersionPayload("v0.1.0", "local", "")[:13], "too short"},
		{append(ut.VersionPayload("v", "s", ""), 0), "1 bytes after its strings"},
		{ut.VersionPayload(long, "s", ""), "string 1 33 bytes, more than 32"},
		{ut.VersionPayload("v\x01", "s", ""), "not printable ASCII"},
	} {
		_, err := parseVersion(tt.reply)
		wantError(t, err, tt.want)
	}
	if _, err := parseVersion(ut.VersionPayload("v0.1.0", "local", "")); err != nil {
		t.Errorf("an empty commit: %v", err)
	}
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Answers = map[Opcode]answer{OpVersion: {Payload: ut.VersionPayload("v\x01", "s", "")}}
		c := helloClient(t, device)
		_, err := c.Version(t.Context())
		wantError(t, err, "not printable ASCII")
		// The reply came whole and changed no session.
		if err := c.open(t.Context()); err != nil {
			t.Errorf("the client is unusable: %v", err)
		}
	})
}

func TestHalfVersions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		reply := ut.Snapshot(36, 61000,
			ut.HalfRecord(0, 2, 1500, 0xc0ffee00, 1, "v0.2.0"),
			ut.HalfRecord(1, 0, 0xffffffff, 0, 0, ""))
		device.Answers = map[Opcode]answer{OpHalfVersions: {Payload: reply}}
		h, err := helloClient(t, device).HalfVersions(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := HalfRecord{Link: Bluetooth, Age: 1500 * time.Millisecond,
			Report: HalfReport{ProgramCRC: 0xc0ffee00, SealVersion: 1, Module: "v0.2.0"}}
		if left := h.Side(firmware.Left); left != want {
			t.Errorf("got %+v\nwant %+v", left, want)
		}
		if h.Side(firmware.Right) != (HalfRecord{}) || h.Side(firmware.NoSide) != (HalfRecord{}) {
			t.Errorf("records %+v", h)
		}
		if !h.Side(firmware.Left).Runs(0xc0ffee00) || h.Side(firmware.Left).Runs(1) || h.Side(firmware.Right).Known() {
			t.Errorf("records %+v", h)
		}
	})
}

func TestHalfVersionsRejectsBadReplies(t *testing.T) {
	good := func() [2][]byte {
		return [2][]byte{ut.HalfRecord(0, 1, 0, 1, 1, "v"), ut.HalfRecord(1, 0, 0xffffffff, 0, 0, "")}
	}
	for _, tt := range []struct {
		name  string
		reply func() []byte
		want  string
	}{
		{"size", func() []byte { r := good(); return ut.Snapshot(48, 0, r[0], r[1]) }, "records of 48 bytes"},
		{"length", func() []byte { r := good(); return ut.Snapshot(36, 0, r[0], r[1])[:79] }, "79 bytes, not 80"},
		{"sides", func() []byte { r := good(); return ut.Snapshot(36, 0, r[1], r[0]) }, "record 0 gives side 1"},
		{"link", func() []byte { r := good(); r[0][1] = 4; return ut.Snapshot(36, 0, r[0], r[1]) }, "link 4"},
		{"report", func() []byte { r := good(); r[0][8] = 2; return ut.Snapshot(36, 0, r[0], r[1]) }, "report has format 2"},
		{"module", func() []byte { r := good(); r[0][18] = 0x80; return ut.Snapshot(36, 0, r[0], r[1]) }, "not printable ASCII"},
	} {
		if _, err := parseHalfVersions(tt.reply()); err == nil {
			t.Errorf("%s: no error", tt.name)
		} else {
			wantError(t, err, tt.want)
		}
	}
}

func TestClearKeymapChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The reply takes seconds when many changes are saved.
		device := newFake(t)
		device.Answers = map[Opcode]answer{OpClearKeymap: {}}
		device.Slow = map[Opcode]time.Duration{OpClearKeymap: 6 * time.Second}
		c := helloClient(t, device)
		if err := c.ClearKeymapChanges(t.Context()); err != nil {
			t.Fatal(err)
		}
		if n := count(ops(device.Requests), OpClearKeymap); n != 1 {
			t.Errorf("sent CLEAR KEYMAP CHANGES %d times", n)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// Studio unlocked: the client goes on, so the request can go again.
		device := newFake(t)
		device.Answers = map[Opcode]answer{OpClearKeymap: {Status: -ebusy}}
		c := helloClient(t, device)
		err := c.ClearKeymapChanges(t.Context())
		if !errors.Is(err, ErrStudioUnlocked) || errors.Is(err, ErrUnsupported) {
			t.Errorf("got %v", err)
		}
		device.Answers[OpClearKeymap] = answer{}
		if err := c.ClearKeymapChanges(t.Context()); err != nil {
			t.Error(err)
		}
	})
}

func TestSplitStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Answers = map[Opcode]answer{OpSplitStatus: {Payload: ut.Snapshot(48, 5000, ut.SplitSlot(0, 7, 12), ut.SplitSlot(1, 1, 0))}}
		n, err := helloClient(t, device).SplitStatus(t.Context())
		if err != nil || n != 1 {
			t.Errorf("got %d, %v", n, err)
		}
	})
	if _, err := parseSplitStatus(ut.Snapshot(48, 0, ut.SplitSlot(0, 11, 0), ut.SplitSlot(1, 0, 0))); err == nil {
		t.Error("phase 11 passed")
	}
	if _, err := parseSplitStatus(ut.Snapshot(48, 0, ut.SplitSlot(1, 0, 0), ut.SplitSlot(0, 0, 0))); err == nil {
		t.Error("swapped slots passed")
	}
}

func TestBattery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Answers = map[Opcode]answer{OpBattery: {Payload: ut.Snapshot(48, 0,
			ut.BatteryRecord(0, 0x0f, 2, 80, ut.BatteryFrame(1, 0x00)),
			ut.BatteryRecord(1, 0x07, 1, 255, ut.BatteryFrame(0, 0x10)))}}
		b, err := helloClient(t, device).Battery(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		left, right := b.Side(firmware.Left), b.Side(firmware.Right)
		if !left.Valid || left.Level != 80 || !left.Bluetooth || left.Cable || left.Charge != NotCharging {
			t.Errorf("left %+v", left)
		}
		if right.Valid || !right.Connected || !right.Cable || right.Charge != Charging {
			t.Errorf("right %+v", right)
		}
	})
	for status, want := range map[byte]Charge{0x20: Charged, 0x30: ChargeFault} {
		if got := chargeOf(ut.BatteryFrame(0, status)); got != want {
			t.Errorf("status 0x%02x: %v, want %v", status, got, want)
		}
	}
	broken := ut.BatteryFrame(0, 0x10)
	broken[20] ^= 1
	if chargeOf(broken) != ChargeUnknown {
		t.Error("a frame with a bad CRC gave a charge state")
	}
	// Another charger: the identity's low two bits are its revision.
	for id, want := range map[byte]Charge{0x94: Charging, 0x97: Charging, 0x98: ChargeUnknown, 0x14: ChargeUnknown} {
		f := ut.BatteryFrame(0, 0x10)
		f[22] = id
		if got := chargeOf(append(f[:30], binary.LittleEndian.AppendUint16(nil, crc16(f[:30]))...)); got != want {
			t.Errorf("identity 0x%02x: %v, want %v", id, got, want)
		}
	}
}

func TestBadBatteryRecordIsUnknown(t *testing.T) {
	good := ut.BatteryRecord(1, 0x0f, 1, 60, ut.BatteryFrame(0, 0x20))
	for _, bad := range [][]byte{
		ut.BatteryRecord(0, 0x0f, 1, 101, ut.BatteryFrame(1, 0)),
		ut.BatteryRecord(1, 0x0f, 1, 80, ut.BatteryFrame(1, 0)),
	} {
		b, err := parseBattery(ut.Snapshot(48, 0, bad, good))
		if err != nil {
			t.Fatal(err)
		}
		right := b.Side(firmware.Right)
		if b.Side(firmware.Left) != (BatteryRecord{}) || !right.Valid || right.Level != 60 || right.Charge != Charged {
			t.Errorf("left %+v, right %+v", b.Side(firmware.Left), right)
		}
	}
	// A level over 100% without the valid flag is no level at all.
	b, err := parseBattery(ut.Snapshot(48, 0, ut.BatteryRecord(0, 0x03, 2, 255, ut.BatteryFrame(1, 0)), good))
	if left := b.Side(firmware.Left); err != nil || left.Valid || left.Level != 0 || !left.Connected || !left.Bluetooth {
		t.Errorf("left %+v, %v", left, err)
	}
	if _, err := parseBattery(ut.Snapshot(48, 0, good)); err == nil {
		t.Error("one record passed")
	}
}
