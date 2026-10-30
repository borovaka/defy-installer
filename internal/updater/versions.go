// SPDX-License-Identifier: MIT

package updater

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
)

// Version is what VERSION reads: what the Neuron's firmware was built
// from.
type Version struct {
	// Module is the zmk-defy-wireless version, such as v0.1.0, or
	// UnknownModule.
	Module string
	// Source is the keymap repository as owner/repo, or local.
	Source string
	// Commit is that repository's commit in full, with -dirty for a
	// changed tree; empty for a build outside git.
	Commit string
}

// LocalSource is the source of a build whose keymap is not in a GitHub
// repository.
const LocalSource = "local"

// UnknownModule is the module version of a build that has none, such as
// one from a module that is not a git checkout.
const UnknownModule = "unknown"

// Dirty reports whether the build's keymap repository had changes that
// were not committed.
func (v Version) Dirty() bool {
	return strings.HasSuffix(v.Commit, "-dirty")
}

const versionFormat = 1

// The longest of each string VERSION returns, in bytes.
var versionLimits = [3]int{32, 140, 72}

// parseVersion reads VERSION's reply: format 1, three zero bytes, then the
// module version, the source and the commit, each a length byte and that
// many printable ASCII bytes.
func parseVersion(reply []byte) (Version, error) {
	if len(reply) < 4 || reply[0] != versionFormat || reply[1]|reply[2]|reply[3] != 0 {
		return Version{}, fmt.Errorf("updater VERSION reply starts %x, not format %d", reply[:min(4, len(reply))], versionFormat)
	}
	rest := reply[4:]
	var fields [3]string
	for i, limit := range versionLimits {
		if len(rest) == 0 || int(rest[0]) >= len(rest) {
			return Version{}, fmt.Errorf("updater VERSION reply is %d bytes, too short for its strings", len(reply))
		}
		n := int(rest[0])
		if n > limit {
			return Version{}, fmt.Errorf("updater VERSION reply gives string %d %d bytes, more than %d", i+1, n, limit)
		}
		field := rest[1 : 1+n]
		if !printable(field) {
			return Version{}, fmt.Errorf("updater VERSION reply gives string %d %q, not printable ASCII", i+1, field)
		}
		fields[i] = string(field)
		rest = rest[1+n:]
	}
	if len(rest) > 0 {
		return Version{}, fmt.Errorf("updater VERSION reply has %d bytes after its strings", len(rest))
	}
	return Version{Module: fields[0], Source: fields[1], Commit: fields[2]}, nil
}

// printable reports whether b is printable ASCII.
func printable(b []byte) bool {
	return !bytes.ContainsFunc(b, func(r rune) bool { return r < 0x20 || r > 0x7e })
}

// Version sends VERSION and reads what the Neuron's firmware was built
// from. Firmware without it gives an error matching ErrUnsupported.
func (c *Client) Version(ctx context.Context) (Version, error) {
	reply, err := c.query(ctx, OpVersion, c.timeout)
	if err != nil {
		return Version{}, err
	}
	return parseVersion(reply)
}

// HalfReport is what a half running ZMK reports about itself.
type HalfReport struct {
	// ProgramCRC is the program's CRC-32 from the half's own seal, as its
	// package's seal and PROBE give it; 0 without a valid seal.
	ProgramCRC uint32
	// SealVersion is the seal's version; 0 without a valid seal.
	SealVersion uint32
	// Module is the zmk-defy-wireless version the half runs, cut to its
	// first 18 bytes.
	Module string
}

// HalfRecord is the Neuron's latest record from one half.
type HalfRecord struct {
	// Link is the link the report came by: NoLink while the half is off,
	// asleep, held by a session, or runs firmware without the report.
	Link Link
	// Age is how long ago the report arrived.
	Age    time.Duration
	Report HalfReport
}

// Known reports whether the Neuron has a report from the half.
func (r HalfRecord) Known() bool {
	return r.Link != NoLink
}

// Runs reports whether the half reported a valid seal with the program
// CRC-32 crc.
func (r HalfRecord) Runs(crc uint32) bool {
	return r.Known() && r.Report.ProgramCRC != 0 && r.Report.ProgramCRC == crc
}

// HalfVersions is what HALF VERSIONS reads: the Neuron's latest record
// from each half.
type HalfVersions struct {
	records [2]HalfRecord // left, then right
}

// Side returns the record for the left or the right half, and an unknown
// record for any other side.
func (h HalfVersions) Side(side firmware.Side) HalfRecord {
	if id, err := sideID(side); err == nil {
		return h.records[id]
	}
	return HalfRecord{}
}

const (
	halfVersionsVersion = 1
	halfRecordSize      = 36
	halfReportSize      = 28
	halfReportFormat    = 1
	halfModuleSize      = 18
)

// parseHalfVersions reads HALF VERSIONS's records, left then right.
func parseHalfVersions(reply []byte) (HalfVersions, error) {
	if err := checkSnapshot(OpHalfVersions, reply, halfVersionsVersion, halfRecordSize); err != nil {
		return HalfVersions{}, err
	}
	le := binary.LittleEndian
	var h HalfVersions
	for i, side := range halves {
		r := reply[snapshotHeader+i*halfRecordSize:][:halfRecordSize]
		rec := HalfRecord{Link: Link(r[1])}
		if int(r[0]) != i || le.Uint16(r[2:]) != 0 || rec.Link > Bluetooth {
			return HalfVersions{}, fmt.Errorf("updater HALF VERSIONS record %d gives side %d, link %d and reserved %#x",
				i, r[0], r[1], le.Uint16(r[2:]))
		}
		if rec.Known() {
			report := r[8:][:halfReportSize]
			if report[0] != halfReportFormat || int(report[1]) != i {
				return HalfVersions{}, fmt.Errorf("the %v half's report has format %d and side %d", side, report[0], report[1])
			}
			module, _, _ := bytes.Cut(report[10:10+halfModuleSize], []byte{0})
			if !printable(module) {
				return HalfVersions{}, fmt.Errorf("the %v half reports module version %q, not printable ASCII", side, module)
			}
			rec.Age = time.Duration(le.Uint32(r[4:])) * time.Millisecond
			rec.Report = HalfReport{ProgramCRC: le.Uint32(report[2:]), SealVersion: le.Uint32(report[6:]), Module: string(module)}
		}
		h.records[i] = rec
	}
	return h, nil
}

// HalfVersions sends HALF VERSIONS and reads the Neuron's latest record
// from each half. It neither wakes nor resets a half. Firmware without it
// gives an error matching ErrUnsupported.
func (c *Client) HalfVersions(ctx context.Context) (HalfVersions, error) {
	reply, err := c.query(ctx, OpHalfVersions, c.timeout)
	if err != nil {
		return HalfVersions{}, err
	}
	return parseHalfVersions(reply)
}

// clearTimeout is how long CLEAR KEYMAP CHANGES's reply may take: each
// delete scans the Neuron's stored settings.
const clearTimeout = 10 * time.Second

// ClearKeymapChanges sends CLEAR KEYMAP CHANGES: the Neuron deletes the
// keymap changes ZMK Studio saved and reloads the keymap built into its
// image. Firmware older than it, or built without Studio's settings
// storage, gives an error matching ErrUnsupported, and a Neuron with Studio
// unlocked or a half update running one matching ErrStudioUnlocked.
func (c *Client) ClearKeymapChanges(ctx context.Context) error {
	reply, err := c.query(ctx, OpClearKeymap, clearTimeout)
	if err == nil && len(reply) > 0 {
		err = fmt.Errorf("unexpected updater %v reply %x, want none", OpClearKeymap, reply)
	}
	return err
}
