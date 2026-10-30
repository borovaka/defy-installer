// SPDX-License-Identifier: MIT

// Package usbserial lists USB serial ports and opens them.
//
// Listing reads what the operating system recorded when each device
// enumerated: sysfs on Linux, the I/O Kit registry on macOS and the device
// manager's properties on Windows. It opens no port and sends nothing to
// any device.
package usbserial

import (
	"cmp"
	"context"
	"fmt"
	"slices"
)

// USBID is a USB vendor and product ID.
type USBID struct {
	Vendor, Product uint16
}

func (id USBID) String() string {
	return fmt.Sprintf("%04x:%04x", id.Vendor, id.Product)
}

// Port is a USB serial port as the operating system lists it.
type Port struct {
	// Path is what Open takes: /dev/ttyACM0, /dev/cu.usbmodem1101 or COM5.
	Path string
	ID   USBID
	// Serial is the device's USB serial number, empty if it has none.
	Serial string
	// Product is the device's USB product string.
	Product string
	// Location names the USB socket the device is plugged into. All of a
	// device's ports share it, and it stays the same when the device
	// enumerates again in another mode.
	Location string
}

// List returns the USB serial ports, sorted by path.
func List(ctx context.Context) ([]Port, error) {
	ports, err := list(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing serial ports: %w", err)
	}
	slices.SortFunc(ports, func(a, b Port) int { return cmp.Compare(a.Path, b.Path) })
	return ports, nil
}
