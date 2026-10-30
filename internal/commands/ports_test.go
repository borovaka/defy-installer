// SPDX-License-Identifier: MIT

package commands

import (
	"strings"
	"testing"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

var ports = []usbserial.Port{
	{Path: "/dev/ttyACM0", ID: neuron.ZMKID, Serial: "0123456789ABCDEF", Product: "Defy", Location: "1-14"},
	{Path: "/dev/ttyUSB0", ID: usbserial.USBID{Vendor: 0x0403, Product: 0x6001}, Product: "FT232R USB UART", Location: "1-2.1"},
	{Path: "/dev/ttyACM1", ID: neuron.LoaderID, Serial: "0123456789ABCDEF", Product: "Defy UF2 loader", Location: "1-14"},
}

func TestWritePorts(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := writePorts(&out, ports); err != nil {
		t.Fatal(err)
	}
	want := `PORT          USB ID     NEURON      PRODUCT          SERIAL            LOCATION
/dev/ttyACM0  1d50:615e  ZMK         Defy             0123456789ABCDEF  1-14
/dev/ttyUSB0  0403:6001  -           FT232R USB UART  -                 1-2.1
/dev/ttyACM1  2fe3:0100  UF2 loader  Defy UF2 loader  0123456789ABCDEF  1-14
`
	if out.String() != want {
		t.Errorf("got\n%s\nwant\n%s", out.String(), want)
	}
	out.Reset()
	if err := writePorts(&out, nil); err != nil || out.String() != "No USB serial ports.\n" {
		t.Errorf("got %q, %v", out.String(), err)
	}
}

func TestWritePortsJSON(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := writePortsJSON(&out, ports); err != nil {
		t.Fatal(err)
	}
	want := `[
  {
    "path": "/dev/ttyACM0",
    "usb_id": "1d50:615e",
    "neuron": "ZMK",
    "product": "Defy",
    "serial": "0123456789ABCDEF",
    "location": "1-14"
  },
  {
    "path": "/dev/ttyUSB0",
    "usb_id": "0403:6001",
    "product": "FT232R USB UART",
    "location": "1-2.1"
  },
  {
    "path": "/dev/ttyACM1",
    "usb_id": "2fe3:0100",
    "neuron": "UF2 loader",
    "product": "Defy UF2 loader",
    "serial": "0123456789ABCDEF",
    "location": "1-14"
  }
]
`
	if out.String() != want {
		t.Errorf("got\n%s\nwant\n%s", out.String(), want)
	}
}
