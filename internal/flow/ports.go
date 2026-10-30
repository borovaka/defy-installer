// SPDX-License-Identifier: MIT

package flow

import (
	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// device is one Neuron as its USB ports show it.
type device struct {
	mode neuron.Mode
	// ports are its ports in the order listed: one, or ZMK's two.
	ports []usbserial.Port
}

// neurons sorts ports into the Neurons connected, in the order listed.
// Each port of a bootloader, of the UF2 loader or of Dygma's firmware is a
// Neuron, since each has one port; the ports of ZMK with one USB serial
// number at one USB location are one. Ports of other devices, and ZMK's
// without a serial number that tells them apart, are left out.
func neurons(ports []usbserial.Port) []device {
	var devices []device
	zmk := map[string]int{}
	for _, p := range ports {
		switch mode := neuron.ModeOf(p); {
		case neuron.IsZMK(p):
			key := p.Serial + "@" + p.Location
			if i, ok := zmk[key]; ok {
				devices[i].ports = append(devices[i].ports, p)
				continue
			}
			zmk[key] = len(devices)
			devices = append(devices, device{mode, []usbserial.Port{p}})
		case mode == neuron.Stock || mode == neuron.Bootloader || mode == neuron.Loader:
			devices = append(devices, device{mode, []usbserial.Port{p}})
		}
	}
	return devices
}

// inMode returns the devices in mode.
func inMode(devices []device, mode neuron.Mode) []device {
	var in []device
	for _, d := range devices {
		if d.mode == mode {
			in = append(in, d)
		}
	}
	return in
}
