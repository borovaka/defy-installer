// SPDX-License-Identifier: MIT

// Package neuron works with the Defy's Neuron, the receiver that holds the
// keymap: it identifies the Neuron by what its USB ports say, enters its
// bootloader from ZMK, from the UF2 loader or, through Entry.Ask, from
// Dygma's firmware, sees the firmware come back, and writes the Neuron
// through Dygma's n2_bootloader 1.0.0.
//
// The bootloader takes short ASCII commands with hex arguments, and
// answers each with "ACK\r\n" or "ERR\r\n":
//
//	E<addr>,<len>#        erase
//	U<len>#               then <len> raw bytes into its buffer
//	W<addr>,<len>#        write the buffer
//	C<addr>,<len>,<crc>#  compare the CRC32 of flash with <crc>
//	S#                    start the firmware
//
// Its E and W refuse anything below 0x1000 or past 0x77000, so Nordic's
// MBR and the bootloader are out of their reach. This package also
// refuses, before sending it, any erase or write outside 0x1000..0x72000,
// which keeps Dygma's settings as they are. zmk-defy-wireless's
// docs/neuron-flash.md and docs/software-bootloader.md describe the
// layout, the write and the entry this package follows.
package neuron

import (
	"strings"

	"github.com/borovaka/defy-installer/internal/usbserial"
)

// The Neuron's USB IDs in each mode.
var (
	// ZMKID is ZMK's own ID, which every ZMK keyboard shares; the product
	// string tells the Neuron apart.
	ZMKID        = usbserial.USBID{Vendor: 0x1d50, Product: 0x615e}
	StockID      = usbserial.USBID{Vendor: 0x35ef, Product: 0x0012}
	BootloaderID = usbserial.USBID{Vendor: 0x35ef, Product: 0x0013}
	// LoaderID is the UF2 loader's ID while it shows its DEFY drive. It is
	// Zephyr's testing ID, a placeholder until the loader has a product ID
	// of its own.
	LoaderID = usbserial.USBID{Vendor: 0x2fe3, Product: 0x0100}
)

// zmkProduct is the product string of the ZMK Neuron, and loaderProduct
// the UF2 loader's. Every Zephyr sample uses LoaderID too, so the loader
// is told apart by its product string.
const (
	zmkProduct    = "Defy"
	loaderProduct = "Defy UF2 loader"
)

// Mode is what a port's USB identity says the Neuron runs.
type Mode int

const (
	NotNeuron Mode = iota
	ZMK
	Stock
	Bootloader
	// Loader is the UF2 loader in front of ZMK, showing its drive.
	Loader
)

func (m Mode) String() string {
	switch m {
	case ZMK:
		return "ZMK"
	case Stock:
		return "Dygma firmware"
	case Bootloader:
		return "bootloader"
	case Loader:
		return "UF2 loader"
	}
	return "not a Neuron"
}

// ModeOf tells which mode the Neuron behind a port is in, if the port is a
// Neuron's.
func ModeOf(p usbserial.Port) Mode {
	switch {
	case p.ID == ZMKID && p.Product == zmkProduct:
		return ZMK
	case p.ID == StockID:
		return Stock
	case p.ID == BootloaderID:
		return Bootloader
	case p.ID == LoaderID && p.Product == loaderProduct:
		return Loader
	}
	return NotNeuron
}

// IsZMK reports whether p is a port of a Neuron running ZMK whose USB
// serial number tells it apart: one that is not blank or all zeros.
func IsZMK(p usbserial.Port) bool {
	return ModeOf(p) == ZMK && strings.Trim(strings.TrimSpace(p.Serial), "0") != ""
}
