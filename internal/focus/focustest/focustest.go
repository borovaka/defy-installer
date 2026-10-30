// SPDX-License-Identifier: MIT

// Package focustest is a model of Dygma's 2.3.0 Neuron firmware for tests.
// It answers the Focus commands that enter the Neuron's bootloader and
// write both halves through their factory bootloaders, keeps a model of
// each half's flash, and records what it was sent.
//
// It does not import the focus package, so that package's own tests can
// use it.
package focustest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
)

// flashSize is the half flash the factory bootloader writes.
const flashSize = firmware.HalfStorageStart - firmware.HalfPackageStart

// help is the model's reply to help. It lists the commands the model
// answers, apart from upgrade.isReady, which Dygma's 2.3.0 answers without
// listing it.
var help = []string{"help", "upgrade.end", "upgrade.neuron", "hardware.version", "version",
	"upgrade.start", "upgrade.keyscanner.isConnected", "upgrade.keyscanner.begin", "upgrade.keyscanner.getInfo",
	"upgrade.keyscanner.sendWrite", "upgrade.keyscanner.validate", "upgrade.keyscanner.finish"}

const sendWrite = "upgrade.keyscanner.sendWrite "

// Neuron is Dygma's 2.3.0 Neuron firmware over Focus, with both halves'
// factory bootloaders behind it. The halves go by Focus's numbers: 0 is
// the right, 1 the left. Reads behave as usbserial.Conn's do, returning a
// few bytes at a time. A command it does not know, or a write that breaks
// the factory bootloader's rules, fails the test.
type Neuron struct {
	t         testing.TB
	fragments []int // the most each Read returns, in turn
	reads     int

	// BufferSize is how many bytes the factory bootloader buffers before
	// it writes them.
	BufferSize int
	// Name and Version are what hardware.version and version answer.
	Name, Version string
	// Hardware, Starts and Connected are each half's hardware version and
	// flash start, as getInfo reports them, and whether it is cabled.
	Hardware, Starts [2]int
	Connected        [2]bool
	// ReadyAfter is how many upgrade.isReady queries answer false, as
	// while the updater waits for the Esc key.
	ReadyAfter int
	// RejectPacket and DropPacket are the packet, counted from 1, that is
	// refused or left unanswered.
	RejectPacket, DropPacket int
	// CorruptMetadata makes getInfo report a wrong CRC once a packet has
	// gone, and InvalidProgram makes validate fail.
	CorruptMetadata, InvalidProgram bool
	// SilentOn is a command that gets no reply.
	SilentOn string
	// ChangeAfterPreflight changes the program version in the seal of the
	// half begun a third time, after both were first read.
	ChangeAfterPreflight bool
	// Misreport makes getInfo right after validate report the version
	// plus one.
	Misreport bool
	// OnReboot, if set, runs once upgrade.neuron is accepted, as the
	// Neuron resets into its bootloader.
	OnReboot func()

	// Commands are the commands taken, with sendWrite's packets as the
	// bare command, and Packets the packets. Memory is each half's flash
	// from firmware.HalfPackageStart, and Finished the halves finished,
	// in turn.
	Commands []string
	Packets  []Packet
	Memory   [2][]byte
	Finished []int
	// ReadyQueries counts upgrade.isReady, and Rebooted is set once
	// upgrade.neuron is accepted.
	ReadyQueries int
	Rebooted     bool
	// Output is the replies not read yet.
	Output []byte

	input, pending []byte
	pendingAddr    uint32
	active, begins int
	last           string
}

// Packet is a page sendWrite took, for the half active then.
type Packet struct {
	Side    int
	Address uint32
	Data    []byte
}

// New returns a Defy-wireless Neuron on v2.3.0 with both halves cabled,
// at hardware version 1 with their flash erased.
func New(t testing.TB) *Neuron {
	f := &Neuron{t: t, BufferSize: 256, fragments: []int{1, 7, 2, 13, 31}, Name: "Defy-wireless", Version: "v2.3.0",
		Hardware: [2]int{1, 1}, Starts: [2]int{firmware.HalfPackageStart, firmware.HalfPackageStart},
		Connected: [2]bool{true, true}}
	for i := range f.Memory {
		f.Memory[i] = bytes.Repeat([]byte{0xff}, flashSize)
	}
	return f
}

func (f *Neuron) Read(p []byte) (int, error) {
	if len(f.Output) == 0 {
		time.Sleep(50 * time.Millisecond)
		return 0, fmt.Errorf("fake: %w", os.ErrDeadlineExceeded)
	}
	limit := f.fragments[f.reads%len(f.fragments)]
	f.reads++
	n := copy(p[:min(len(p), limit)], f.Output)
	f.Output = f.Output[n:]
	return n, nil
}

func (f *Neuron) Write(p []byte) (int, error) {
	f.input = append(f.input, p...)
	for len(f.input) > 0 {
		if bytes.HasPrefix(f.input, []byte(sendWrite)) {
			total := len(sendWrite) + 8 + firmware.HalfPageSize + 4
			if len(f.input) < total {
				break
			}
			f.packet(f.input[len(sendWrite):total])
			f.input = f.input[total:]
			continue
		}
		i := bytes.IndexByte(f.input, '\n')
		if i < 0 {
			break
		}
		command := string(f.input[:i])
		f.input = f.input[i+1:]
		f.Commands = append(f.Commands, command)
		if command != f.SilentOn {
			f.handle(command)
		}
		f.last = command
	}
	return len(p), nil
}

func (f *Neuron) SetDTR(bool) error     { return nil }
func (f *Neuron) SetBaudRate(int) error { return nil }
func (f *Neuron) Close() error          { return nil }

// ResetInput drops the replies not read yet.
func (f *Neuron) ResetInput() error {
	f.Output = nil
	return nil
}

func (f *Neuron) packet(p []byte) {
	le := binary.LittleEndian
	address, length := le.Uint32(p), le.Uint32(p[4:])
	data := p[8 : 8+firmware.HalfPageSize]
	if length != firmware.HalfPageSize || le.Uint32(p[8+firmware.HalfPageSize:]) != crc32.ChecksumIEEE(data) {
		f.t.Errorf("fake Neuron: a packet with length %d and a wrong CRC", length)
	}
	f.Commands = append(f.Commands, strings.TrimSpace(sendWrite))
	f.Packets = append(f.Packets, Packet{f.active, address, bytes.Clone(data)})
	switch len(f.Packets) {
	case f.RejectPacket:
		f.reply("false")
	case f.DropPacket:
		f.buffer(address, data)
	default:
		f.buffer(address, data)
		f.reply("true")
	}
}

func (f *Neuron) reply(s string) {
	f.Output = append(f.Output, s+" \r\n.\r\n"...)
}

func (f *Neuron) buffer(address uint32, data []byte) {
	if len(f.pending) > 0 && address != f.pendingAddr+uint32(len(f.pending)) {
		f.flush()
	}
	if len(f.pending) == 0 {
		f.pendingAddr = address
	}
	f.pending = append(f.pending, data...)
	if len(f.pending) == f.BufferSize {
		f.flush()
	}
}

// flush writes the buffered pages, erasing each 4 KiB sector before its
// first page as the factory bootloader does.
func (f *Neuron) flush() {
	memory := f.Memory[f.active]
	for off := 0; off < len(f.pending); off += firmware.HalfPageSize {
		rel := int(f.pendingAddr) + off - firmware.HalfPackageStart
		if rel%firmware.HalfSectorSize == 0 {
			copy(memory[rel:rel+firmware.HalfSectorSize], bytes.Repeat([]byte{0xff}, firmware.HalfSectorSize))
		}
		for i, b := range f.pending[off : off+firmware.HalfPageSize] {
			if memory[rel+i]&b != b {
				f.t.Errorf("fake Neuron: a write to 0x%x needs an erase", rel+i+firmware.HalfPackageStart)
			}
			memory[rel+i] = b
		}
	}
	f.pending = nil
}

func (f *Neuron) handle(command string) {
	name, arg, _ := strings.Cut(command, " ")
	id, _ := strconv.Atoi(arg)
	switch name {
	case "help":
		f.reply(strings.Join(help, "\n"))
	case "upgrade.neuron":
		if f.ReadyQueries <= f.ReadyAfter {
			f.t.Error("fake Neuron: upgrade.neuron before the updater was ready")
		}
		f.Rebooted = true
		if f.OnReboot != nil {
			f.OnReboot()
		}
	case "hardware.version":
		f.reply(f.Name)
	case "version":
		f.reply(f.Version)
	case "upgrade.start":
		f.reply("")
	case "upgrade.isReady":
		f.ReadyQueries++
		f.reply(strconv.FormatBool(f.ReadyQueries > f.ReadyAfter))
	case "upgrade.keyscanner.isConnected":
		f.reply(strconv.FormatBool(f.Connected[id]))
	case "upgrade.keyscanner.begin":
		f.active = id
		f.pending = nil
		if f.begins++; f.ChangeAfterPreflight && f.begins == 3 {
			binary.LittleEndian.PutUint32(f.Memory[id][24:], 7)
		}
		f.reply("true")
	case "upgrade.keyscanner.getInfo":
		memory := f.Memory[f.active]
		crc := binary.LittleEndian.Uint32(memory[20:])
		if f.CorruptMetadata && len(f.Packets) > 0 {
			crc ^= 1
		}
		version := binary.LittleEndian.Uint32(memory[24:])
		if f.Misreport && f.last == "upgrade.keyscanner.validate" {
			version++
		}
		f.reply(fmt.Sprintf("%d %d %d %d true", f.Hardware[f.active], f.Starts[f.active], version, crc))
	case "upgrade.keyscanner.validate":
		memory := f.Memory[f.active]
		length := int(binary.LittleEndian.Uint32(memory[16:])) + 0x1000
		length += -length & (firmware.HalfPageSize - 1)
		_, err := firmware.ParseHalfImage("written", memory[:min(length, len(memory))])
		f.reply(strconv.FormatBool(err == nil && len(f.pending) == 0 && !f.InvalidProgram))
	case "upgrade.keyscanner.finish":
		if len(f.pending) > 0 {
			f.t.Error("fake Neuron: finish with writes still buffered")
		}
		f.Finished = append(f.Finished, f.active)
		f.reply("true")
	default:
		f.t.Errorf("fake Neuron: unexpected command %q", command)
	}
}
