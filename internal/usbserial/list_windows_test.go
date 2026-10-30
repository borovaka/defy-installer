// SPDX-License-Identifier: MIT

package usbserial

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestDevNodeProperties reads, from every USB device on this machine, the
// properties describe reads from a serial port's device. The instance IDs
// must match what SetupAPI reports.
func TestDevNodeProperties(t *testing.T) {
	onlyInCI(t)
	set, err := windows.SetupDiGetClassDevsEx(nil, "USB", 0, windows.DIGCF_ALLCLASSES|windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	devices := 0
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		devices++
		node := uint32(data.DevInst)
		id, err := stringProperty(node, &keyInstanceID)
		if err != nil {
			t.Errorf("device %d: %v", i, err)
			continue
		}
		if want, err := set.DeviceInstanceID(data); err != nil || !strings.EqualFold(id, want) {
			t.Errorf("instance ID %q, SetupAPI says %q (%v)", id, want, err)
		}
	}
	if devices == 0 {
		t.Skip("no USB devices")
	}
}
