// SPDX-License-Identifier: MIT

package usbserial

import (
	"strconv"
	"strings"
)

// instanceID is what a Windows device instance ID says about a USB device,
// such as USB\VID_1D50&PID_615E\0123456789ABCDEF for a device, or
// USB\VID_1D50&PID_615E&MI_00\7&2A8F1C3&0&0000 for one function of a
// composite device.
type instanceID struct {
	id USBID
	// function is set for one function of a composite device; the device
	// itself is its parent.
	function bool
	// serial is the device's serial number. Windows builds the last part
	// of the ID itself, with '&' in it, when a device has no serial number
	// it can use, and for functions.
	serial string
}

func parseInstanceID(s string) (instanceID, bool) {
	parts := strings.Split(s, `\`)
	if len(parts) != 3 || !strings.EqualFold(parts[0], "USB") {
		return instanceID{}, false
	}
	var parsed instanceID
	var vendor, product bool
	for field := range strings.SplitSeq(parts[1], "&") {
		name, value, _ := strings.Cut(field, "_")
		switch strings.ToUpper(name) {
		case "VID":
			v, err := strconv.ParseUint(value, 16, 16)
			parsed.id.Vendor, vendor = uint16(v), err == nil
		case "PID":
			v, err := strconv.ParseUint(value, 16, 16)
			parsed.id.Product, product = uint16(v), err == nil
		case "MI":
			parsed.function = true
		}
	}
	if !vendor || !product {
		return instanceID{}, false
	}
	if !parsed.function && !strings.Contains(parts[2], "&") {
		parsed.serial = parts[2]
	}
	return parsed, true
}
