// SPDX-License-Identifier: MIT

// Package updatertest is a fake of the ZMK Neuron's half updater port for
// tests. It answers the requests that zmk-defy-wireless's
// docs/half-updater.md describes as the Neuron's firmware does, with a
// model of both halves, and records what it was sent.
//
// It does not import the updater package, so that package's own tests can
// use it. Op is the client's opcode type, updater.Opcode.
package updatertest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
)

// The requests' opcodes.
const (
	opHello        = 1
	opOpen         = 2
	opProbe        = 3
	opStart        = 4
	opPage         = 5
	opFinish       = 6
	opClose        = 7
	opSplitStatus  = 8
	opBattery      = 9
	opPairing      = 12
	opResetPairing = 13
	opVersion      = 14
	opHalfVersions = 15
	opClearKeymap  = 16
)

// sessionless are the requests that need no session. The updater never
// answers one of them from its cache.
var sessionless = []byte{opHello, opPairing, opSplitStatus, opBattery, opVersion, opHalfVersions, opClearKeymap}

// The Neuron's errno values, from picolibc.
const (
	eio      = 5
	ebusy    = 16
	einval   = 22
	eproto   = 71
	ebadmsg  = 77
	enotconn = 128
	enotsup  = 134
)

// The frame layout and the updater's limits.
const (
	headerSize   = 16
	crcSize      = 4
	maxPayload   = 260
	pageSize     = 256
	packageStart = 0x5000
	storageStart = 0x1c0000
	idleLimitMS  = 10000
	// uptime is the Neuron's uptime in the snapshots, in milliseconds,
	// and age a record's age.
	uptime = 60000
	age    = 1000
)

// Link is how a half last reported to the Neuron.
type Link byte

const (
	NoLink Link = iota
	Cable
	Bluetooth
)

// Half is a half as the Neuron sees it.
type Half struct {
	// CRC and Version are the program CRC-32 and version in the half's
	// seal; a zero CRC is no valid seal. FINISH sets them to the image
	// written.
	CRC, Version uint32
	// Module is the module version the half reports.
	Module string
	// Link is how the half reports; NoLink reports nothing.
	Link Link
	// Asleep is how many more HALF VERSIONS requests find no record from
	// the half; -1 is all of them.
	Asleep int
	// ProbeFails is how many more PROBEs of the half fail with EIO, which
	// ends the session, as for a half that is not cabled or is switched
	// on.
	ProbeFails int
	// AfterWrite is how the half reports once a session that wrote it
	// ends, as it starts its new firmware.
	AfterWrite Link
}

// Answer is a reply's status and payload.
type Answer struct {
	Status  int16
	Payload []byte
}

// Request is a request the fake took off the wire.
type Request[Op ~uint8] struct {
	Op                Op
	Session, Sequence uint32
	Payload, Wire     []byte
}

// Page is a page the fake wrote to a half: 256 bytes, and where.
type Page struct {
	// Side is the updater's number for the half: 0 left, 1 right.
	Side    byte
	Address uint32
	Data    []byte
}

// transfer is what START set up.
type transfer struct {
	side                                  byte
	length, checksum, version, programCRC uint32
}

// Updater is the half updater's port. With no fields set it is firmware
// that answers only HELLO outside a session, and refuses the other requests
// there with ENOTCONN.
// Reads behave as usbserial.Conn's do. A request that breaks the protocol,
// such as one with a bad CRC, fails the test.
type Updater[Op ~uint8] struct {
	t testing.TB

	// Silent takes requests and never answers.
	Silent bool
	// ReadSize is the most one Read returns.
	ReadSize int
	// Pairing is PAIRING's payload. Nil is firmware older than PAIRING,
	// which has no RESET PAIRING either.
	Pairing []byte
	// Version is VERSION's payload. Nil is firmware older than VERSION,
	// which has no HALF VERSIONS or CLEAR KEYMAP CHANGES either.
	Version []byte
	// Clear holds CLEAR KEYMAP CHANGES's statuses, in turn; the last
	// stays. Without one, it is taken for a session request.
	Clear []int16
	// Connects is, for each split slot, how many SPLIT STATUS requests go
	// before the slot reports; -1 never. Nil is firmware without SPLIT
	// STATUS.
	Connects []int
	// Battery is BATTERY's payload. Nil is firmware without BATTERY.
	Battery []byte
	// Halves are the left and the right half.
	Halves [2]Half
	// Answers replace the replies to the requests that need no session
	// other than HELLO and PAIRING, by opcode.
	Answers map[Op]Answer
	// Reject answers an opcode with a status, once its session checks
	// pass.
	Reject map[Op]int16
	// Slow keeps the updater busy for this long after it runs an opcode:
	// its reply, and any reply to a request that came meanwhile, wait.
	Slow map[Op]time.Duration
	// Filter, if set, sees each request and its reply, and returns the
	// bytes that go back.
	Filter func(r Request[Op], reply []byte) []byte

	// Requests are the requests taken, and Executed the opcodes of those
	// the updater ran rather than answered from its cache.
	Requests []Request[Op]
	Executed []Op
	// Pages and Finished are what the halves were written: the pages, and
	// the sides FINISH validated. Memory is what each side holds of the
	// last transfer.
	Pages    []Page
	Finished []byte
	Memory   [2][]byte
	// Resets counts the RESET PAIRINGs run.
	Resets int
	// DTR holds each change of DTR, and Closed whether the port closed.
	DTR    []bool
	Closed bool
	// Opened is set while a session is open.
	Opened bool
	// Input is a request that has not all arrived, Output the replies not
	// read yet, and Held the replies that wait while the updater is busy.
	Input, Output, Held []byte

	session, sequence          uint32
	probed                     [2]bool
	current                    *transfer
	started, reset             bool // a START or a RESET PAIRING has run in the session
	wrote                      [2]bool
	splits                     int // SPLIT STATUS requests answered
	busyUntil                  time.Time
	cachedRequest, cachedReply []byte
}

// New returns an updater whose reads return at most 11 bytes.
func New[Op ~uint8](t testing.TB) *Updater[Op] {
	return &Updater[Op]{t: t, ReadSize: 11, Reject: map[Op]int16{}}
}

// Count counts the requests for op the updater ran.
func (f *Updater[Op]) Count(op Op) int {
	n := 0
	for _, o := range f.Executed {
		if o == op {
			n++
		}
	}
	return n
}

func (f *Updater[Op]) Read(p []byte) (int, error) {
	if len(f.Held) > 0 && !time.Now().Before(f.busyUntil) {
		f.Output = append(f.Output, f.Held...)
		f.Held = nil
	}
	if len(f.Output) == 0 {
		time.Sleep(50 * time.Millisecond)
		return 0, fmt.Errorf("fake: %w", os.ErrDeadlineExceeded)
	}
	n := copy(p[:min(len(p), f.ReadSize)], f.Output)
	f.Output = f.Output[n:]
	return n, nil
}

func (f *Updater[Op]) Write(p []byte) (int, error) {
	f.Input = append(f.Input, p...)
	if len(f.Input) >= headerSize {
		total := headerSize + int(binary.LittleEndian.Uint16(f.Input[12:])) + crcSize
		if len(f.Input) > total {
			f.t.Errorf("the client sent more than one frame at once: %x", f.Input)
		}
		if len(f.Input) >= total {
			wire := f.Input
			f.Input = nil
			f.handle(wire)
		}
	}
	return len(p), nil
}

// SetDTR records the change. Clearing DTR ends a session.
func (f *Updater[Op]) SetDTR(on bool) error {
	f.DTR = append(f.DTR, on)
	if !on {
		f.endSession()
	}
	return nil
}

func (f *Updater[Op]) SetBaudRate(int) error { return nil }

// ResetInput drops the replies not read yet.
func (f *Updater[Op]) ResetInput() error {
	f.Output = nil
	return nil
}

// Close closes the port, which ends a session.
func (f *Updater[Op]) Close() error {
	f.Closed = true
	f.endSession()
	return nil
}

// endSession ends the session, if one is open, and lets the halves go:
// those it wrote start their new firmware.
func (f *Updater[Op]) endSession() {
	if !f.Opened {
		return
	}
	f.Opened = false
	f.current = nil
	for i := range f.Halves {
		if h := &f.Halves[i]; f.wrote[i] {
			h.Link, h.Asleep = h.AfterWrite, 0
		}
	}
}

func (f *Updater[Op]) handle(wire []byte) {
	le := binary.LittleEndian
	body := wire[:len(wire)-crcSize]
	if string(wire[:2]) != "DU" || wire[2] != 1 || le.Uint16(wire[14:]) != 0 || le.Uint16(wire[12:]) > maxPayload ||
		le.Uint32(wire[len(body):]) != crc32.ChecksumIEEE(body) {
		f.t.Errorf("the client sent an invalid request %x", wire)
		return
	}
	r := Request[Op]{Op(wire[3]), le.Uint32(wire[4:]), le.Uint32(wire[8:]), wire[headerSize:len(body)], wire}
	f.Requests = append(f.Requests, r)
	if f.Silent {
		return
	}
	var reply []byte
	cached := !slices.Contains(sessionless, byte(r.Op))
	if cached && bytes.Equal(wire, f.cachedRequest) {
		reply = f.cachedReply
	} else {
		f.Executed = append(f.Executed, r.Op)
		status, body := f.dispatch(r)
		reply = Response(r.Op, r.Session, r.Sequence, body, status)
		if d := f.Slow[r.Op]; d > 0 {
			f.busyUntil = time.Now().Add(d)
		}
		if cached {
			f.cachedRequest, f.cachedReply = wire, reply
		}
	}
	if f.Filter != nil {
		reply = f.Filter(r, reply)
	}
	if time.Now().Before(f.busyUntil) {
		f.Held = append(f.Held, reply...)
		return
	}
	f.Output = append(f.Output, reply...)
}

func (f *Updater[Op]) dispatch(r Request[Op]) (int16, []byte) {
	le := binary.LittleEndian
	outside := r.Session != 0 || r.Sequence != 0 || len(r.Payload) > 0
	switch r.Op {
	case opHello:
		if outside {
			f.t.Errorf("HELLO with session %#x, sequence %d and %d bytes changes the updater's state",
				r.Session, r.Sequence, len(r.Payload))
		}
		return 0, Capabilities("DEFYUPD1", pageSize, maxPayload, packageStart, storageStart, idleLimitMS)
	case opPairing:
		if f.Pairing == nil {
			break
		}
		if outside {
			return -einval, nil
		}
		return 0, f.Pairing
	case opSplitStatus, opBattery, opVersion, opHalfVersions, opClearKeymap:
		a, ok := f.Answers[r.Op]
		if !ok {
			a, ok = f.diagnostic(r.Op)
		}
		if !ok {
			break
		}
		if outside {
			return -einval, nil
		}
		return a.Status, a.Payload
	case opOpen:
		if f.Opened {
			return -ebusy, nil
		}
		if r.Session == 0 || r.Sequence != 1 || len(r.Payload) > 0 {
			return -einval, nil
		}
		if status, ok := f.Reject[r.Op]; ok {
			return status, nil
		}
		f.session, f.sequence, f.Opened = r.Session, r.Sequence, true
		f.probed, f.wrote = [2]bool{}, [2]bool{}
		f.started, f.reset = false, false
		return 0, nil
	}
	if !f.Opened || r.Session != f.session {
		return -enotconn, nil
	}
	if r.Sequence != f.sequence+1 {
		return -eproto, nil
	}
	f.sequence = r.Sequence
	if status, ok := f.Reject[r.Op]; ok {
		return status, nil
	}
	switch r.Op {
	case opProbe:
		if len(r.Payload) != 1 || r.Payload[0] > 1 {
			return -einval, nil
		}
		side := r.Payload[0]
		h := &f.Halves[side]
		if h.ProbeFails > 0 {
			h.ProbeFails--
			f.endSession()
			return -eio, nil
		}
		f.probed[side] = true
		valid := byte(0)
		if h.CRC != 0 {
			valid = 1
		}
		return 0, HalfInfo(side, valid, 0, 1, packageStart, h.Version, h.CRC)
	case opResetPairing:
		if f.Pairing == nil {
			break
		}
		if len(r.Payload) > 0 || f.probed != [2]bool{true, true} || f.started || f.reset {
			return -einval, nil
		}
		// The Neuron's side is cleared, then both halves' settings.
		f.reset = true
		f.Resets++
		f.Pairing = []byte{1, 2, 0, 0}
		return 0, nil
	case opStart:
		if len(r.Payload) != 20 || f.probed != [2]bool{true, true} || f.current != nil {
			return -einval, nil
		}
		t := transfer{r.Payload[0], le.Uint32(r.Payload[4:]), le.Uint32(r.Payload[8:]),
			le.Uint32(r.Payload[12:]), le.Uint32(r.Payload[16:])}
		if t.side > 1 || !bytes.Equal(r.Payload[1:4], []byte{0, 0, 0}) || t.length%4096 != 0 ||
			t.length < 4096 || t.length > storageStart-packageStart {
			return -einval, nil
		}
		f.current = &t
		f.started = true
		f.Memory[t.side] = nil
		return 0, nil
	case opPage:
		if f.current == nil || len(r.Payload) != 4+pageSize {
			return -einval, nil
		}
		side := f.current.side
		address := le.Uint32(r.Payload)
		if address != packageStart+uint32(len(f.Memory[side])) || len(f.Memory[side])+pageSize > int(f.current.length) {
			return -einval, nil
		}
		f.Pages = append(f.Pages, Page{side, address, r.Payload[4:]})
		f.Memory[side] = append(f.Memory[side], r.Payload[4:]...)
		return 0, le.AppendUint32(nil, address+pageSize)
	case opFinish:
		if f.current == nil || len(r.Payload) > 0 {
			return -einval, nil
		}
		t := f.current
		memory := f.Memory[t.side]
		if len(memory) != int(t.length) || crc32.ChecksumIEEE(memory) != t.checksum {
			return -ebadmsg, nil
		}
		size := int(le.Uint32(memory[12:])-packageStart+le.Uint32(memory[16:])+pageSize-1) / pageSize * pageSize
		if size > len(memory) {
			return -ebadmsg, nil
		}
		image, err := firmware.ParseHalfImage("written", memory[:size])
		if err != nil || slices.ContainsFunc(memory[size:], func(b byte) bool { return b != 0xff }) ||
			image.Version != t.version || image.ProgramCRC != t.programCRC {
			return -ebadmsg, nil
		}
		f.Finished = append(f.Finished, t.side)
		f.wrote[t.side] = true
		f.Halves[t.side].CRC, f.Halves[t.side].Version = t.programCRC, t.version
		f.current = nil
		return 0, le.AppendUint32(le.AppendUint32(nil, t.version), t.programCRC)
	case opClose:
		if len(r.Payload) > 0 {
			return -einval, nil
		}
		f.endSession()
		return 0, nil
	}
	return -enotsup, nil
}

// diagnostic answers a request that needs no session from the model, and
// reports false for one the firmware lacks.
func (f *Updater[Op]) diagnostic(op Op) (Answer, bool) {
	switch {
	case op == opVersion && f.Version != nil:
		return Answer{Payload: f.Version}, true
	case op == opHalfVersions && f.Version != nil:
		return Answer{Payload: f.halfVersions()}, true
	case op == opClearKeymap && f.Version != nil && len(f.Clear) > 0:
		status := f.Clear[0]
		if len(f.Clear) > 1 {
			f.Clear = f.Clear[1:]
		}
		return Answer{Status: status}, true
	case op == opSplitStatus && f.Connects != nil:
		f.splits++
		var slots [][]byte
		for i, after := range f.Connects {
			phase := byte(0)
			if after >= 0 && f.splits >= after {
				phase = 7
			}
			slots = append(slots, SplitSlot(byte(i), phase, 0))
		}
		return Answer{Payload: Snapshot(48, uptime, slots...)}, true
	case op == opBattery && f.Battery != nil:
		return Answer{Payload: f.Battery}, true
	}
	return Answer{}, false
}

// halfVersions is HALF VERSIONS's payload. A half that is asleep, or held
// by a session, has no record.
func (f *Updater[Op]) halfVersions() []byte {
	var records [][]byte
	for i := range f.Halves {
		h := &f.Halves[i]
		link := h.Link
		switch {
		case h.Asleep > 0:
			h.Asleep--
			link = NoLink
		case h.Asleep < 0, f.Opened:
			link = NoLink
		}
		if link == NoLink {
			records = append(records, HalfRecord(byte(i), 0, 0xffffffff, 0, 0, ""))
		} else {
			records = append(records, HalfRecord(byte(i), byte(link), age, h.CRC, h.Version, h.Module))
		}
	}
	return Snapshot(36, uptime, records...)
}

// Response is the updater's reply to a request, as the firmware builds it.
func Response[Op ~uint8](op Op, session, sequence uint32, payload []byte, status int16) []byte {
	le := binary.LittleEndian
	wire := []byte{'D', 'U', 1, byte(op) | 0x80}
	wire = le.AppendUint32(wire, session)
	wire = le.AppendUint32(wire, sequence)
	wire = le.AppendUint16(wire, uint16(len(payload)))
	wire = le.AppendUint16(wire, uint16(status))
	wire = append(wire, payload...)
	return le.AppendUint32(wire, crc32.ChecksumIEEE(wire))
}

// Reply is Response to r with status 0.
func Reply[Op ~uint8](r Request[Op], payload []byte) []byte {
	return Response(r.Op, r.Session, r.Sequence, payload, 0)
}

// Capabilities is HELLO's payload.
func Capabilities(name string, page, most uint16, start, end, idle uint32) []byte {
	le := binary.LittleEndian
	b := append([]byte(name), make([]byte, 8-len(name))...)
	b = le.AppendUint16(le.AppendUint16(b, page), most)
	return le.AppendUint32(le.AppendUint32(le.AppendUint32(b, start), end), idle)
}

// HalfInfo is PROBE's payload.
func HalfInfo(side, valid byte, reserved uint16, hardware, start, version, crc uint32) []byte {
	le := binary.LittleEndian
	b := le.AppendUint16([]byte{side, valid}, reserved)
	for _, v := range []uint32{hardware, start, version, crc} {
		b = le.AppendUint32(b, v)
	}
	return b
}

// VersionPayload is VERSION's payload for the strings given.
func VersionPayload(module, source, commit string) []byte {
	b := []byte{1, 0, 0, 0}
	for _, s := range []string{module, source, commit} {
		b = append(append(b, byte(len(s))), s...)
	}
	return b
}

// Snapshot is a reply with the header that HALF VERSIONS, SPLIT STATUS and
// BATTERY share, then the records.
func Snapshot(size uint16, uptime uint32, records ...[]byte) []byte {
	le := binary.LittleEndian
	b := le.AppendUint32(le.AppendUint16([]byte{1, 2}, size), uptime)
	for _, r := range records {
		b = append(b, r...)
	}
	return b
}

// HalfRecord is one of HALF VERSIONS's records: side, link and age, then
// the half's report, which is zero without a link.
func HalfRecord(side, link byte, age uint32, crc, seal uint32, module string) []byte {
	le := binary.LittleEndian
	r := le.AppendUint32([]byte{side, link, 0, 0}, age)
	report := make([]byte, 28)
	if link != 0 {
		report[0], report[1] = 1, side
		le.PutUint32(report[2:], crc)
		le.PutUint32(report[6:], seal)
		copy(report[10:], module)
	}
	return append(r, report...)
}

// SplitSlot is one of SPLIT STATUS's records: the slot, its phase and its
// count of key reports.
func SplitSlot(slot, phase byte, reports uint32) []byte {
	r := make([]byte, 48)
	r[0], r[1] = slot, phase
	binary.LittleEndian.PutUint32(r[28:], reports)
	return r
}
