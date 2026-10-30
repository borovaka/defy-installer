// SPDX-License-Identifier: MIT

// Package firmwaretest builds images for tests: Intel HEX files, Neuron
// programs and half packages, and the inputs the recorded scenarios in
// internal/flow/testdata were made with.
package firmwaretest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"strings"
)

// Span is data placed at an address.
type Span struct {
	Addr uint32
	Data []byte
}

// At places data at addr.
func At(addr uint32, data []byte) Span {
	return Span{addr, data}
}

// Pages lays spans out in pages of size bytes, a power of two, with 0xFF
// where they hold no data, as the firmware package lays out an Intel HEX
// file's data. The spans must come in address order. It also returns where
// the data ends.
func Pages(size uint32, spans ...Span) ([]Span, uint32) {
	var pages []Span
	var end uint32
	for _, s := range spans {
		for i, b := range s.Data {
			addr := s.Addr + uint32(i)
			base := addr &^ (size - 1)
			if len(pages) == 0 || pages[len(pages)-1].Addr != base {
				pages = append(pages, Span{base, bytes.Repeat([]byte{0xff}, int(size))})
			}
			pages[len(pages)-1].Data[addr-base] = b
			end = addr + 1
		}
	}
	return pages, end
}

// HexRecord formats one Intel HEX record.
func HexRecord(addr uint16, kind byte, data []byte) string {
	raw := append([]byte{byte(len(data)), byte(addr >> 8), byte(addr), kind}, data...)
	var sum byte
	for _, b := range raw {
		sum += b
	}
	return fmt.Sprintf(":%X", append(raw, -sum))
}

// Hex writes spans as Intel HEX: 16-byte data records, each after an
// extended linear address record, then EOF.
func Hex(spans ...Span) []byte {
	var rows []string
	for _, s := range spans {
		for off := 0; off < len(s.Data); off += 16 {
			a := s.Addr + uint32(off)
			rows = append(rows, HexRecord(0, 4, []byte{byte(a >> 24), byte(a >> 16)}))
			rows = append(rows, HexRecord(uint16(a), 0, s.Data[off:min(off+16, len(s.Data))]))
		}
	}
	rows = append(rows, HexRecord(0, 1, nil))
	return []byte(strings.Join(rows, "\n") + "\n")
}

// NeuronProgram is a Neuron program of length bytes from base: a stack
// pointer at the top of RAM, a reset vector into the image, then a
// pattern.
func NeuronProgram(length int, seed byte, base uint32) []byte {
	data := make([]byte, length)
	for i := range data {
		data[i] = byte(i*31) + seed
	}
	binary.LittleEndian.PutUint32(data, 0x20020000)
	binary.LittleEndian.PutUint32(data[4:], base+0x101)
	return data
}

// RecordedNeuronHex is the ZMK image the recorded Neuron writes were made
// with: NeuronProgram(0x2000, 3, 0x1000) at 0x1000, and 768 bytes
// 0x00..0xff three times at 0x5000.
func RecordedNeuronHex() []byte {
	ramp := make([]byte, 768)
	for i := range ramp {
		ramp[i] = byte(i)
	}
	return Hex(At(0x1000, NeuronProgram(0x2000, 3, 0x1000)), At(0x5000, ramp))
}

// RecordedStock is what stood in for Dygma's files in the recorded
// exchanges: an application NeuronProgram(0x1800, 5, 0x27000) at 0x27000,
// and an S140 file with 0x40 MBR bytes (i*13+1) at 0 and
// NeuronProgram(0x1100, 9, 0x1000) at 0x1000.
func RecordedStock() (app, s140 []Span) {
	mbr := make([]byte, 0x40)
	for i := range mbr {
		mbr[i] = byte(i*13 + 1)
	}
	return []Span{At(0x27000, NeuronProgram(0x1800, 5, 0x27000))},
		[]Span{At(0, mbr), At(0x1000, NeuronProgram(0x1100, 9, 0x1000))}
}

// HalfVector is where a half's application starts, and is linked.
const HalfVector = 0x10006100

// HalfApplication is a half's application: vectors, then 56 bytes.
func HalfApplication(stack, reset uint32) []byte {
	app := binary.LittleEndian.AppendUint32(nil, stack)
	app = binary.LittleEndian.AppendUint32(app, reset)
	for i := 8; i < 64; i++ {
		app = append(app, byte(i))
	}
	return app
}

// ValidHalfApplication has a stack at the top of SRAM and a reset vector
// into itself.
func ValidHalfApplication() []byte {
	return HalfApplication(0x20042000, HalfVector+0x21)
}

// RecordedHalfApp is the half application the recorded exchanges were
// made with: a stack of 0x20042000, a reset vector of 0x10006109, then 992
// bytes of fill. Their packages are HalfPackage(RecordedHalfApp(0xa5), 17,
// side) for ZMK, and HalfPackage(RecordedHalfApp(0x0a), 101, Left) for
// Dygma's keyscanner.
func RecordedHalfApp(fill byte) []byte {
	app := binary.LittleEndian.AppendUint32(nil, 0x20042000)
	app = binary.LittleEndian.AppendUint32(app, HalfVector+9)
	return append(app, bytes.Repeat([]byte{fill}, 992)...)
}

// Side is the metadata's side number.
type Side uint32

const (
	Right Side = 0
	Left  Side = 1
)

// The package layout, in offsets from the package's start.
const (
	programOffset  = 0x1000 // the program, after the seal
	vectorOffset   = 0x100  // the vector table, after the metadata
	sealSize       = 28
	metadataSize   = 32
	pageSize       = 256
	programAddress = 0x6000 // where the program sits in flash
)

// HalfPackage builds a half's factory package from an application linked
// at HalfVector, as zmk-defy-wireless's scripts/package_factory.py does,
// without its checks.
func HalfPackage(app []byte, version uint32, side Side) []byte {
	length := (vectorOffset + len(app) + 3) &^ 3
	program := make([]byte, vectorOffset, length)
	copy(program, "DFYRP204")
	for i, v := range []uint32{1, metadataSize, 1, uint32(side), version, 0} {
		binary.LittleEndian.PutUint32(program[8+4*i:], v)
	}
	program = append(program, app...)
	program = append(program, bytes.Repeat([]byte{0xff}, length-len(program))...)
	return Seal(program, version)
}

// Seal puts a seal and zero padding around a program.
func Seal(program []byte, version uint32) []byte {
	pkg := make([]byte, programOffset, programOffset+len(program)+pageSize)
	for i, v := range []uint32{1, sealSize, 0, programAddress, uint32(len(program)), crc32.ChecksumIEEE(program), version} {
		binary.LittleEndian.PutUint32(pkg[4*i:], v)
	}
	// The seal's CRC covers the seal with the CRC field zero.
	binary.LittleEndian.PutUint32(pkg[8:], crc32.ChecksumIEEE(pkg[:sealSize]))
	pkg = append(pkg, program...)
	return append(pkg, make([]byte, -len(pkg)&(pageSize-1))...)
}

// Reseal recomputes a changed package's CRCs, so that only the change
// itself can fail the checks.
func Reseal(pkg []byte) []byte {
	length := binary.LittleEndian.Uint32(pkg[16:])
	return Seal(pkg[programOffset:programOffset+int(length)], binary.LittleEndian.Uint32(pkg[24:]))
}
