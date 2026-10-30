// SPDX-License-Identifier: MIT

package usbserial

import "testing"

func TestParseInstanceID(t *testing.T) {
	tests := []struct {
		id   string
		want instanceID
		ok   bool
	}{
		{`USB\VID_1D50&PID_615E\0123456789ABCDEF`, instanceID{id: USBID{0x1d50, 0x615e}, serial: "0123456789ABCDEF"}, true},
		{`USB\VID_1D50&PID_615E&MI_00\7&2A8F1C3&0&0000`, instanceID{id: USBID{0x1d50, 0x615e}, function: true}, true},
		{`usb\vid_35ef&pid_0013\0123456789ABCDEF`, instanceID{id: USBID{0x35ef, 0x0013}, serial: "0123456789ABCDEF"}, true},
		// Windows made up the last part: the device has no usable serial.
		{`USB\VID_0403&PID_6001\5&1A2B3C4D&0&2`, instanceID{id: USBID{0x0403, 0x6001}}, true},
		{`FTDIBUS\VID_0403+PID_6001+A6008ISPA\0000`, instanceID{}, false},
		{`USB\VID_1D50\0123456789ABCDEF`, instanceID{}, false},
		{`USB\VID_XYZ&PID_615E\0123456789ABCDEF`, instanceID{}, false},
		{`ACPI\PNP0501\0`, instanceID{}, false},
		{``, instanceID{}, false},
	}
	for _, tt := range tests {
		got, ok := parseInstanceID(tt.id)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: got %+v, %v; want %+v, %v", tt.id, got, ok, tt.want, tt.ok)
		}
	}
}
