// SPDX-License-Identifier: MIT

// Package updater talks to the ZMK Neuron's half updater: the second USB
// serial port of a Neuron running zmk-defy-wireless, which writes the
// halves' firmware through their factory bootloaders while the Neuron keeps
// running. zmk-defy-wireless's docs/half-updater.md describes the protocol.
// The same port says what the Neuron and the halves run, how the halves
// are connected and how their batteries stand, and clears the keymap
// changes ZMK Studio saved.
//
// The client checks its context between frames, and between reads while
// it waits for the reply to a request that needs no session. A cancelled
// write stops with whole frames sent; closing the port then ends the
// session and releases the halves.
package updater

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
)

// DefaultTimeout is the usual reply timeout.
const DefaultTimeout = 3 * time.Second

const (
	// idleLimit is how long the updater keeps a session without a valid
	// request.
	idleLimit = 10 * time.Second
	// helloTimeout caps the wait for HELLO's reply, in Identify and in
	// Connect.
	helloTimeout = 350 * time.Millisecond
	// signature starts HELLO's reply, and names the protocol's version.
	signature = "DEFYUPD1"
	helloSize = 24
)

var (
	// ErrHalvesBusy matches OPEN refused with EBUSY: another session is
	// open or closing, or the Neuron is still releasing the halves after a
	// restart or is entering its bootloader. OPEN can pass a few seconds
	// later.
	ErrHalvesBusy = errors.New("the Neuron is busy with the halves")
	// ErrHalvesSilent matches OPEN refused with any other status: the
	// halves did not answer through their factory bootloaders, as when a
	// cable is out or a half is switched on.
	ErrHalvesSilent = errors.New("the halves did not answer OPEN")

	// errNoReply reports that no byte of a reply arrived in time.
	errNoReply = errors.New("no reply")
	// errPartialReply reports a reply that stopped part way through its
	// frame.
	errPartialReply = errors.New("the reply stopped part way through a frame")
	errUnusable     = errors.New("the updater client is unusable after a failed request")
	// errNotFrame reports a reply whose first bytes are not those of an
	// updater frame, as from a port that prints a log.
	errNotFrame = errors.New("the reply does not start as an updater frame")
)

// frameStart is how every updater frame starts: the magic and the
// protocol's version.
var frameStart = []byte{magic[0], magic[1], protocolVersion}

// staleSize is how much of a reply's header names its request: the magic,
// the protocol's version, the opcode, the session and the sequence.
const staleSize = 12

// DeviceError is a request the updater refused.
type DeviceError struct {
	Opcode Opcode
	// Status is a negative errno from picolibc.
	Status int16
}

func (e *DeviceError) Error() string {
	return fmt.Sprintf("updater %v failed with status %d", e.Opcode, e.Status)
}

// Is matches an OPEN refusal to ErrHalvesBusy when its status is EBUSY,
// and to ErrHalvesSilent otherwise.
func (e *DeviceError) Is(target error) bool {
	if e.Opcode != OpOpen {
		return false
	}
	busy := e.Status == -ebusy
	return target == ErrHalvesBusy && busy || target == ErrHalvesSilent && !busy
}

// Client exchanges frames with the half updater. After a failed request it
// refuses any other; close the port, which ends the session. A refused
// request that needs no session, or a reply to one that does not parse,
// leaves it usable, since neither changes a session.
type Client struct {
	// nonces is where session nonces come from; nil is crypto/rand.
	nonces      io.Reader
	port        io.ReadWriter
	timeout     time.Duration
	pairingRead bool // PAIRING answered
	failed      bool
	session     uint32
	sequence    uint32 // the next session request's
	lastReply   []byte // the last reply that matched its request
}

// exchange sends a request through send and returns its reply's payload.
// A failure or a refusal makes the client unusable.
func (c *Client) exchange(ctx context.Context, op Opcode, session, sequence uint32, payload []byte, timeout time.Duration) ([]byte, error) {
	if c.failed {
		return nil, errUnusable
	}
	reply, err := c.send(ctx, frame{opcode: op, session: session, sequence: sequence, payload: payload}, timeout)
	if err != nil {
		c.failed = true
		return nil, fmt.Errorf("updater %v: %w", op, err)
	}
	if reply.status < 0 {
		c.failed = true
		return nil, &DeviceError{Opcode: op, Status: reply.status}
	}
	return reply.payload, nil
}

// send writes the request, and once more if no reply starts in time, and
// returns the reply that matches it. It skips one stale reply to the
// previous request, which a resend of that request leaves behind when the
// first reply was only late: a reply whose header names the same request
// as the last reply that matched. The updater answers a resent session
// request from its cache, so that copy is the same, but runs a request that
// needs no session again, so its payload can differ.
//
// It waits for a session request's reply whatever the context does. The
// wait for the reply to a request that needs no session stops when the
// context is done.
func (c *Client) send(ctx context.Context, request frame, timeout time.Duration) (frame, error) {
	wire := encode(request)
	readCtx := context.WithoutCancel(ctx)
	if request.session == 0 {
		readCtx = ctx
	}
	skipped := false
attempts:
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return frame{}, err
		}
		deadline := time.Now().Add(timeout)
		// The port's own write timeout bounds the write: usbserial.Conn
		// fails with ErrStalled.
		if _, err := c.port.Write(wire); err != nil {
			return frame{}, err
		}
		for {
			raw, reply, err := c.read(readCtx, deadline)
			switch {
			case errors.Is(err, errNoReply) && attempt == 1:
				continue attempts
			case errors.Is(err, errNoReply):
				return frame{}, fmt.Errorf("%w within %v of the request or of its resend", errNoReply, timeout)
			case err != nil:
				return frame{}, err
			}
			matches := reply.opcode == request.opcode|replyBit &&
				reply.session == request.session && reply.sequence == request.sequence
			if !matches && !skipped && c.stale(raw) {
				skipped = true
				continue
			}
			if !matches {
				return frame{}, fmt.Errorf("the reply %v, session %#x, sequence %d does not match the request",
					reply.opcode&^replyBit, reply.session, reply.sequence)
			}
			if reply.status > 0 || reply.status < 0 && len(reply.payload) > 0 {
				return frame{}, fmt.Errorf("the reply has status %d with %d bytes of payload", reply.status, len(reply.payload))
			}
			c.lastReply = raw
			return reply, nil
		}
	}
}

// stale reports whether raw's header names the same request as the last
// reply that matched.
func (c *Client) stale(raw []byte) bool {
	return len(c.lastReply) >= staleSize && bytes.Equal(raw[:staleSize], c.lastReply[:staleSize])
}

// read reads one frame, which must start before deadline and arrive whole
// by then. It reads the header first, then as much more as the header
// gives. It stops as soon as the bytes that have arrived cannot start a
// frame, or ctx is done.
func (c *Client) read(ctx context.Context, deadline time.Time) ([]byte, frame, error) {
	wire := make([]byte, maxFrame)
	got, want := 0, headerSize
	for got < want {
		if err := ctx.Err(); err != nil {
			return nil, frame{}, err
		}
		if !time.Now().Before(deadline) {
			if got > 0 {
				return nil, frame{}, errPartialReply
			}
			return nil, frame{}, errNoReply
		}
		n, err := c.port.Read(wire[got:want])
		if n < 0 || n > want-got {
			return nil, frame{}, fmt.Errorf("the port returned %d bytes for a read of %d", n, want-got)
		}
		got += n
		if start := wire[:min(got, len(frameStart))]; !bytes.Equal(start, frameStart[:len(start)]) {
			return nil, frame{}, fmt.Errorf("%w: %q", errNotFrame, wire[:got])
		}
		if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, frame{}, err
		}
		if got == headerSize && want == headerSize {
			length, err := payloadLength(wire)
			if err != nil {
				return nil, frame{}, err
			}
			want += length + crcSize
		}
	}
	wire = wire[:got]
	f, err := decode(wire)
	return wire, f, err
}

// hello sends HELLO with its own reply timeout. The reply must be the ZMK
// half updater's, with the page size, payload limit, writable range and
// idle limit this client is written for.
func (c *Client) hello(ctx context.Context, timeout time.Duration) error {
	reply, err := c.exchange(ctx, OpHello, 0, 0, nil, timeout)
	if err != nil {
		return err
	}
	if err := checkHello(reply); err != nil {
		c.failed = true
		return err
	}
	return nil
}

func checkHello(reply []byte) error {
	if len(reply) != helloSize {
		return fmt.Errorf("updater HELLO reply is %d bytes, not %d", len(reply), helloSize)
	}
	le := binary.LittleEndian
	page, most := le.Uint16(reply[8:]), le.Uint16(reply[10:])
	start, end := le.Uint32(reply[12:]), le.Uint32(reply[16:])
	idle := time.Duration(le.Uint32(reply[20:])) * time.Millisecond
	if string(reply[:8]) != signature || page != firmware.HalfPageSize || most != maxPayload ||
		start != firmware.HalfPackageStart || end != firmware.HalfStorageStart || idle != idleLimit {
		return fmt.Errorf("the port's HELLO reply is not a supported half updater's: %q, %d-byte pages, "+
			"payloads up to %d bytes, writes to %#x..%#x, idle limit %v", reply[:8], page, most, start, end, idle)
	}
	return nil
}

var (
	// ErrUnsupported reports Neuron firmware that does not answer a
	// request that needs no session: firmware older than the request
	// takes it for a session request without a session and refuses it
	// with ENOTCONN, and a build without what the request reads refuses it
	// with ENOTSUP. Either way nothing changed, and the client stays
	// usable. PAIRING refused so gives ErrNoPairing instead.
	ErrUnsupported = errors.New("this Neuron firmware does not answer the request")
	// ErrStudioUnlocked reports CLEAR KEYMAP CHANGES refused with EBUSY:
	// ZMK Studio is unlocked, or a half update's session is open or
	// closing.
	ErrStudioUnlocked = errors.New("ZMK Studio is unlocked, or a half update is running")
)

// query sends a request that needs no session, other than HELLO, and
// returns its reply's payload. It leaves the client usable after a refusal,
// as Client says. ENOTCONN and ENOTSUP give an error matching
// ErrUnsupported, or ErrNoPairing for PAIRING; EBUSY refusing CLEAR KEYMAP
// CHANGES gives one matching ErrStudioUnlocked; any other status gives a
// *DeviceError alone.
func (c *Client) query(ctx context.Context, op Opcode, timeout time.Duration) ([]byte, error) {
	reply, err := c.exchange(ctx, op, 0, 0, nil, timeout)
	refused, ok := errors.AsType[*DeviceError](err)
	if !ok {
		return reply, err
	}
	c.failed = false
	switch {
	case (refused.Status == -enotconn || refused.Status == -enotsup) && op == OpPairing:
		return nil, fmt.Errorf("%w: %w", ErrNoPairing, err)
	case refused.Status == -enotconn || refused.Status == -enotsup:
		return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
	case refused.Status == -ebusy && op == OpClearKeymap:
		return nil, fmt.Errorf("%w: %w", ErrStudioUnlocked, err)
	}
	return nil, err
}

// open starts a session with a random nonce. The updater pauses typing and
// resets both halves into their factory bootloaders. A refusal matches
// ErrHalvesBusy or ErrHalvesSilent.
func (c *Client) open(ctx context.Context) error {
	nonce, err := c.nonce()
	if err != nil {
		return err
	}
	// A session is never 0, which requests outside a session use.
	c.session = max(nonce, 1)
	c.sequence = 1
	reply, err := c.exchange(ctx, OpOpen, c.session, c.sequence, nil, c.timeout)
	if err == nil {
		err = c.expect(OpOpen, reply, nil)
	}
	if err != nil {
		return err
	}
	c.sequence++
	return nil
}

func (c *Client) nonce() (uint32, error) {
	source := c.nonces
	if source == nil {
		source = rand.Reader
	}
	var b [4]byte
	if _, err := io.ReadFull(source, b[:]); err != nil {
		return 0, fmt.Errorf("reading an updater session nonce: %w", err)
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

// request sends a session request with the next sequence number.
func (c *Client) request(ctx context.Context, op Opcode, payload []byte) ([]byte, error) {
	return c.requestWithin(ctx, op, payload, c.timeout)
}

// requestWithin is request with its own reply timeout.
func (c *Client) requestWithin(ctx context.Context, op Opcode, payload []byte, timeout time.Duration) ([]byte, error) {
	reply, err := c.exchange(ctx, op, c.session, c.sequence, payload, timeout)
	if err != nil {
		return nil, err
	}
	c.sequence++
	return reply, nil
}

// expect fails, and makes the client unusable, unless reply is want.
func (c *Client) expect(op Opcode, reply, want []byte) error {
	if !bytes.Equal(reply, want) {
		c.failed = true
		return fmt.Errorf("unexpected updater %v reply %x, want %x", op, reply, want)
	}
	return nil
}

// probe reads a half's factory bootloader information, and the seal of the
// application it holds.
func (c *Client) probe(ctx context.Context, side firmware.Side) (HalfInfo, error) {
	id, err := sideID(side)
	if err != nil {
		return HalfInfo{}, err
	}
	reply, err := c.request(ctx, OpProbe, []byte{id})
	if err != nil {
		return HalfInfo{}, err
	}
	info, err := parseHalfInfo(side, id, reply)
	if err != nil {
		c.failed = true
	}
	return info, err
}

// start begins writing image to a half. The updater checks the transfer's
// length; the pages that follow must start with image's seal and metadata.
func (c *Client) start(ctx context.Context, side firmware.Side, image *firmware.HalfImage) error {
	id, err := sideID(side)
	if err != nil {
		return err
	}
	transfer := image.TransferData()
	payload := []byte{id, 0, 0, 0}
	payload = binary.LittleEndian.AppendUint32(payload, uint32(len(transfer)))
	payload = binary.LittleEndian.AppendUint32(payload, crc32.ChecksumIEEE(transfer))
	payload = binary.LittleEndian.AppendUint32(payload, image.Version)
	payload = binary.LittleEndian.AppendUint32(payload, image.ProgramCRC)
	reply, err := c.request(ctx, OpStart, payload)
	if err != nil {
		return err
	}
	return c.expect(OpStart, reply, nil)
}

// page writes one page at address, an offset in the half's flash. The half
// acknowledges it with the next page's address.
func (c *Client) page(ctx context.Context, address uint32, data []byte) error {
	if err := firmware.CheckHalfPage(firmware.Page{Addr: address, Data: data}); err != nil {
		return err
	}
	payload := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(data)), address)
	payload = append(payload, data...)
	reply, err := c.request(ctx, OpPage, payload)
	if err != nil {
		return err
	}
	return c.expect(OpPage, reply, binary.LittleEndian.AppendUint32(nil, address+firmware.HalfPageSize))
}

// finish ends image's transfer. The updater checks the CRC-32 of the pages
// sent, and the half's bootloader validates the program; the reply must
// give image's version and program CRC-32.
func (c *Client) finish(ctx context.Context, image *firmware.HalfImage) error {
	reply, err := c.request(ctx, OpFinish, nil)
	if err != nil {
		return err
	}
	want := binary.LittleEndian.AppendUint32(nil, image.Version)
	want = binary.LittleEndian.AppendUint32(want, image.ProgramCRC)
	return c.expect(OpFinish, reply, want)
}

// endSession sends CLOSE, which ends the session: the updater releases both
// halves and typing resumes. The port stays open.
func (c *Client) endSession(ctx context.Context) error {
	reply, err := c.request(ctx, OpClose, nil)
	if err != nil {
		return err
	}
	return c.expect(OpClose, reply, nil)
}
