// SPDX-License-Identifier: MIT

package usbserial

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The fixtures are ioreg's output on a Mac with the Neuron running ZMK,
// trimmed to the entries on each port's chain and with a made-up serial
// number. ioreg-hub.plist moves the Neuron behind a USB hub.

func neuronPorts(location string) []Port {
	neuron := Port{ID: USBID{0x1d50, 0x615e}, Serial: "0123456789ABCDEF", Product: "Defy", Location: location}
	first, second := neuron, neuron
	first.Path, second.Path = "/dev/cu.usbmodem1101", "/dev/cu.usbmodem1104"
	return []Port{first, second}
}

func parseFixture(t *testing.T, name string) []Port {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ports, err := parseIoreg(f)
	if err != nil {
		t.Fatal(err)
	}
	return ports
}

func TestIoregListsOnlyUSBPorts(t *testing.T) {
	// The fixture also has the debug console and three Bluetooth ports.
	got := parseFixture(t, "ioreg-zmk.plist")
	if want := neuronPorts("0x01100000"); !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestIoregFindsTheDeviceBehindAHub(t *testing.T) {
	// The hub is a USB device too, with its own IDs; the port belongs to
	// the nearest device above it.
	got := parseFixture(t, "ioreg-hub.plist")
	if want := neuronPorts("0x01120000"); !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestIoregWithoutPorts(t *testing.T) {
	for _, out := range []string{"", `<?xml version="1.0"?><plist version="1.0"><array/></plist>`} {
		ports, err := parseIoreg(strings.NewReader(out))
		if err != nil || len(ports) != 0 {
			t.Errorf("%q: got %v, %v; want no ports", out, ports, err)
		}
	}
}

func TestIoregRejectsOtherOutput(t *testing.T) {
	for _, out := range []string{
		"<html></html>",
		`<plist><string>x</string></plist>`,
		`<plist><array><dict><key>IOCalloutDevice</key></dict></array></plist>`,
		`<plist><array><dict><key>a</key><string>unclosed`,
	} {
		if _, err := parseIoreg(strings.NewReader(out)); err == nil {
			t.Errorf("%q: no error", out)
		}
	}
}
