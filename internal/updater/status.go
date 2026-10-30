// SPDX-License-Identifier: MIT

package updater

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/borovaka/defy-installer/internal/firmware"
)

const (
	splitStatusVersion = 1
	splitSlotSize      = 48
	// phaseReporting is a slot whose half's key reports arrive, and
	// phaseRecovery the last phase version 1 defines.
	phaseReporting = 7
	phaseRecovery  = 10
)

// parseSplitStatus counts SPLIT STATUS's records in the reporting phase.
// The records are the Neuron's two Bluetooth split connection slots, not
// sides.
func parseSplitStatus(reply []byte) (int, error) {
	if err := checkSnapshot(OpSplitStatus, reply, splitStatusVersion, splitSlotSize); err != nil {
		return 0, err
	}
	reporting := 0
	for i := range snapshotRecords {
		r := reply[snapshotHeader+i*splitSlotSize:][:splitSlotSize]
		if int(r[0]) != i || r[1] > phaseRecovery {
			return 0, fmt.Errorf("updater SPLIT STATUS record %d gives slot %d in phase %d", i, r[0], r[1])
		}
		if r[1] == phaseReporting {
			reporting++
		}
	}
	return reporting, nil
}

// SplitStatus sends SPLIT STATUS and counts the Neuron's Bluetooth split
// connection slots in the reporting phase: the halves connected over
// Bluetooth whose key reports arrive. Firmware without it gives an error
// matching ErrUnsupported.
func (c *Client) SplitStatus(ctx context.Context) (int, error) {
	reply, err := c.query(ctx, OpSplitStatus, c.timeout)
	if err != nil {
		return 0, err
	}
	return parseSplitStatus(reply)
}

// Charge is a half's charger state, from its battery frame.
type Charge uint8

const (
	// ChargeUnknown is a half whose frame does not show its charger.
	ChargeUnknown Charge = iota
	NotCharging
	Charging
	Charged
	ChargeFault
)

// BatteryRecord is the Neuron's cached battery record for one half. The
// zero BatteryRecord is a half the Neuron knows nothing about.
type BatteryRecord struct {
	// Connected is set when the half is connected, and Valid when its
	// sample gives a level.
	Connected, Valid bool
	// Cable and Bluetooth are the links the sample is live on.
	Cable, Bluetooth bool
	// Level is the percentage, when Valid.
	Level  int
	Charge Charge
}

// Battery is what BATTERY reads: a record for each half.
type Battery struct {
	records [2]BatteryRecord // left, then right
}

// Side returns the record for the left or the right half, and an unknown
// record for any other side.
func (b Battery) Side(side firmware.Side) BatteryRecord {
	if id, err := sideID(side); err == nil {
		return b.records[id]
	}
	return BatteryRecord{}
}

const (
	batteryVersion    = 1
	batteryRecordSize = 48
	batteryFrameSize  = 32
)

// parseBattery reads BATTERY's records, left then right. A record that
// names another side, or gives a level over 100%, is unknown; the other
// still counts.
func parseBattery(reply []byte) (Battery, error) {
	if err := checkSnapshot(OpBattery, reply, batteryVersion, batteryRecordSize); err != nil {
		return Battery{}, err
	}
	// The record's flags.
	const present, connected, valid = 1, 2, 8
	var b Battery
	for i := range b.records {
		r := reply[snapshotHeader+i*batteryRecordSize:][:batteryRecordSize]
		flags, links := r[1], Link(r[2])
		if int(r[0]) != i || flags&valid != 0 && r[12] > 100 {
			continue
		}
		rec := BatteryRecord{Connected: flags&connected != 0, Valid: flags&valid != 0,
			Cable: links&Cable != 0, Bluetooth: links&Bluetooth != 0}
		if rec.Valid {
			rec.Level = int(r[12])
		}
		if flags&present != 0 {
			rec.Charge = chargeOf(r[16:][:batteryFrameSize])
		}
		b.records[i] = rec
	}
	return b, nil
}

// chargeOf reads the charger's state from a half's battery frame, as
// zmk-defy-wireless's battery protocol lays it out: "DB", the version 1,
// the side, the session, the sample's sequence, age and status, eight
// 16-bit words and a CRC16. An analogue sample's words hold the charger's
// registers: its identity in the low byte of word 4 and its status
// register in the high byte, and in the high byte of word 7 a mask of the
// registers read. Like the firmware, it takes the state only from a
// charger whose identity passes (id & 0xfc) == 0x94, the FAN5405's.
func chargeOf(frame []byte) Charge {
	le := binary.LittleEndian
	if frame[0] != 'D' || frame[1] != 'B' || frame[2] != 1 || crc16(frame[:30]) != le.Uint16(frame[30:]) {
		return ChargeUnknown
	}
	const analogOK, analogError = 4, 5
	words := frame[14:30]
	format := le.Uint16(words)
	if status := frame[13]; status != analogOK && status != analogError || format != 0xa102 && format != 0xa103 {
		return ChargeUnknown
	}
	const idRead, statusRead = 1, 2
	if read := words[15]; read&idRead == 0 || read&statusRead == 0 {
		return ChargeUnknown
	}
	const idMask, fan5405 = 0xfc, 0x94
	if words[8]&idMask != fan5405 {
		return ChargeUnknown
	}
	switch words[9] >> 4 & 3 {
	case 0:
		return NotCharging
	case 1:
		return Charging
	case 2:
		return Charged
	}
	return ChargeFault
}

// crc16 is the CRC the halves' cable frames carry: CRC-16/CCITT-FALSE.
func crc16(data []byte) uint16 {
	crc := uint16(0xffff)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// Battery sends BATTERY and reads the Neuron's cached battery records. It
// neither wakes a half nor asks for a new sample. Firmware without it gives
// an error matching ErrUnsupported.
func (c *Client) Battery(ctx context.Context) (Battery, error) {
	reply, err := c.query(ctx, OpBattery, c.timeout)
	if err != nil {
		return Battery{}, err
	}
	return parseBattery(reply)
}
