// SPDX-License-Identifier: MIT

package updater

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
	"github.com/borovaka/defy-installer/internal/neuron"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// fakeUpdater, request and answer are updatertest's types for Opcode.
type (
	fakeUpdater = ut.Updater[Opcode]
	request     = ut.Request[Opcode]
	answer      = ut.Answer
)

func newFake(t testing.TB) *fakeUpdater {
	return ut.New[Opcode](t)
}

// ops returns the opcodes of requests, in order.
func ops(requests []request) []Opcode {
	var out []Opcode
	for _, r := range requests {
		out = append(out, r.Op)
	}
	return out
}

// count counts op among opcodes.
func count(opcodes []Opcode, op Opcode) int {
	n := 0
	for _, o := range opcodes {
		if o == op {
			n++
		}
	}
	return n
}

// helloFrame is HELLO as it goes on the wire.
var helloFrame = encode(frame{opcode: OpHello})

// nonce is a source of one session nonce.
func nonce(n uint32) io.Reader {
	return bytes.NewReader(binary.LittleEndian.AppendUint32(nil, n))
}

// newClient returns a client on port with a 100 ms timeout and session
// nonce 0xabcdef01.
func newClient(t *testing.T, port io.ReadWriter) *Client {
	t.Helper()
	return &Client{nonces: nonce(0xabcdef01), port: port, timeout: 100 * time.Millisecond}
}

// helloClient is newClient after HELLO.
func helloClient(t *testing.T, port io.ReadWriter) *Client {
	t.Helper()
	c := newClient(t, port)
	if err := c.hello(t.Context(), c.timeout); err != nil {
		t.Fatal(err)
	}
	return c
}

// halfPackage builds a ZMK package from firmwaretest's
// RecordedHalfApp(fill), filled out to length bytes, at least 1000.
func halfPackage(side firmware.Side, version uint32, fill byte, length int) []byte {
	app := append(firmwaretest.RecordedHalfApp(fill), bytes.Repeat([]byte{fill}, length-1000)...)
	metadata := firmwaretest.Left
	if side == firmware.Right {
		metadata = firmwaretest.Right
	}
	return firmwaretest.HalfPackage(app, version, metadata)
}

func halfImage(t testing.TB, side firmware.Side, version uint32, fill byte, length int) *firmware.HalfImage {
	t.Helper()
	image, err := firmware.ParseHalfImage(side.String()+".bin", halfPackage(side, version, fill, length))
	if err != nil {
		t.Fatal(err)
	}
	return image
}

// testImages are the left and right images most tests write: 1500 bytes
// of application each, version 0x10203040 and fill 0x40 for the left,
// 0x10203041 and 0x41 for the right.
func testImages(t testing.TB) (left, right *firmware.HalfImage) {
	return halfImage(t, firmware.Left, 0x10203040, 0x40, 1500), halfImage(t, firmware.Right, 0x10203041, 0x41, 1500)
}

// totalPages is how many pages both images take.
func totalPages(images ...*firmware.HalfImage) int {
	n := 0
	for _, image := range images {
		n += len(image.TransferData()) / firmware.HalfPageSize
	}
	return n
}

const testSerial = "0123456789ABCDEF"

// The ZMK Neuron's two ports.
var (
	studioPort  = usbserial.Port{Path: "/dev/studio", ID: neuron.ZMKID, Serial: testSerial, Product: "Defy", Location: "1-1"}
	updaterPort = usbserial.Port{Path: "/dev/updater", ID: neuron.ZMKID, Serial: testSerial, Product: "Defy", Location: "1-1"}
)

// listing returns a Lister that always lists ports.
func listing(ports ...usbserial.Port) usbserial.Lister {
	return func(context.Context) ([]usbserial.Port, error) { return ports, nil }
}

// opener opens fakes by path, or fails with an error set for the path, and
// records what it was asked to open.
type opener struct {
	devices map[string]*fakeUpdater
	errs    map[string]error
	calls   []string
}

func (o *opener) open(path string, timeout time.Duration) (Conn, error) {
	o.calls = append(o.calls, path)
	if err := o.errs[path]; err != nil {
		return nil, err
	}
	if d := o.devices[path]; d != nil {
		return d, nil
	}
	return nil, fmt.Errorf("no fake at %s", path)
}

// flash is Flash for a test that does not ask whether it wrote.
func flash(ctx context.Context, c *Client, left, right *firmware.HalfImage, opts FlashOptions) error {
	_, err := Flash(ctx, c, left, right, opts)
	return err
}
