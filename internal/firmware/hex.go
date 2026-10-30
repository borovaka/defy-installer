// SPDX-License-Identifier: MIT

// Package firmware reads and checks the images the installer writes: the
// Neuron's Intel HEX files, ZMK's and Dygma's, and the halves' packages.
// It touches no device.
package firmware

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
)

// memory is the data an Intel HEX file places, by address.
type memory struct {
	spans []span // by address, apart from each other, merged where they meet
}

type span struct {
	addr uint32
	data []byte
}

// Page is one flash page: its address, and its bytes with 0xFF where the
// image has no data.
type Page struct {
	Addr uint32
	Data []byte
}

// parseHex reads Intel HEX text whose data must lie within [lo, hi). Data
// records that end at or below skip are dropped after the record checks,
// without the range and overlap checks.
func parseHex(text []byte, lo, hi, skip uint32) (*memory, error) {
	type record struct {
		line int
		addr uint32
		data []byte
	}
	var records []record
	var base uint64
	ended := false
	text = bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
	text = bytes.ReplaceAll(text, []byte("\r"), []byte("\n"))
	n := 0
	for line := range bytes.Lines(text) {
		n++
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if ended {
			return nil, fmt.Errorf("line %d: data after the EOF record", n)
		}
		raw, ok := decodeRecord(line)
		if !ok {
			return nil, fmt.Errorf("line %d: invalid Intel HEX record", n)
		}
		length, offset, kind := raw[0], uint64(binary.BigEndian.Uint16(raw[1:3])), raw[3]
		data := raw[4 : len(raw)-1]
		switch kind {
		case 0:
			if length == 0 || offset+uint64(length) > 0x10000 {
				return nil, fmt.Errorf("line %d: invalid data span", n)
			}
			start := base + offset
			end := start + uint64(length)
			if end <= uint64(skip) {
				continue
			}
			if start < uint64(lo) || end > uint64(hi) {
				return nil, fmt.Errorf("line %d: data outside 0x%x..0x%x", n, lo, hi)
			}
			records = append(records, record{n, uint32(start), data})
		case 1:
			if length != 0 || offset != 0 {
				return nil, fmt.Errorf("line %d: invalid EOF record", n)
			}
			ended = true
		case 2, 4:
			if length != 2 || offset != 0 {
				return nil, fmt.Errorf("line %d: invalid extended address", n)
			}
			base = uint64(binary.BigEndian.Uint16(data))
			if kind == 2 {
				base <<= 4
			} else {
				base <<= 16
			}
		case 3, 5:
			if length != 4 || offset != 0 {
				return nil, fmt.Errorf("line %d: invalid start address", n)
			}
		default:
			return nil, fmt.Errorf("line %d: unsupported record type %d", n, kind)
		}
	}
	if !ended || len(records) == 0 {
		return nil, errors.New("no data, or no EOF record")
	}

	slices.SortStableFunc(records, func(a, b record) int { return cmp.Compare(a.addr, b.addr) })
	m := &memory{}
	var end uint64
	line := 0
	for _, r := range records {
		if uint64(r.addr) < end {
			return nil, fmt.Errorf("line %d: overlapping data at 0x%x", max(r.line, line), r.addr)
		}
		if last := len(m.spans) - 1; last >= 0 && uint64(r.addr) == end {
			m.spans[last].data = append(m.spans[last].data, r.data...)
		} else {
			m.spans = append(m.spans, span{r.addr, slices.Clone(r.data)})
		}
		end, line = uint64(r.addr)+uint64(len(r.data)), r.line
	}
	return m, nil
}

// decodeRecord decodes a record line: a colon, then length, address, type,
// data and checksum in hex.
func decodeRecord(line []byte) ([]byte, bool) {
	if line[0] != ':' {
		return nil, false
	}
	raw := make([]byte, hex.DecodedLen(len(line)-1))
	if _, err := hex.Decode(raw, line[1:]); err != nil || len(raw) < 5 || len(raw) != int(raw[0])+5 {
		return nil, false
	}
	var sum byte
	for _, b := range raw {
		sum += b
	}
	return raw, sum == 0
}

// End is one past the highest address with data.
func (m *memory) End() uint32 {
	last := m.spans[len(m.spans)-1]
	return last.addr + uint32(len(last.data))
}

// Bytes returns the n bytes from addr, if the image has data at all of
// them.
func (m *memory) Bytes(addr uint32, n int) ([]byte, bool) {
	i, found := slices.BinarySearchFunc(m.spans, addr, func(s span, a uint32) int { return cmp.Compare(s.addr, a) })
	if !found {
		if i == 0 {
			return nil, false
		}
		i--
	}
	s := m.spans[i]
	off := uint64(addr - s.addr)
	if off+uint64(n) > uint64(len(s.data)) {
		return nil, false
	}
	return slices.Clone(s.data[off : off+uint64(n)]), true
}

// Has reports whether the image has data at addr.
func (m *memory) Has(addr uint32) bool {
	_, ok := m.Bytes(addr, 1)
	return ok
}

// Pages splits the data into pages of size bytes, a power of two, in
// address order. Gaps stay 0xFF, as erased flash.
func (m *memory) Pages(size uint32) []Page {
	var pages []Page
	for _, s := range m.spans {
		start, end := uint64(s.addr), uint64(s.addr)+uint64(len(s.data))
		for a := start; a < end; {
			page := a &^ uint64(size-1)
			if len(pages) == 0 || uint64(pages[len(pages)-1].Addr) != page {
				pages = append(pages, Page{uint32(page), bytes.Repeat([]byte{0xff}, int(size))})
			}
			next := min(page+uint64(size), end)
			copy(pages[len(pages)-1].Data[a-page:], s.data[a-start:next-start])
			a = next
		}
	}
	return pages
}
