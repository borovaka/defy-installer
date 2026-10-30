// SPDX-License-Identifier: MIT

package updater

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/borovaka/defy-installer/internal/firmware"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

func TestInvalidProbeReplyStopsFlash(t *testing.T) {
	for _, tt := range []struct {
		name  string
		reply []byte
		want  string
	}{
		{"side", ut.HalfInfo(1, 0, 0, 1, 0x5000, 0, 0), "gives side 1"},
		{"seal flag", ut.HalfInfo(0, 2, 0, 1, 0x5000, 0, 0), "seal flag 2"},
		{"reserved", ut.HalfInfo(0, 0, 1, 1, 0x5000, 0, 0), "reserved 0x1"},
		{"hardware", ut.HalfInfo(0, 0, 0, 2, 0x5000, 0, 0), "hardware version 2"},
		{"flash start", ut.HalfInfo(0, 0, 0, 1, 0x6000, 0, 0), "application at 0x6000"},
		{"length", ut.HalfInfo(0, 0, 0, 1, 0x5000, 0, 0)[:19], "19 bytes, not 20"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				device := newFake(t)
				device.Filter = func(r request, b []byte) []byte {
					if r.Op == OpProbe {
						return ut.Reply(r, tt.reply)
					}
					return b
				}
				left, right := testImages(t)
				c := helloClient(t, device)
				wantError(t, flash(t.Context(), c, left, right, FlashOptions{}), tt.want)
				if slices.Contains(device.Executed, OpStart) || !c.failed {
					t.Errorf("requests %v; failed %v", device.Executed, c.failed)
				}
			})
		})
	}
}

func TestOnlyAFailedProbeIsAProbeError(t *testing.T) {
	left, right := testImages(t)
	synctest.Test(t, func(t *testing.T) {
		// Refused: the updater's status comes with it, and nothing follows.
		device := newFake(t)
		device.Reject[OpProbe] = -19
		_, err := Flash(t.Context(), helloClient(t, device), left, right, FlashOptions{UnlessCurrent: true})
		probe, isProbe := errors.AsType[*ProbeError](err)
		refused, isDevice := errors.AsType[*DeviceError](err)
		if !isProbe || probe.Side != firmware.Left || !isDevice || refused.Status != -19 {
			t.Errorf("got %v", err)
		}
		wantOps(t, "requests", device.Executed, OpHello, OpOpen, OpProbe)
	})
	synctest.Test(t, func(t *testing.T) {
		// Not answered.
		device := newFake(t)
		device.Filter = func(r request, b []byte) []byte {
			if r.Op == OpProbe {
				return nil
			}
			return b
		}
		_, err := Flash(t.Context(), helloClient(t, device), left, right, FlashOptions{UnlessCurrent: true})
		probe, ok := errors.AsType[*ProbeError](err)
		if !ok || probe.Side != firmware.Left || !errors.Is(err, errNoReply) {
			t.Errorf("got %v", err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// A half the updater does not support.
		device := newFake(t)
		device.Filter = func(r request, b []byte) []byte {
			if r.Op == OpProbe {
				return ut.Reply(r, ut.HalfInfo(0, 0, 0, 2, 0x5000, 0, 0))
			}
			return b
		}
		_, err := Flash(t.Context(), helloClient(t, device), left, right, FlashOptions{UnlessCurrent: true})
		if _, ok := errors.AsType[*ProbeError](err); ok {
			t.Errorf("got %v", err)
		}
		wantError(t, err, "the left half has hardware version 2")
	})
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		device := newFake(t)
		device.Filter = func(r request, b []byte) []byte {
			if r.Op == OpOpen {
				cancel()
			}
			return b
		}
		_, err := ProbeHalves(ctx, helloClient(t, device))
		if _, ok := errors.AsType[*ProbeError](err); ok || !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
	})
}

func TestRefusedOpenIsClassified(t *testing.T) {
	for status, busy := range map[int16]bool{-ebusy: true, -5: false} {
		synctest.Test(t, func(t *testing.T) {
			device := newFake(t)
			device.Reject[OpOpen] = status
			left, right := testImages(t)
			err := flash(t.Context(), helloClient(t, device), left, right, FlashOptions{})
			if errors.Is(err, ErrHalvesBusy) != busy || errors.Is(err, ErrHalvesSilent) == busy {
				t.Errorf("status %d: got %v", status, err)
			}
		})
	}
	// Another request's refusal is neither.
	err := error(&DeviceError{Opcode: OpProbe, Status: -ebusy})
	if errors.Is(err, ErrHalvesBusy) || errors.Is(err, ErrHalvesSilent) {
		t.Errorf("%v matches an OPEN refusal", err)
	}
}

func TestFlashChecksImagesFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		left, right := testImages(t)
		for _, tt := range []struct {
			name        string
			left, right *firmware.HalfImage
			want        string
		}{
			{"swapped", right, left, "built for the right half, not the left"},
			{"two lefts", left, left, "built for the left half, not the right"},
			{"missing", left, nil, "both the left and the right image"},
		} {
			device := newFake(t)
			wantError(t, flash(t.Context(), helloClient(t, device), tt.left, tt.right, FlashOptions{}), tt.want)
			wantOps(t, tt.name+" requests", device.Executed, OpHello)
		}
	})
}

func TestFlashWritesBothHalves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		c := helloClient(t, device)
		left, right := testImages(t)
		if err := flash(t.Context(), c, left, right, FlashOptions{}); err != nil {
			t.Fatal(err)
		}
		wantOps(t, "first requests", device.Executed[:5], OpHello, OpOpen, OpProbe, OpProbe, OpStart)
		wantOps(t, "last request", device.Executed[len(device.Executed)-1:], OpClose)
		if !slices.Equal(device.Finished, []byte{0, 1}) {
			t.Errorf("finished %v", device.Finished)
		}
		if device.Opened {
			t.Error("the session is still open")
		}
		for i, image := range []*firmware.HalfImage{left, right} {
			if !bytes.Equal(device.Memory[i], image.TransferData()) {
				t.Errorf("side %d holds %d bytes, not the image's %d padded bytes", i, len(device.Memory[i]), len(image.TransferData()))
			}
		}
		if len(device.Pages) != totalPages(left, right) {
			t.Errorf("wrote %d pages, want %d", len(device.Pages), totalPages(left, right))
		}
	})
}

func TestFlashReportsProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		c := helloClient(t, device)
		left, right := testImages(t)
		type step struct {
			side        firmware.Side
			done, total int
			finished    int // halves the device had finished
		}
		var steps []step
		progress := func(side firmware.Side, done, total int) {
			steps = append(steps, step{side, done, total, len(device.Finished)})
		}
		if err := flash(t.Context(), c, left, right, FlashOptions{Progress: progress}); err != nil {
			t.Fatal(err)
		}
		// 1500 bytes of application make a package of two sectors. Each
		// half's last page leaves done below total, and its FINISH makes
		// done total.
		split := slices.IndexFunc(steps, func(s step) bool { return s.side == firmware.Right })
		if split < 2 {
			t.Fatalf("got %v", steps)
		}
		for i, got := range [][]step{steps[:split], steps[split:]} {
			want := []step{{halves[i], 8192 - 256, 8192, i}, {halves[i], 8192, 8192, i + 1}}
			if len(got) < 2 || !slices.Equal(got[len(got)-2:], want) {
				t.Errorf("the %v half ends %v, want %v", halves[i], got, want)
			}
		}
	})
}

func TestFlashUnlessCurrent(t *testing.T) {
	left, right := testImages(t)
	other := halfImage(t, firmware.Right, 0x10203041, 0x42, 1500)
	for _, tt := range []struct {
		name   string
		holds  [2]*firmware.HalfImage
		reset  bool
		wrote  bool
		before []Opcode
	}{
		{"both current", [2]*firmware.HalfImage{left, right}, false, false, nil},
		{"one differs", [2]*firmware.HalfImage{left, other}, false, true, nil},
		{"no seals", [2]*firmware.HalfImage{}, false, true, nil},
		{"current, with a pairing reset", [2]*firmware.HalfImage{left, right}, true, true, []Opcode{OpResetPairing}},
	} {
		synctest.Test(t, func(t *testing.T) {
			device := newFake(t)
			for i, image := range tt.holds {
				if image != nil {
					device.Halves[i].CRC, device.Halves[i].Version = image.ProgramCRC, image.Version
				}
			}
			device.Pairing = []byte{1, 2, 0, 0}
			c := helloClient(t, device)
			if _, err := c.Pairing(t.Context()); err != nil {
				t.Fatal(err)
			}
			wrote, err := Flash(t.Context(), c, left, right, FlashOptions{ResetPairing: tt.reset, UnlessCurrent: true})
			if err != nil || wrote != tt.wrote {
				t.Fatalf("%s: wrote %v, %v", tt.name, wrote, err)
			}
			want := slices.Concat([]Opcode{OpHello, OpPairing, OpOpen, OpProbe, OpProbe}, tt.before)
			if tt.wrote {
				if !slices.Contains(device.Executed, OpStart) || len(device.Finished) != 2 {
					t.Errorf("%s: ran %v", tt.name, device.Executed)
				}
			} else {
				wantOps(t, tt.name, device.Executed, append(want, OpClose)...)
			}
		})
	}
}
