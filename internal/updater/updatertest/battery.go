// SPDX-License-Identifier: MIT

package updatertest

import "encoding/binary"

// BatteryRecord is one of BATTERY's records: side, flags, links and the
// level in percent, then the half's last battery frame.
func BatteryRecord(side, flags, links, level byte, frame []byte) []byte {
	le := binary.LittleEndian
	r := le.AppendUint32([]byte{side, flags, links, 2}, 1000)
	r = le.AppendUint32(r, 0)
	r = le.AppendUint16(append(r, level, 0), 4100)
	return append(r, frame...)
}

// BatteryFrame is a half's battery frame: an analogue sample with the
// charger's status register.
func BatteryFrame(side, charger byte) []byte {
	le := binary.LittleEndian
	f := []byte{'D', 'B', 1, side}
	f = le.AppendUint32(f, 7)
	f = le.AppendUint32(f, 3)
	f = append(f, 1, 4)
	for _, w := range []uint16{0xa103, 0x5020, 2, 4100, uint16(charger)<<8 | 0x94, 0, 0, 0x0300} {
		f = le.AppendUint16(f, w)
	}
	return le.AppendUint16(f, crc16(f))
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
