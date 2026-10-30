// SPDX-License-Identifier: MIT

package usbserial

import (
	"io/fs"
	"os"
	"slices"
	"testing"
	"testing/fstest"
)

func link(target string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(target), Mode: fs.ModeSymlink}
}

func file(content string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(content + "\n")}
}

// sysfs is laid out as the kernel lays out /sys, with only what listing
// reads.
func sysfs() fstest.MapFS {
	const usb = "devices/pci0000:00/0000:00:14.0/usb1"
	return fstest.MapFS{
		// The Neuron running ZMK: two CDC ACM functions, whose ttys hang
		// off their control interfaces.
		"class/tty/ttyACM0":                       link("../../" + usb + "/1-14/1-14:1.0/tty/ttyACM0"),
		usb + "/1-14/1-14:1.0/tty/ttyACM0/device": link("../../../1-14:1.0"),
		usb + "/1-14/1-14:1.0/bInterfaceNumber":   file("00"),
		"class/tty/ttyACM1":                       link("../../" + usb + "/1-14/1-14:1.3/tty/ttyACM1"),
		usb + "/1-14/1-14:1.3/tty/ttyACM1/device": link("../../../1-14:1.3"),
		usb + "/1-14/1-14:1.3/bInterfaceNumber":   file("03"),
		usb + "/1-14/idVendor":                    file("1d50"),
		usb + "/1-14/idProduct":                   file("615e"),
		usb + "/1-14/serial":                      file("0123456789ABCDEF"),
		usb + "/1-14/product":                     file("Defy"),
		// A USB serial adapter behind a hub, without a serial number. Its
		// tty hangs off a usb-serial port below the interface.
		"class/tty/ttyUSB0": link("../../" + usb + "/1-2/1-2.1/1-2.1:1.0/ttyUSB0/tty/ttyUSB0"),
		usb + "/1-2/1-2.1/1-2.1:1.0/ttyUSB0/tty/ttyUSB0/device": link("../../../ttyUSB0"),
		usb + "/1-2/1-2.1/1-2.1:1.0/bInterfaceNumber":           file("00"),
		usb + "/1-2/1-2.1/idVendor":                             file("0403"),
		usb + "/1-2/1-2.1/idProduct":                            file("6001"),
		usb + "/1-2/1-2.1/product":                              file("FT232R USB UART"),
		// A built-in UART, a virtual terminal and a link out of sysfs.
		"class/tty/ttyS0": link("../../devices/platform/serial8250/serial8250:0/serial8250:0.0/tty/ttyS0"),
		"devices/platform/serial8250/serial8250:0/serial8250:0.0/tty/ttyS0/device": link("../../../serial8250:0.0"),
		"class/tty/tty0":               link("../../devices/virtual/tty/tty0"),
		"devices/virtual/tty/tty0/dev": file("4:0"),
		"class/tty/ttyX":               link("../../../../tmp/ttyX"),
	}
}

func TestSysfsListsOnlyUSBPorts(t *testing.T) {
	got, err := listSysfs(sysfs())
	if err != nil {
		t.Fatal(err)
	}
	neuron := Port{ID: USBID{0x1d50, 0x615e}, Serial: "0123456789ABCDEF", Product: "Defy", Location: "1-14"}
	first, second := neuron, neuron
	first.Path, second.Path = "/dev/ttyACM0", "/dev/ttyACM1"
	adapter := Port{Path: "/dev/ttyUSB0", ID: USBID{0x0403, 0x6001}, Product: "FT232R USB UART", Location: "1-2.1"}
	if want := []Port{first, second, adapter}; !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestSysfsWithoutTTYs(t *testing.T) {
	if _, err := listSysfs(fstest.MapFS{}); err == nil {
		t.Error("no error without class/tty")
	}
}

// TestList lists this machine's ports through the real source: ioreg,
// sysfs or the device manager. It only checks that listing works.
func TestList(t *testing.T) {
	onlyInCI(t)
	if _, err := List(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// onlyInCI skips a test that reads this machine's devices, such as a
// keyboard plugged in, unless it runs in CI, which sets CI. Set CI to run
// it on purpose.
func onlyInCI(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("reads this machine's devices; runs in CI, or with CI set")
	}
}
