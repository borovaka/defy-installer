// SPDX-License-Identifier: MIT

package neuron

import (
	"testing"

	"github.com/borovaka/defy-installer/internal/usbserial"
)

func TestModeOf(t *testing.T) {
	tests := []struct {
		port usbserial.Port
		want Mode
	}{
		{usbserial.Port{ID: ZMKID, Product: "Defy"}, ZMK},
		// Another keyboard running ZMK.
		{usbserial.Port{ID: ZMKID, Product: "Corne"}, NotNeuron},
		{usbserial.Port{ID: StockID}, Stock},
		{usbserial.Port{ID: BootloaderID}, Bootloader},
		{usbserial.Port{ID: LoaderID, Product: "Defy UF2 loader", Serial: "0123456789ABCDEF"}, Loader},
		// Zephyr's samples share the loader's testing ID.
		{usbserial.Port{ID: LoaderID, Product: "USBD MSC sample"}, NotNeuron},
		{usbserial.Port{ID: usbserial.USBID{Vendor: 0x35ef, Product: 0x0011}}, NotNeuron},
	}
	for _, tt := range tests {
		if got := ModeOf(tt.port); got != tt.want {
			t.Errorf("%v %q: got %v, want %v", tt.port.ID, tt.port.Product, got, tt.want)
		}
	}
}

func TestIsZMK(t *testing.T) {
	zmk := usbserial.Port{ID: ZMKID, Product: "Defy", Serial: "0123456789ABCDEF"}
	if !IsZMK(zmk) {
		t.Error("a ZMK Neuron is not ZMK")
	}
	for _, serial := range []string{"", "  ", "0000000000000000"} {
		p := zmk
		p.Serial = serial
		if IsZMK(p) {
			t.Errorf("serial %q passed", serial)
		}
	}
	if IsZMK(usbserial.Port{ID: BootloaderID, Serial: "0123456789ABCDEF"}) {
		t.Error("the bootloader passed")
	}
	if IsZMK(usbserial.Port{ID: LoaderID, Product: "Defy", Serial: "0123456789ABCDEF"}) {
		t.Error("the UF2 loader passed")
	}
}
