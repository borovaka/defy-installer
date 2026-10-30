// SPDX-License-Identifier: MIT

package focus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
	"github.com/borovaka/defy-installer/internal/focus/focustest"
)

// standIn stands in for Dygma's keyscanner.bin, as in flow's recorded
// restore.
func standIn(t testing.TB) *firmware.HalfImage {
	t.Helper()
	image, err := firmware.ParseHalfImage("keyscanner.bin", ft.HalfPackage(ft.RecordedHalfApp(0x0a), 101, ft.Left))
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func wantError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Errorf("error %v, want one containing %q", err, contains)
	}
}

func restoreOn(t *testing.T, f *focustest.Neuron) error {
	return Restore(t.Context(), f, standIn(t), nil, ReadyWait{})
}

func TestRestoreWritesBothHalves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := focustest.New(t)
		image := standIn(t)
		var done []string
		err := Restore(t.Context(), f, image, func(side firmware.Side, n, total int) {
			if n == total {
				done = append(done, side.String())
			}
		}, ReadyWait{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(f.Finished, []int{0, 1}) || !slices.Equal(done, []string{"right", "left"}) {
			t.Errorf("finished %v, reported %v", f.Finished, done)
		}
		data := image.TransferData()
		for side := range 2 {
			if !bytes.Equal(f.Memory[side][:len(data)], data) {
				t.Errorf("half %d holds other data", side)
			}
			var addrs []uint32
			for _, p := range f.Packets {
				if p.Side == side {
					addrs = append(addrs, p.Address)
				}
			}
			for i, a := range addrs {
				if a != firmware.HalfPackageStart+uint32(i*256) {
					t.Fatalf("half %d: packet %d at 0x%x", side, i, a)
				}
			}
		}
		first := slices.Index(f.Commands, "upgrade.keyscanner.sendWrite")
		if !slices.Contains(f.Commands[:first], "upgrade.keyscanner.begin 0") ||
			!slices.Contains(f.Commands[:first], "upgrade.keyscanner.begin 1") {
			t.Error("a half was written before both were checked")
		}
		for i, c := range f.Commands {
			if c == "upgrade.keyscanner.finish" &&
				!slices.Equal(f.Commands[i-2:i], []string{"upgrade.keyscanner.validate", "upgrade.keyscanner.getInfo"}) {
				t.Errorf("finish after %v", f.Commands[i-2:i])
			}
		}
	})
}

func TestUnsupportedOrMissingHalvesStopBeforeAnyWrite(t *testing.T) {
	for name, change := range map[string]func(*focustest.Neuron){
		"hardware 2":      func(f *focustest.Neuron) { f.Hardware[1] = 2 },
		"other start":     func(f *focustest.Neuron) { f.Starts[1] = 0x10005000 },
		"left not cabled": func(f *focustest.Neuron) { f.Connected[1] = false },
	} {
		synctest.Test(t, func(t *testing.T) {
			f := focustest.New(t)
			change(f)
			if err := restoreOn(t, f); err == nil || len(f.Packets) > 0 {
				t.Errorf("%s: got %v after %d packets", name, err, len(f.Packets))
			}
		})
	}
}

func TestOtherNeuronsAreRefusedBeforeTheUpdater(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := focustest.New(t)
		f.Name = "Raise"
		wantError(t, restoreOn(t, f), "expected a stock Defy-wireless Neuron")
		if slices.Contains(f.Commands, "upgrade.start") {
			t.Error("the updater started")
		}
	})
	for _, version := range []string{"", "v2.2.1", "v2.3.1", "2.3.0", "v2.3.0-custom"} {
		synctest.Test(t, func(t *testing.T) {
			f := focustest.New(t)
			f.Version = version
			err := restoreOn(t, f)
			wantError(t, err, "unsupported Neuron firmware version")
			if !slices.Equal(f.Commands, []string{"help", "hardware.version", "version"}) {
				t.Errorf("%q: sent %v", version, f.Commands)
			}
			if err != nil && strings.Contains(err.Error(), "Unplug") {
				t.Errorf("%q: the error asks for a replug although nothing was reset", version)
			}
		})
	}
}

// A Neuron whose help lacks a command Restore uses is refused at the help,
// before upgrade.start.
func TestNeuronsWithoutTheCommandsAreRefusedFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listed := slices.DeleteFunc(slices.Clone(restoreCommands), func(c string) bool {
			return c == "upgrade.start" || c == "upgrade.keyscanner.finish"
		})
		port := &scripted{reply: []byte(strings.Join(append([]string{"help"}, listed...), "\r\n") + "\r\n.\r\n")}
		err := Restore(t.Context(), port, standIn(t), nil, ReadyWait{})
		wantError(t, err, "the Neuron lacks the Focus commands upgrade.start, upgrade.keyscanner.finish")
		if port.writes != 1 {
			t.Errorf("wrote %d times", port.writes)
		}
	})
}

func TestRestoreWaitsForTheUpdater(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := focustest.New(t)
		f.ReadyAfter = 2
		if err := restoreOn(t, f); err != nil || f.ReadyQueries != 3 {
			t.Errorf("got %v after %d queries", err, f.ReadyQueries)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		f := focustest.New(t)
		f.ReadyAfter = 1 << 30
		start := time.Now()
		err := restoreOn(t, f)
		wantError(t, err, "did not get ready for the update in time")
		wantError(t, err, "Unplug the Neuron from this computer")
		if len(f.Packets) > 0 || time.Since(start) < readyTimeout {
			t.Errorf("%d packets after %v", len(f.Packets), time.Since(start))
		}
	})
}

func TestALostStartReplyStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := focustest.New(t)
		f.SilentOn = "upgrade.start"
		err := restoreOn(t, f)
		wantError(t, err, "Dygma's firmware may still hold the halves")
		wantError(t, err, "run defy-installer uninstall again")
		wantError(t, err, ReconnectGuidance)
		if f.Commands[len(f.Commands)-1] != "upgrade.start" || len(f.Packets) > 0 || len(f.Finished) > 0 {
			t.Errorf("sent %v", f.Commands)
		}
	})
}

func TestARefusedOrUnansweredPacketIsNeverRetried(t *testing.T) {
	for _, drop := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			f := focustest.New(t)
			if drop {
				f.DropPacket = 3
			} else {
				f.RejectPacket = 3
			}
			if err := restoreOn(t, f); err == nil || len(f.Packets) != 3 || len(f.Finished) > 0 {
				t.Errorf("got %v, %d packets, finished %v", err, len(f.Packets), f.Finished)
			}
		})
	}
}

func TestBadMetadataOrValidationBlocksFinish(t *testing.T) {
	for _, change := range []func(*focustest.Neuron){
		func(f *focustest.Neuron) { f.CorruptMetadata = true },
		func(f *focustest.Neuron) { f.InvalidProgram = true },
	} {
		synctest.Test(t, func(t *testing.T) {
			f := focustest.New(t)
			change(f)
			if err := restoreOn(t, f); err == nil || len(f.Finished) > 0 {
				t.Errorf("got %v, finished %v", err, f.Finished)
			}
		})
	}
}

func TestRestoreStopsBetweenCommandsWhenCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := focustest.New(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		err := Restore(ctx, f, standIn(t), func(_ firmware.Side, done, _ int) {
			if done == 512 {
				cancel()
			}
		}, ReadyWait{})
		if !errors.Is(err, context.Canceled) || len(f.Packets) != 2 || len(f.Finished) > 0 {
			t.Errorf("got %v after %d packets", err, len(f.Packets))
		}
	})
}

// scripted is a port whose reads return one prepared reply, at most size
// bytes a Read when size is set, then time out.
type scripted struct {
	reply  []byte
	size   int
	writes int
}

func (s *scripted) Write(p []byte) (int, error) {
	s.writes++
	return len(p), nil
}

func (s *scripted) Read(p []byte) (int, error) {
	if len(s.reply) == 0 {
		time.Sleep(50 * time.Millisecond)
		return 0, os.ErrDeadlineExceeded
	}
	if s.size > 0 {
		p = p[:min(len(p), s.size)]
	}
	n := copy(p, s.reply)
	s.reply = s.reply[n:]
	return n, nil
}

func TestBadRepliesAreRefused(t *testing.T) {
	for _, reply := range []string{"true\r\n", "true\x00\r\n.\r\n", strings.Repeat("x", 16385)} {
		synctest.Test(t, func(t *testing.T) {
			c := &client{rw: &scripted{reply: []byte(reply)}}
			if _, err := c.request(t.Context(), "test", nil); err == nil {
				t.Errorf("%.32q: accepted", reply)
			}
		})
	}
}

func TestRepliesMustBeWhatTheCommandGives(t *testing.T) {
	for _, value := range []string{"1", "false true", "error", ""} {
		synctest.Test(t, func(t *testing.T) {
			_, err := (&client{rw: &scripted{reply: []byte(value + "\r\n.\r\n")}}).boolean(t.Context(), "test", nil)
			wantError(t, err, "expected true or false")
		})
	}
	synctest.Test(t, func(t *testing.T) {
		c := &client{rw: &scripted{reply: []byte("true\r\n.\r\n")}}
		wantError(t, c.empty(t.Context(), "upgrade.start"), `upgrade.start: unexpected reply "true"`)
	})
	for _, reply := range []string{"1 20480 1 2 false", "1 20480 1 2 true extra", "1 -1 1 2 true",
		"1 20480 4294967296 2 true", "1 0x5000 1 2 true", "2 20480 1 2 true", "1 0 1 2 true"} {
		if _, err := parseInfo(reply); err == nil {
			t.Errorf("%q: accepted", reply)
		}
	}
	if info, err := parseInfo("1 20480 101 784248692 true"); err != nil || info != (halfInfo{1, 0x5000, 101, 784248692}) {
		t.Errorf("got %+v, %v", info, err)
	}
}

func TestAHalfThatChangesOrMisreportsStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The right half's seal changes between the first read and its write.
		f := focustest.New(t)
		f.ChangeAfterPreflight = true
		wantError(t, restoreOn(t, f), "keyscanner information changed")
		if len(f.Packets) > 0 {
			t.Errorf("%d packets sent", len(f.Packets))
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// The right half validates, then reports another version.
		f := focustest.New(t)
		f.Misreport = true
		wantError(t, restoreOn(t, f), "reports version 0x00000066")
		if len(f.Finished) > 0 {
			t.Errorf("finished %v", f.Finished)
		}
	})
}
