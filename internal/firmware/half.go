// SPDX-License-Identifier: MIT

package firmware

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"slices"
)

// The halves' RP2040 flash, as their factory bootloader lays it out, in
// offsets from the start of flash. The bootloader starts the factory
// package at HalfPackageStart: a seal, then the program.
const (
	HalfPackageStart = 0x5000
	// HalfStorageStart is where the halves' settings start; never written.
	HalfStorageStart = 0x1C0000
	// HalfPageSize is the unit the bootloader writes, and packages are
	// padded to.
	HalfPageSize = 256
	// HalfSectorSize is the unit the bootloader erases.
	HalfSectorSize = 4096

	halfFlashBase    = 0x10000000
	halfProgramStart = 0x6000
	halfVectorStart  = 0x6100 // the application's vector table
	halfRAMStart     = 0x20000000
	halfRAMEnd       = 0x20042000
	sealSize         = 28
	metadataSize     = 32
)

// metadataMagic starts the metadata zmk-defy-wireless's builds put at the
// start of the program. Dygma's keyscanner has none.
var metadataMagic = []byte("DFYRP204")

// Side is the half a package is built for, from its metadata.
type Side uint8

const (
	// NoSide is a package without metadata, such as Dygma's keyscanner.
	NoSide Side = iota
	Left
	Right
)

func (s Side) String() string {
	switch s {
	case Left:
		return "left"
	case Right:
		return "right"
	}
	return "no side"
}

// sideIDs are the metadata's side numbers.
var sideIDs = map[uint32]Side{0: Right, 1: Left}

// keyboardRole is the metadata's role number for a keyboard half, the only
// role there is.
const keyboardRole = 1

// HalfImage is a factory package for a half.
type HalfImage struct {
	Name       string
	Data       []byte
	Version    uint32
	ProgramCRC uint32
	Side       Side
}

// HalfProgress reports a half's write: done of the transfer's total bytes.
// It is called after each page the Neuron accepts, with done below total,
// and with done equal to total once the half is written and validated.
type HalfProgress func(side Side, done, total int)

// LoadHalfImage reads and checks a half's factory package.
func LoadHalfImage(path string) (*HalfImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// One byte more than fits, so that too large a file fails the check.
	data, err := io.ReadAll(io.LimitReader(f, HalfStorageStart-HalfPackageStart+1))
	if err != nil {
		return nil, err
	}
	return ParseHalfImage(path, data)
}

// ParseHalfImage checks a half's factory package: its seal, program CRC,
// vectors and, if it has them, its metadata.
func ParseHalfImage(name string, data []byte) (*HalfImage, error) {
	image, err := verifyPackage(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	image.Name = name
	image.Data = data
	return image, nil
}

func verifyPackage(pkg []byte) (*HalfImage, error) {
	if len(pkg) < halfVectorStart-HalfPackageStart+8 {
		return nil, errors.New("factory package is truncated")
	}
	// The limit is a whole number of sectors, so TransferData's padding
	// stays below the settings too.
	if len(pkg)%HalfPageSize != 0 || len(pkg) > HalfStorageStart-HalfPackageStart {
		return nil, errors.New("factory package size overlaps settings storage or is not page aligned")
	}
	var seal [7]uint32
	for i := range seal {
		seal[i] = binary.LittleEndian.Uint32(pkg[4*i:])
	}
	kind, size, sealCRCField, start, length, programCRC, version := seal[0], seal[1], seal[2], seal[3], seal[4], seal[5], seal[6]
	if kind != 1 || size != sealSize || start != halfProgramStart || length%4 != 0 {
		return nil, errors.New("unsupported factory seal layout")
	}
	if sealCRC(pkg[:sealSize]) != sealCRCField {
		return nil, errors.New("factory seal CRC mismatch")
	}
	offset := uint64(halfProgramStart - HalfPackageStart)
	end := offset + uint64(length)
	if length < halfVectorStart-halfProgramStart+8 || end > uint64(len(pkg)) {
		return nil, errors.New("factory program is truncated")
	}
	if (end+HalfPageSize-1)/HalfPageSize*HalfPageSize != uint64(len(pkg)) {
		return nil, errors.New("unexpected data after factory program")
	}
	program := pkg[offset:end]
	if crc32.ChecksumIEEE(program) != programCRC {
		return nil, errors.New("factory program CRC mismatch")
	}
	if err := checkVectors(program[halfVectorStart-halfProgramStart:], length); err != nil {
		return nil, err
	}
	side := NoSide
	if bytes.HasPrefix(program, metadataMagic) {
		var err error
		if side, err = checkMetadata(program, version); err != nil {
			return nil, err
		}
	}
	return &HalfImage{Version: version, ProgramCRC: programCRC, Side: side}, nil
}

// sealCRC is the CRC-32 of a seal with its own CRC field zeroed.
func sealCRC(seal []byte) uint32 {
	unsigned := bytes.Clone(seal[:sealSize])
	clear(unsigned[8:12])
	return crc32.ChecksumIEEE(unsigned)
}

func checkVectors(vectors []byte, programLength uint32) error {
	stack, reset := binary.LittleEndian.Uint32(vectors), binary.LittleEndian.Uint32(vectors[4:])
	if stack%8 != 0 || stack <= halfRAMStart || stack > halfRAMEnd {
		return errors.New("initial stack pointer is outside aligned RP2040 SRAM")
	}
	if reset&1 == 0 {
		return errors.New("reset vector must be a Thumb address")
	}
	target := uint64(reset &^ 1)
	if target < halfFlashBase+halfVectorStart+8 || target >= halfFlashBase+halfProgramStart+uint64(programLength) {
		return errors.New("reset vector is outside the factory application image")
	}
	return nil
}

// checkMetadata reads the metadata at the start of program: magic, schema,
// size, role, side, version and a reserved word, then zeros up to the
// vector table.
func checkMetadata(program []byte, version uint32) (Side, error) {
	field := func(i int) uint32 { return binary.LittleEndian.Uint32(program[8+4*i:]) }
	schema, size, role, sideID, metadataVersion, reserved := field(0), field(1), field(2), field(3), field(4), field(5)
	if schema != 1 || size != metadataSize || reserved != 0 || metadataVersion != version {
		return NoSide, errors.New("unsupported factory image metadata layout or version")
	}
	side, ok := sideIDs[sideID]
	if role != keyboardRole || !ok {
		return NoSide, errors.New("unsupported factory image role or side")
	}
	if slices.ContainsFunc(program[metadataSize:halfVectorStart-halfProgramStart], func(b byte) bool { return b != 0 }) {
		return NoSide, errors.New("factory image metadata padding must be zero")
	}
	return side, nil
}

// TransferData is the package padded with 0xFF to whole sectors, as the
// half updater sends it.
func (h *HalfImage) TransferData() []byte {
	pad := -len(h.Data) & (HalfSectorSize - 1)
	return append(bytes.Clone(h.Data), bytes.Repeat([]byte{0xff}, pad)...)
}

// Pages returns TransferData in HalfPageSize pages, in address order, each
// at its offset in the half's flash from HalfPackageStart. Both ways of
// writing a half, ZMK's half updater and Dygma's Neuron firmware, send
// these pages in this order.
func (h *HalfImage) Pages() []Page {
	data := h.TransferData()
	pages := make([]Page, 0, len(data)/HalfPageSize)
	for offset := 0; offset < len(data); offset += HalfPageSize {
		pages = append(pages, Page{Addr: HalfPackageStart + uint32(offset), Data: data[offset : offset+HalfPageSize]})
	}
	return pages
}

// CheckHalfPage checks that p is a whole HalfPageSize page, aligned to its
// size, within HalfPackageStart..HalfStorageStart: the only place a half
// is written.
func CheckHalfPage(p Page) error {
	if len(p.Data) != HalfPageSize || p.Addr%HalfPageSize != 0 ||
		p.Addr < HalfPackageStart || uint64(p.Addr)+uint64(len(p.Data)) > HalfStorageStart {
		return fmt.Errorf("%d bytes at %#x are not an aligned %d-byte page within %#x..%#x",
			len(p.Data), p.Addr, HalfPageSize, HalfPackageStart, HalfStorageStart)
	}
	return nil
}

// isStock reports whether the package is Dygma's 2.3.0 keyscanner.bin.
func (h *HalfImage) isStock() bool {
	return sha256.Sum256(h.Data) == stockKeyscannerSHA256
}

// RequireApplication checks that the package is a ZMK build for side.
func (h *HalfImage) RequireApplication(side Side) error {
	switch {
	case h.isStock():
		return fmt.Errorf("%s is Dygma's 2.3.0 keyscanner; defy-installer uninstall puts Dygma's firmware back", h.Name)
	case h.Side == NoSide:
		return fmt.Errorf("%s has no side in its metadata; rebuild the matching left or right target", h.Name)
	case h.Side != side:
		return fmt.Errorf("%s is built for the %s half, not the %s", h.Name, h.Side, side)
	}
	return nil
}

// RequireStock checks that the package is Dygma's 2.3.0 keyscanner.bin.
func (h *HalfImage) RequireStock() error {
	if !h.isStock() {
		return fmt.Errorf("%s is not Dygma's 2.3.0 keyscanner.bin (SHA-256 %x); only the pinned release file is accepted",
			h.Name, sha256.Sum256(h.Data))
	}
	return nil
}
