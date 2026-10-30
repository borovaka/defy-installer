// SPDX-License-Identifier: MIT

package updater

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

// The parsers below read what the Neuron sends. Each must refuse a reply
// or return what the reply holds, and never panic.

func FuzzParseVersion(f *testing.F) {
	f.Add(ut.VersionPayload("v0.2.0", "alice/my-defy", "8c04e7d0123456789abcdef0123456789abcdef0-dirty"))
	f.Add(ut.VersionPayload("unknown", "local", ""))
	f.Fuzz(func(t *testing.T, reply []byte) {
		v, err := parseVersion(reply)
		if err != nil {
			return
		}
		if again := ut.VersionPayload(v.Module, v.Source, v.Commit); !bytes.Equal(again, reply) {
			t.Errorf("%x reads as %+v, which is %x", reply, v, again)
		}
	})
}

func FuzzCheckHello(f *testing.F) {
	supported := ut.Capabilities(signature, 256, 260, 0x5000, 0x1c0000, 10000)
	f.Add(supported)
	f.Add(ut.Capabilities("OTHERUPD", 256, 260, 0x5000, 0x1c0000, 10000))
	f.Fuzz(func(t *testing.T, reply []byte) {
		if err := checkHello(reply); err == nil && !bytes.Equal(reply, supported) {
			t.Errorf("accepted %x", reply)
		}
	})
}

// readFrames reads frames from stream, which arrives at most size bytes a
// Read, until the first error. It returns the frames, the bytes left
// unread and that error.
func readFrames(t *testing.T, stream []byte, size int) ([][]byte, int, error) {
	device := newFake(t)
	device.Output, device.ReadSize = bytes.Clone(stream), size
	c := &Client{port: device}
	var frames [][]byte
	for {
		raw, f, err := c.read(t.Context(), time.Now().Add(100*time.Millisecond))
		if err != nil {
			return frames, len(device.Output), err
		}
		if again := encode(f); !bytes.Equal(again, raw) {
			t.Errorf("%x reads as %+v, which is %x", raw, f, again)
		}
		frames = append(frames, raw)
	}
}

// FuzzClientRead checks that how a stream is split into reads changes
// nothing: the same frames come out, and the same error stops them. A
// stream that is not a frame stops at its first wrong byte, which a split
// read finds sooner.
func FuzzClientRead(f *testing.F) {
	page := encode(frame{opcode: OpPage, session: 7, sequence: 8, payload: bytes.Repeat([]byte("x"), 260)})
	f.Add(slices.Concat(helloFrame, ut.Response(OpHello, 0, 0, ut.Capabilities(signature, 256, 260, 0x5000, 0x1c0000, 10000), 0)), uint8(1))
	f.Add(slices.Concat(page, page[:30]), uint8(7))
	f.Add([]byte("[00:00:01.250,000] <inf> log\r\n"), uint8(3))
	f.Fuzz(func(t *testing.T, stream []byte, size uint8) {
		synctest.Test(t, func(t *testing.T) {
			wholeFrames, wholeLeft, wholeErr := readFrames(t, stream, len(stream)+1)
			splitFrames, splitLeft, splitErr := readFrames(t, stream, int(size%32)+1)
			if !slices.EqualFunc(wholeFrames, splitFrames, bytes.Equal) {
				t.Errorf("%d frames read whole, %d split", len(wholeFrames), len(splitFrames))
			}
			notFrame := errors.Is(wholeErr, errNotFrame)
			switch {
			case notFrame != errors.Is(splitErr, errNotFrame):
				t.Errorf("read whole: %v; split: %v", wholeErr, splitErr)
			case !notFrame && (wholeErr.Error() != splitErr.Error() || wholeLeft != splitLeft):
				t.Errorf("read whole: %v, %d bytes left; split: %v, %d bytes left", wholeErr, wholeLeft, splitErr, splitLeft)
			}
		})
	})
}
