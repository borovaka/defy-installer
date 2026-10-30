// SPDX-License-Identifier: MIT

// Package neurontest is a model of Dygma's n2_bootloader 1.0.0 on the
// Neuron for tests. It plays the bootloader's serial protocol over the
// nRF52833's whole flash, refuses what the real one refuses, and records
// what it was sent. It also plays the serial port of the UF2 loader in
// front of ZMK, which takes only the request for the bootloader.
//
// It does not import the neuron package, so that package's own tests can
// use it.
package neurontest

import (
	"bytes"
	"hash/crc32"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Dygma's n2_bootloader 1.0.0 lies contiguous from bootloaderStart, with
// this CRC32.
const (
	bootloaderStart  = 0x77000
	bootloaderLength = 0x6F54
	bootloaderCRC32  = 0x080F9450
)

// The range the bootloader's E and W accept, and the flash's size.
const (
	driverStart, driverEnd = 0x1000, 0x77000
	flashSize              = 0x80000
)

// Bootloader plays n2_bootloader's serial protocol over a model of the
// nRF52833's whole flash. Like the real one, E and W refuse anything below
// 0x1000 or past 0x77000, E with only an address erases up to 0x77000,
// Dygma's settings included, and C reads any flash without bounds. It
// answers nothing once S# is acknowledged. Reads behave as
// usbserial.Conn's do. A command that breaks the protocol, such as a write
// to flash that is not erased, fails the test.
//
// Its flash holds a pattern where the bootloader's code would be, so the
// identification command is answered as Dygma's n2_bootloader 1.0.0 would
// answer it, unless Other is set.
type Bootloader struct {
	t testing.TB

	// Flash is the whole flash.
	Flash []byte
	// Other makes the bootloader not Dygma's.
	Other bool
	// FailOn answers ERR to the commands that start with it, and SilentOn
	// answers nothing to them.
	FailOn, SilentOn string
	// Handled, if set, sees each command once it is handled.
	Handled func(cmd string)
	// OnStart, if set, runs when the port closes after S#, as the Neuron
	// leaves the bootloader for its firmware.
	OnStart func()

	// Commands are the commands taken, Writes the addresses W wrote, and
	// Output the replies not read yet.
	Commands []string
	Writes   []uint32
	Output   []byte
	// Started is set once S# is acknowledged.
	Started bool

	input, buffer []byte
	receiving     int // the bytes of an upload still to come
}

// New returns a bootloader whose flash holds 0xa5 outside the bootloader's
// code.
func New(t testing.TB) *Bootloader {
	b := &Bootloader{t: t, Flash: bytes.Repeat([]byte{0xa5}, flashSize)}
	for i := range bootloaderLength {
		b.Flash[bootloaderStart+i] = byte(i * 7)
	}
	return b
}

// Read returns what the bootloader has sent. With nothing there it waits
// 50 ms and times out.
func (b *Bootloader) Read(p []byte) (int, error) {
	if len(b.Output) == 0 {
		time.Sleep(50 * time.Millisecond)
		return 0, os.ErrDeadlineExceeded
	}
	n := copy(p, b.Output)
	b.Output = b.Output[n:]
	return n, nil
}

func (b *Bootloader) Write(p []byte) (int, error) {
	for _, c := range p {
		if b.receiving > 0 {
			b.buffer = append(b.buffer, c)
			b.receiving--
			continue
		}
		b.input = append(b.input, c)
		if c == '#' {
			cmd := string(b.input)
			b.input = nil
			b.handle(cmd)
			if b.Handled != nil {
				b.Handled(cmd)
			}
		}
	}
	return len(p), nil
}

func (b *Bootloader) SetDTR(bool) error     { return nil }
func (b *Bootloader) SetBaudRate(int) error { return nil }

// ResetInput drops the replies not read yet.
func (b *Bootloader) ResetInput() error {
	b.Output = nil
	return nil
}

// Close closes the port, after which a started bootloader runs OnStart.
func (b *Bootloader) Close() error {
	if b.Started && b.OnStart != nil {
		b.OnStart()
	}
	return nil
}

func (b *Bootloader) reply(ok bool) {
	if ok {
		b.Output = append(b.Output, "ACK\r\n"...)
	} else {
		b.Output = append(b.Output, "ERR\r\n"...)
	}
}

func (b *Bootloader) handle(cmd string) {
	b.Commands = append(b.Commands, cmd)
	if b.Started || (b.SilentOn != "" && strings.HasPrefix(cmd, b.SilentOn)) {
		return
	}
	if b.FailOn != "" && strings.HasPrefix(cmd, b.FailOn) {
		b.reply(false)
		return
	}
	var args []uint64
	for s := range strings.SplitSeq(cmd[1:len(cmd)-1], ",") {
		if s == "" {
			continue
		}
		v, err := strconv.ParseUint(s, 16, 32)
		if err != nil {
			b.t.Errorf("fake bootloader: %q: %v", cmd, err)
			b.reply(false)
			return
		}
		args = append(args, v)
	}
	counts := map[byte][]int{'E': {1, 2}, 'U': {1}, 'W': {2}, 'C': {3}, 'S': {0}}[cmd[0]]
	if !slices.Contains(counts, len(args)) {
		b.t.Errorf("fake bootloader: unexpected command %q", cmd)
		b.reply(false)
		return
	}
	switch cmd[0] {
	case 'E':
		addr, end := args[0], uint64(driverEnd)
		if len(args) == 2 {
			end = addr + args[1]
		}
		if addr < driverStart || end > driverEnd || addr > end {
			b.reply(false)
			return
		}
		if addr%4096 != 0 || end%4096 != 0 {
			b.t.Errorf("fake bootloader: %q erases part of a page", cmd)
		}
		copy(b.Flash[addr:end], bytes.Repeat([]byte{0xff}, int(end-addr)))
		b.reply(true)
	case 'U':
		b.buffer = nil
		b.receiving = int(args[0])
		b.reply(args[0] <= 4096)
	case 'W':
		addr, end := args[0], args[0]+args[1]
		if addr < driverStart || end > driverEnd {
			b.reply(false)
			return
		}
		if int(args[1]) != len(b.buffer) {
			b.t.Errorf("fake bootloader: %q writes %d bytes from a buffer of %d", cmd, args[1], len(b.buffer))
		}
		for i, v := range b.buffer {
			if b.Flash[addr+uint64(i)] != 0xff {
				b.t.Errorf("fake bootloader: %q writes 0x%x, which is not erased", cmd, addr+uint64(i))
				break
			}
			b.Flash[addr+uint64(i)] = v
		}
		b.Writes = append(b.Writes, uint32(addr))
		b.reply(true)
	case 'C':
		addr, end, crc := args[0], args[0]+args[1], uint32(args[2])
		if addr == bootloaderStart && args[1] == bootloaderLength {
			b.reply(!b.Other && crc == bootloaderCRC32)
			return
		}
		b.reply(end <= uint64(len(b.Flash)) && crc32.ChecksumIEEE(b.Flash[addr:end]) == crc)
	case 'S':
		b.reply(true)
		b.Started = true
	}
}
