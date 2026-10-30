// SPDX-License-Identifier: MIT

package usbserial

import (
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// listSysfs finds the USB serial ports in a sysfs tree. A tty is one when
// its device is a USB interface, or sits under one as a usb-serial port
// does; the interface's parent is the USB device.
func listSysfs(sys fs.FS) ([]Port, error) {
	ttys, err := fs.ReadDir(sys, "class/tty")
	if err != nil {
		return nil, err
	}
	var ports []Port
	for _, tty := range ttys {
		dir, err := resolve(sys, "class/tty/"+tty.Name())
		if err != nil {
			continue
		}
		// Virtual terminals have no device.
		device, err := resolve(sys, dir+"/device")
		if err != nil {
			continue
		}
		usb, ok := usbDevice(sys, device)
		if !ok {
			continue
		}
		vendor, err1 := strconv.ParseUint(attribute(sys, usb, "idVendor"), 16, 16)
		product, err2 := strconv.ParseUint(attribute(sys, usb, "idProduct"), 16, 16)
		if err1 != nil || err2 != nil {
			continue
		}
		ports = append(ports, Port{
			Path:    "/dev/" + tty.Name(),
			ID:      USBID{uint16(vendor), uint16(product)},
			Serial:  attribute(sys, usb, "serial"),
			Product: attribute(sys, usb, "product"),
			// The device's directory is named after its bus and port
			// path, such as 1-14 or 3-2.1.
			Location: path.Base(usb),
		})
	}
	return ports, nil
}

// resolve follows the symlink name to a path inside sys.
func resolve(sys fs.FS, name string) (string, error) {
	target, err := fs.ReadLink(sys, name)
	if err != nil {
		return "", err
	}
	resolved := path.Join(path.Dir(name), target)
	if path.IsAbs(target) || !fs.ValidPath(resolved) {
		return "", fmt.Errorf("%s: link leads outside sysfs", name)
	}
	return resolved, nil
}

// usbDevice returns the USB device that dir, a tty's device, belongs to.
func usbDevice(sys fs.FS, dir string) (string, bool) {
	for range 3 {
		if exists(sys, dir+"/bInterfaceNumber") {
			parent := path.Dir(dir)
			return parent, exists(sys, parent+"/idVendor")
		}
		dir = path.Dir(dir)
	}
	return "", false
}

func exists(sys fs.FS, name string) bool {
	_, err := fs.Stat(sys, name)
	return err == nil
}

// attribute reads a sysfs attribute; the kernel leaves out the files for
// strings a device does not have.
func attribute(sys fs.FS, dir, name string) string {
	data, err := fs.ReadFile(sys, dir+"/"+name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
