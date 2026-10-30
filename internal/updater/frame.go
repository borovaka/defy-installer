// SPDX-License-Identifier: MIT

package updater

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// Opcode is a request's opcode. A reply carries the request's opcode with
// replyBit set.
type Opcode uint8

// The requests the half updater takes, from docs/half-updater.md.
const (
	OpHello Opcode = iota + 1
	OpOpen
	OpProbe
	OpStart
	OpPage
	OpFinish
	OpClose
)

// Read-only diagnostics, which need no session: the Bluetooth split status
// and the halves' battery records. Opcodes 10 and 11, cable and dock
// detection and the key trace, are not sent.
const (
	OpSplitStatus Opcode = 8
	OpBattery     Opcode = 9
)

// The split pairing's requests. PAIRING, like HELLO, needs no session;
// RESET PAIRING goes within one.
const (
	OpPairing      Opcode = 12
	OpResetPairing Opcode = 13
)

// What the Neuron and the halves run, and Studio's keymap changes. These
// need no session either.
const (
	OpVersion      Opcode = 14
	OpHalfVersions Opcode = 15
	OpClearKeymap  Opcode = 16
)

// opcodeNames are the names messages give the opcodes; any other opcode
// goes by its number.
var opcodeNames = [...]string{
	OpHello:        "HELLO",
	OpOpen:         "OPEN",
	OpProbe:        "PROBE",
	OpStart:        "START",
	OpPage:         "PAGE",
	OpFinish:       "FINISH",
	OpClose:        "CLOSE",
	OpSplitStatus:  "SPLIT STATUS",
	OpBattery:      "BATTERY",
	OpPairing:      "PAIRING",
	OpResetPairing: "RESET PAIRING",
	OpVersion:      "VERSION",
	OpHalfVersions: "HALF VERSIONS",
	OpClearKeymap:  "CLEAR KEYMAP CHANGES",
}

func (o Opcode) String() string {
	if int(o) < len(opcodeNames) && opcodeNames[o] != "" {
		return opcodeNames[o]
	}
	return fmt.Sprintf("opcode %d", uint8(o))
}

// A refusal's status is a negative errno from picolibc, the Neuron's C
// library, whose numbers differ from Linux's.
const (
	ebusy    = 16
	enotconn = 128
	enotsup  = 134
)

// The frame layout: a 16-byte header, up to 260 bytes of payload, then a
// CRC-32 of the header and payload.
const (
	headerSize      = 16
	crcSize         = 4
	maxPayload      = 260
	maxFrame        = headerSize + maxPayload + crcSize
	protocolVersion = 1
	replyBit        = 0x80
)

var magic = [2]byte{'D', 'U'}

// frame is a request or a reply, parsed. A request carries status 0; a
// failed reply carries a negative errno.
type frame struct {
	opcode   Opcode
	session  uint32
	sequence uint32
	status   int16
	payload  []byte
}

// encode returns f as it goes on the wire. Every request the client builds
// fits in a frame, so a longer payload is a bug, and encode panics on it.
func encode(f frame) []byte {
	if len(f.payload) > maxPayload {
		panic(fmt.Sprintf("an updater payload of %d bytes is longer than %d", len(f.payload), maxPayload))
	}
	wire := make([]byte, 0, headerSize+len(f.payload)+crcSize)
	wire = append(wire, magic[0], magic[1], protocolVersion, byte(f.opcode))
	wire = binary.LittleEndian.AppendUint32(wire, f.session)
	wire = binary.LittleEndian.AppendUint32(wire, f.sequence)
	wire = binary.LittleEndian.AppendUint16(wire, uint16(len(f.payload)))
	wire = binary.LittleEndian.AppendUint16(wire, uint16(f.status))
	wire = append(wire, f.payload...)
	return binary.LittleEndian.AppendUint32(wire, crc32.ChecksumIEEE(wire))
}

// payloadLength checks a frame's header and returns its payload length.
func payloadLength(header []byte) (int, error) {
	if header[0] != magic[0] || header[1] != magic[1] || header[2] != protocolVersion {
		return 0, errors.New("updater frame has the wrong magic or protocol version")
	}
	length := int(binary.LittleEndian.Uint16(header[12:]))
	if length > maxPayload {
		return 0, fmt.Errorf("updater frame header gives a payload of %d bytes, more than %d", length, maxPayload)
	}
	return length, nil
}

// decode checks and parses one whole frame. The payload shares wire's
// memory.
func decode(wire []byte) (frame, error) {
	if len(wire) < headerSize+crcSize {
		return frame{}, fmt.Errorf("updater frame of %d bytes is truncated", len(wire))
	}
	length, err := payloadLength(wire)
	if err != nil {
		return frame{}, err
	}
	if len(wire) != headerSize+length+crcSize {
		return frame{}, fmt.Errorf("updater frame is %d bytes, its header gives %d", len(wire), headerSize+length+crcSize)
	}
	body := wire[:len(wire)-crcSize]
	if binary.LittleEndian.Uint32(wire[len(body):]) != crc32.ChecksumIEEE(body) {
		return frame{}, errors.New("updater frame CRC mismatch")
	}
	return frame{
		opcode:   Opcode(wire[3]),
		session:  binary.LittleEndian.Uint32(wire[4:]),
		sequence: binary.LittleEndian.Uint32(wire[8:]),
		status:   int16(binary.LittleEndian.Uint16(wire[14:])),
		payload:  wire[headerSize:len(body):len(body)],
	}, nil
}

const (
	// snapshotHeader is the size of the header that HALF VERSIONS, SPLIT
	// STATUS and BATTERY replies start with: a version, the record count,
	// the record size and the uptime.
	snapshotHeader = 8
	// snapshotRecords is the record count of each: one record for each
	// half, or for SPLIT STATUS for each split connection slot.
	snapshotRecords = 2
)

// checkSnapshot checks a snapshot reply: its version, two records of size
// bytes, and its length.
func checkSnapshot(op Opcode, reply []byte, version byte, size int) error {
	want := snapshotHeader + snapshotRecords*size
	if len(reply) >= 4 && (reply[0] != version || int(reply[1]) != snapshotRecords ||
		int(binary.LittleEndian.Uint16(reply[2:])) != size) {
		return fmt.Errorf("updater %v reply gives version %d with %d records of %d bytes, not version %d with %d of %d",
			op, reply[0], reply[1], binary.LittleEndian.Uint16(reply[2:]), version, snapshotRecords, size)
	}
	if len(reply) != want {
		return fmt.Errorf("updater %v reply is %d bytes, not %d", op, len(reply), want)
	}
	return nil
}

// Link is how a half's report reached the Neuron: a HALF VERSIONS record's
// link, or a bit of a BATTERY record's links.
type Link uint8

const (
	// NoLink is a side without a record.
	NoLink    Link = 0
	Cable     Link = 1
	Bluetooth Link = 2
)

func (l Link) String() string {
	switch l {
	case NoLink:
		return "no link"
	case Cable:
		return "cable"
	case Bluetooth:
		return "Bluetooth"
	}
	return fmt.Sprintf("link %d", uint8(l))
}
