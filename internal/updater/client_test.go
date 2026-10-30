// SPDX-License-Identifier: MIT

package updater

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// The updater's port type fits Conn.
var _ Conn = (*usbserial.Conn)(nil)

func wantError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Errorf("no error, want one containing %q", contains)
	} else if !strings.Contains(err.Error(), contains) {
		t.Errorf("error %q does not contain %q", err, contains)
	}
}

func wantOps(t *testing.T, what string, got []Opcode, want ...Opcode) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s %v, want %v", what, got, want)
	}
}

func TestHelloFrame(t *testing.T) {
	if got := hex.EncodeToString(helloFrame); got != "44550101000000000000000000000000629373eb" {
		t.Errorf("HELLO is %s", got)
	}
	// ZMK Studio's frames start with 0xab, so Studio's port ignores HELLO.
	if bytes.IndexByte(helloFrame, 0xab) >= 0 {
		t.Error("HELLO holds 0xab")
	}
	f, err := decode(helloFrame)
	if err != nil || f.opcode != OpHello || f.session != 0 || f.sequence != 0 || len(f.payload) != 0 {
		t.Errorf("got %+v, %v", f, err)
	}
}

func TestDecodeRejectsBadFrames(t *testing.T) {
	wire := encode(frame{opcode: OpPage, session: 7, sequence: 8, payload: bytes.Repeat([]byte("x"), 260)})
	if len(wire) != 280 {
		t.Fatalf("a full PAGE is %d bytes", len(wire))
	}
	for _, tt := range []struct {
		name string
		wire []byte
		want string
	}{
		{"truncated", wire[:len(wire)-1], "header gives"},
		{"too long", append(bytes.Clone(wire), 0), "header gives"},
		{"shorter than a header", wire[:19], "truncated"},
	} {
		if _, err := decode(tt.wire); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: got %v, want an error containing %q", tt.name, err, tt.want)
		}
	}
}

func TestLostPageReplyIsResent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		lost := false
		device.Filter = func(r request, reply []byte) []byte {
			if r.Op == OpPage && !lost {
				lost = true
				return nil
			}
			return reply
		}
		left, right := testImages(t)
		if err := flash(t.Context(), helloClient(t, device), left, right, FlashOptions{}); err != nil {
			t.Fatal(err)
		}
		var pages []request
		for _, r := range device.Requests {
			if r.Op == OpPage {
				pages = append(pages, r)
			}
		}
		if !bytes.Equal(pages[0].Wire, pages[1].Wire) {
			t.Error("the resend differs from the request")
		}
		if want := totalPages(left, right); len(pages) != want+1 || len(device.Pages) != want {
			t.Errorf("sent %d PAGE requests and wrote %d pages, want %d and %d", len(pages), len(device.Pages), want+1, want)
		}
	})
}

// TestTwoStaleRepliesAreOneTooMany: a stale reply is skipped once only,
// so two late copies of OPEN's reply before PROBE's stop the probe.
func TestTwoStaleRepliesAreOneTooMany(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Filter = func(r request, reply []byte) []byte {
			if r.Op == OpOpen {
				return slices.Concat(reply, reply, reply)
			}
			return reply
		}
		_, err := ProbeHalves(t.Context(), helloClient(t, device))
		wantError(t, err, "does not match the request")
	})
}

// TestLateSessionlessReplyIsSkipped: the updater runs a resent request
// that needs no session again. When the first reply was only late, a
// second one follows, with a later uptime, and the next request skips it.
func TestLateSessionlessReplyIsSkipped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Answers = map[Opcode]answer{OpHalfVersions: {Payload: ut.Snapshot(36, 0,
			ut.HalfRecord(0, 2, 0, 1, 1, "v"), ut.HalfRecord(1, 0, 0xffffffff, 0, 0, ""))}}
		uptime := uint32(1000)
		var late []byte
		device.Filter = func(r request, b []byte) []byte {
			if r.Op != OpSplitStatus {
				return b
			}
			uptime += 100
			b = ut.Response(r.Op, r.Session, r.Sequence, ut.Snapshot(48, uptime, ut.SplitSlot(0, 7, 0), ut.SplitSlot(1, 1, 0)), 0)
			if late == nil {
				late = b
				return nil
			}
			return slices.Concat(late, b)
		}
		c := helloClient(t, device)
		n, err := c.SplitStatus(t.Context())
		if err != nil || n != 1 {
			t.Fatalf("got %d, %v", n, err)
		}
		h, err := c.HalfVersions(t.Context())
		if err != nil || !h.Side(firmware.Left).Known() {
			t.Fatalf("got %+v, %v", h, err)
		}
		wantOps(t, "requests", ops(device.Requests), OpHello, OpSplitStatus, OpSplitStatus, OpHalfVersions)
		if len(device.Output) != 0 {
			t.Errorf("%d bytes left unread", len(device.Output))
		}
	})
}

func TestDeviceErrorStopsFlash(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Reject[OpPage] = -5
		c := helloClient(t, device)
		left, right := testImages(t)
		err := flash(t.Context(), c, left, right, FlashOptions{})
		refused, ok := errors.AsType[*DeviceError](err)
		if !ok || refused.Opcode != OpPage || refused.Status != -5 {
			t.Fatalf("got %v", err)
		}
		wantError(t, err, "left half: ")
		wantOps(t, "requests", device.Executed, OpHello, OpOpen, OpProbe, OpProbe, OpStart, OpPage)
		if len(device.Pages) != 0 {
			t.Errorf("wrote %d pages", len(device.Pages))
		}
		// The client refuses anything more.
		wantError(t, c.page(t.Context(), firmware.HalfPackageStart, make([]byte, 256)), "unusable")
		wantError(t, c.endSession(t.Context()), "unusable")
		wantError(t, c.hello(t.Context(), c.timeout), "unusable")
		if len(device.Requests) != 6 {
			t.Errorf("sent %d requests after the failure", len(device.Requests)-6)
		}
	})
}

func TestSilentUpdaterGetsTwoAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Silent = true
		c := newClient(t, device)
		start := time.Now()
		err := c.hello(t.Context(), c.timeout)
		if !errors.Is(err, errNoReply) || errors.Is(err, errPartialReply) {
			t.Errorf("got %v", err)
		}
		if len(device.Requests) != 2 {
			t.Errorf("sent %d requests", len(device.Requests))
		}
		if waited := time.Since(start); waited >= 210*time.Millisecond {
			t.Errorf("waited %v", waited)
		}
		if !c.failed {
			t.Error("the client is still usable")
		}
	})
}

func TestPartialReplyIsNotResent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Filter = func(_ request, reply []byte) []byte { return reply[:18] }
		c := newClient(t, device)
		err := c.hello(t.Context(), c.timeout)
		if !errors.Is(err, errPartialReply) {
			t.Errorf("got %v", err)
		}
		if len(device.Requests) != 1 {
			t.Errorf("sent %d requests", len(device.Requests))
		}
	})
}

func TestBadRepliesFailWithoutResend(t *testing.T) {
	for _, tt := range []struct {
		name   string
		filter func(r request, reply []byte) []byte
		want   string
	}{
		{"corrupt CRC", func(_ request, b []byte) []byte { return append(b[:len(b)-1:len(b)-1], b[len(b)-1]^1) }, "CRC mismatch"},
		{"other session", func(r request, _ []byte) []byte { return ut.Response(r.Op, r.Session+1, r.Sequence, nil, 0) }, "does not match"},
		{"other sequence", func(r request, _ []byte) []byte { return ut.Response(r.Op, r.Session, r.Sequence+1, nil, 0) }, "does not match"},
		{"other opcode", func(r request, _ []byte) []byte { return ut.Response(r.Op+1, r.Session, r.Sequence, nil, 0) }, "does not match"},
		{"positive status", func(r request, _ []byte) []byte { return ut.Response(r.Op, r.Session, r.Sequence, nil, 1) }, "status 1"},
		{"error with a payload", func(r request, _ []byte) []byte { return ut.Response(r.Op, r.Session, r.Sequence, []byte("x"), -5) }, "status -5 with 1 bytes"},
		{"oversized header", func(r request, b []byte) []byte {
			return append(binary.LittleEndian.AppendUint16(bytes.Clone(b[:12]), 261), b[14:]...)
		}, "more than 260"},
		{"not a frame", func(request, []byte) []byte { return []byte("[00:00:01.250,000] <inf> log\r\n") }, "does not start as an updater frame"},
		{"wrong version", func(_ request, b []byte) []byte { return append([]byte("DU\x02"), b[3:]...) }, "does not start as an updater frame"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				device := newFake(t)
				device.Filter = tt.filter
				c := newClient(t, device)
				wantError(t, c.hello(t.Context(), c.timeout), tt.want)
				if len(device.Requests) != 1 || !c.failed {
					t.Errorf("sent %d requests; failed %v", len(device.Requests), c.failed)
				}
			})
		})
	}
}

func TestHelloRejectsOtherCapabilities(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Filter = func(r request, _ []byte) []byte {
			return ut.Reply(r, ut.Capabilities("OTHERUPD", 256, 260, 0x5000, 0x1c0000, 10000))
		}
		c := newClient(t, device)
		wantError(t, c.hello(t.Context(), c.timeout), "not a supported half updater")
		wantError(t, c.open(t.Context()), "unusable")
		wantOps(t, "requests", device.Executed, OpHello)
	})
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Filter = func(r request, b []byte) []byte { return ut.Reply(r, b[headerSize:len(b)-crcSize-1]) }
		c := newClient(t, device)
		wantError(t, c.hello(t.Context(), c.timeout), "23 bytes, not 24")
	})
}

// port reads and writes through the functions a test sets.
type port struct {
	read   func([]byte) (int, error)
	write  func([]byte) (int, error)
	writes int
}

func (p *port) Read(b []byte) (int, error) { return p.read(b) }

func (p *port) Write(b []byte) (int, error) {
	p.writes++
	return p.write(b)
}

func TestPortFailuresAreNotRetried(t *testing.T) {
	lost := errors.New("connection lost")
	written := func(b []byte) (int, error) { return len(b), nil }
	for _, p := range []*port{
		{write: func([]byte) (int, error) { return 0, lost }},
		{read: func([]byte) (int, error) { return 0, lost }, write: written},
	} {
		c := newClient(t, p)
		if err := c.hello(t.Context(), c.timeout); !errors.Is(err, lost) || p.writes != 1 || !c.failed {
			t.Errorf("got %v after %d writes; failed %v", err, p.writes, c.failed)
		}
	}
}

func TestZeroNonce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		c := helloClient(t, device)
		c.nonces = nonce(0)
		if err := c.open(t.Context()); err != nil {
			t.Fatal(err)
		}
		if last := device.Requests[len(device.Requests)-1]; c.session != 1 || last.Session != 1 || last.Sequence != 1 {
			t.Errorf("session %d; OPEN went with session %d, sequence %d", c.session, last.Session, last.Sequence)
		}
	})
}

func TestPageChecksTheAddressFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		c := helloClient(t, device)
		if err := c.open(t.Context()); err != nil {
			t.Fatal(err)
		}
		page := bytes.Repeat([]byte("x"), 256)
		wantError(t, c.page(t.Context(), firmware.HalfPackageStart+1, page), "not an aligned 256-byte page")
		wantOps(t, "requests", device.Executed, OpHello, OpOpen)
		if c.failed {
			t.Error("a refused page made the client unusable")
		}
	})
}

func TestWrongReplyStopsFlash(t *testing.T) {
	for _, tt := range []struct {
		op    Opcode
		reply []byte
	}{
		{OpPage, binary.LittleEndian.AppendUint32(nil, 0x5000)},
		{OpFinish, make([]byte, 8)},
	} {
		synctest.Test(t, func(t *testing.T) {
			device := newFake(t)
			device.Filter = func(r request, b []byte) []byte {
				if r.Op == tt.op {
					return ut.Reply(r, tt.reply)
				}
				return b
			}
			left, right := testImages(t)
			wantError(t, flash(t.Context(), helloClient(t, device), left, right, FlashOptions{}), "unexpected updater "+tt.op.String()+" reply")
			if n := count(device.Executed, tt.op); n != 1 || len(device.Memory[1]) != 0 {
				t.Errorf("ran %v %d times; wrote %d bytes to the right half", tt.op, n, len(device.Memory[1]))
			}
		})
	}
}

func TestCancelStopsBetweenFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		device := newFake(t)
		device.Filter = func(r request, b []byte) []byte {
			if r.Op == OpPage {
				cancel()
			}
			return b
		}
		left, right := testImages(t)
		c := helloClient(t, device)
		err := flash(ctx, c, left, right, FlashOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
		// The first PAGE went whole and its reply came back; nothing
		// followed.
		wantOps(t, "requests", ops(device.Requests), OpHello, OpOpen, OpProbe, OpProbe, OpStart, OpPage)
		if len(device.Pages) != 1 || len(device.Input) != 0 || len(device.Output) != 0 {
			t.Errorf("wrote %d pages; %d bytes of a request and %d of a reply left over",
				len(device.Pages), len(device.Input), len(device.Output))
		}
		wantError(t, c.endSession(t.Context()), "unusable")
	})
}

func TestCancelPreventsResend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		device := newFake(t)
		device.Filter = func(r request, b []byte) []byte {
			if r.Op == OpOpen {
				cancel()
				return nil
			}
			return b
		}
		c := helloClient(t, device)
		if err := c.open(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
		wantOps(t, "requests", ops(device.Requests), OpHello, OpOpen)
	})
}

func TestCancelStopsTheWaitForASessionlessReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Answers = map[Opcode]answer{OpClearKeymap: {}}
		device.Slow = map[Opcode]time.Duration{OpClearKeymap: 6 * time.Second}
		c := helloClient(t, device)
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			time.Sleep(time.Second)
			cancel()
		}()
		start := time.Now()
		err := c.ClearKeymapChanges(ctx)
		if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second+50*time.Millisecond {
			t.Errorf("got %v after %v", err, time.Since(start))
		}
	})
}

func TestCancelWaitsForASessionReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		device.Pairing = []byte{1, 2, 0, 0}
		device.Slow = map[Opcode]time.Duration{OpResetPairing: 2 * time.Second}
		c := helloClient(t, device)
		if _, err := c.Pairing(t.Context()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		device.Filter = func(r request, b []byte) []byte {
			if r.Op == OpResetPairing {
				cancel()
			}
			return b
		}
		left, right := testImages(t)
		start := time.Now()
		err := flash(ctx, c, left, right, FlashOptions{ResetPairing: true})
		if !errors.Is(err, context.Canceled) || time.Since(start) < 2*time.Second {
			t.Errorf("got %v after %v", err, time.Since(start))
		}
		// RESET PAIRING's reply was read whole; nothing followed it.
		wantOps(t, "requests", ops(device.Requests), OpHello, OpPairing, OpOpen, OpProbe, OpProbe, OpResetPairing)
		if len(device.Held)+len(device.Output) != 0 {
			t.Errorf("%d bytes left unread", len(device.Held)+len(device.Output))
		}
	})
}
