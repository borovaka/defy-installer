// SPDX-License-Identifier: MIT

package usbserial

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	cfgmgr32               = windows.NewLazySystemDLL("cfgmgr32.dll")
	procGetParent          = cfgmgr32.NewProc("CM_Get_Parent")
	procGetDevNodeProperty = cfgmgr32.NewProc("CM_Get_DevNode_PropertyW")
)

// GUID_DEVCLASS_PORTS, the class usbser.sys puts USB serial ports in.
var portsClass = windows.GUID{Data1: 0x4d36e978, Data2: 0xe325, Data3: 0x11ce,
	Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}

// Device properties from devpkey.h.
var (
	keyInstanceID = windows.DEVPROPKEY{FmtID: windows.DEVPROPGUID{Data1: 0x78c34fc8, Data2: 0x104a, Data3: 0x4aca,
		Data4: [8]byte{0x9e, 0xa4, 0x52, 0x4d, 0x52, 0x99, 0x6e, 0x57}}, PID: 256}
	keyBusReportedDeviceDesc = windows.DEVPROPKEY{FmtID: windows.DEVPROPGUID{Data1: 0x540b947e, Data2: 0x8b40, Data3: 0x45bc,
		Data4: [8]byte{0xa8, 0xa2, 0x6a, 0x0b, 0x89, 0x4c, 0xbd, 0xa2}}, PID: 4}
	keyLocationPaths = windows.DEVPROPKEY{FmtID: windows.DEVPROPGUID{Data1: 0xa45c254e, Data2: 0xdf1c, Data3: 0x4efd,
		Data4: [8]byte{0x80, 0x20, 0x67, 0xd1, 0x46, 0xa8, 0x50, 0xe0}}, PID: 37}
)

const (
	crSuccess     = 0x00
	crBufferSmall = 0x1a
)

func list(context.Context) ([]Port, error) {
	set, err := windows.SetupDiGetClassDevsEx(&portsClass, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil, err
	}
	defer set.Close()
	var ports []Port
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			return ports, nil
		}
		if err != nil {
			return nil, err
		}
		if port, ok := describe(set, data); ok {
			ports = append(ports, port)
		}
	}
}

// describe reads a port's name, and the IDs, strings and location of the
// USB device it belongs to: the port's own node, or for a function of a
// composite device, its parent.
func describe(set windows.DevInfo, data *windows.DevInfoData) (Port, bool) {
	name, err := portName(set, data)
	if err != nil || !strings.HasPrefix(name, "COM") {
		return Port{}, false
	}
	node := uint32(data.DevInst)
	id, err := stringProperty(node, &keyInstanceID)
	if err != nil {
		return Port{}, false
	}
	parsed, ok := parseInstanceID(id)
	if !ok {
		return Port{}, false
	}
	if parsed.function {
		if node, err = parent(node); err != nil {
			return Port{}, false
		}
		id, err := stringProperty(node, &keyInstanceID)
		if err != nil {
			return Port{}, false
		}
		device, ok := parseInstanceID(id)
		if !ok || device.function || device.id != parsed.id {
			return Port{}, false
		}
		parsed = device
	}
	product, _ := stringProperty(node, &keyBusReportedDeviceDesc)
	location, _ := stringProperty(node, &keyLocationPaths)
	return Port{Path: name, ID: parsed.id, Serial: parsed.serial, Product: product, Location: location}, true
}

func portName(set windows.DevInfo, data *windows.DevInfoData) (string, error) {
	h, err := windows.SetupDiOpenDevRegKey(set, data, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DEV, windows.KEY_READ)
	if err != nil {
		return "", err
	}
	key := registry.Key(h)
	defer key.Close()
	name, _, err := key.GetStringValue("PortName")
	return name, err
}

func parent(node uint32) (uint32, error) {
	var p uint32
	if r, _, _ := procGetParent.Call(uintptr(unsafe.Pointer(&p)), uintptr(node), 0); r != crSuccess {
		return 0, fmt.Errorf("CM_Get_Parent: CONFIGRET %#x", r)
	}
	return p, nil
}

// stringProperty reads a string property, or the first string of a string
// list property.
func stringProperty(node uint32, key *windows.DEVPROPKEY) (string, error) {
	var kind, size uint32
	r, _, _ := procGetDevNodeProperty.Call(uintptr(node), uintptr(unsafe.Pointer(key)),
		uintptr(unsafe.Pointer(&kind)), 0, uintptr(unsafe.Pointer(&size)), 0)
	if r != crBufferSmall || size < 2 {
		return "", fmt.Errorf("CM_Get_DevNode_Property: CONFIGRET %#x", r)
	}
	buf := make([]uint16, size/2)
	r, _, _ = procGetDevNodeProperty.Call(uintptr(node), uintptr(unsafe.Pointer(key)),
		uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	if r != crSuccess {
		return "", fmt.Errorf("CM_Get_DevNode_Property: CONFIGRET %#x", r)
	}
	return windows.UTF16ToString(buf), nil
}
