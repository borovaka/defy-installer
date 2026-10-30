// SPDX-License-Identifier: MIT

package usbserial

import (
	"cmp"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ioregKeys are the properties listing reads; parseIoreg skips the rest.
var ioregKeys = map[string]bool{
	"IOCalloutDevice":        true,
	"idVendor":               true,
	"idProduct":              true,
	"locationID":             true,
	"bInterfaceNumber":       true,
	"USB Serial Number":      true,
	"kUSBSerialNumberString": true,
	"USB Product Name":       true,
	"kUSBProductString":      true,
}

// entry is a registry entry with the properties in ioregKeys.
type entry struct {
	props    map[string]string
	children []*entry
}

// parseIoreg reads the XML property list ioreg prints for ioregArgs, in
// list_darwin.go.
func parseIoreg(r io.Reader) ([]Port, error) {
	d := xml.NewDecoder(r)
	plist, err := next(d)
	if errors.Is(err, io.EOF) {
		return nil, nil // no serial ports at all
	}
	if err != nil {
		return nil, fmt.Errorf("ioreg: %w", err)
	}
	if plist == nil || plist.Name.Local != "plist" {
		return nil, errors.New("ioreg: output is not a property list")
	}
	top, err := next(d)
	if err != nil {
		return nil, fmt.Errorf("ioreg: %w", err)
	}
	if top == nil {
		return nil, nil
	}
	roots, err := decodeEntries(d, *top)
	if err != nil {
		return nil, fmt.Errorf("ioreg: %w", err)
	}
	var ports []Port
	seen := map[string]bool{}
	for _, root := range roots {
		for _, port := range appendPorts(nil, root, nil) {
			if !seen[port.Path] {
				seen[port.Path] = true
				ports = append(ports, port)
			}
		}
	}
	return ports, nil
}

// next returns the next start element, or nil at the end of the enclosing
// element.
func next(d *xml.Decoder) (*xml.StartElement, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return &t, nil
		case xml.EndElement:
			return nil, nil
		}
	}
}

// decodeEntries reads the dict, or array of dicts, that start opens.
func decodeEntries(d *xml.Decoder, start xml.StartElement) ([]*entry, error) {
	switch start.Name.Local {
	case "dict":
		e, err := decodeDict(d)
		if err != nil {
			return nil, err
		}
		return []*entry{e}, nil
	case "array":
		var entries []*entry
		for {
			el, err := next(d)
			if err != nil {
				return nil, err
			}
			if el == nil {
				return entries, nil
			}
			if el.Name.Local != "dict" {
				return nil, fmt.Errorf("<%s> in a list of registry entries", el.Name.Local)
			}
			e, err := decodeDict(d)
			if err != nil {
				return nil, err
			}
			entries = append(entries, e)
		}
	}
	return nil, fmt.Errorf("<%s> where registry entries belong", start.Name.Local)
}

// decodeDict reads a dict whose start element has been read.
func decodeDict(d *xml.Decoder) (*entry, error) {
	e := &entry{props: map[string]string{}}
	for {
		k, err := next(d)
		if err != nil {
			return nil, err
		}
		if k == nil {
			return e, nil
		}
		if k.Name.Local != "key" {
			return nil, fmt.Errorf("<%s> where a key belongs", k.Name.Local)
		}
		var key string
		if err := d.DecodeElement(&key, k); err != nil {
			return nil, err
		}
		v, err := next(d)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, fmt.Errorf("key %q has no value", key)
		}
		switch {
		case key == "IORegistryEntryChildren":
			if e.children, err = decodeEntries(d, *v); err != nil {
				return nil, err
			}
		case ioregKeys[key]:
			var text string
			if err := d.DecodeElement(&text, v); err != nil {
				return nil, err
			}
			e.props[key] = strings.TrimSpace(text)
		default:
			if err := d.Skip(); err != nil {
				return nil, err
			}
		}
	}
}

// appendPorts appends the USB serial ports in the tree under e; device is
// the nearest USB device above e, if any.
func appendPorts(ports []Port, e, device *entry) []Port {
	if isUSBDevice(e) {
		device = e
	}
	if path := e.props["IOCalloutDevice"]; path != "" && device != nil {
		if port, ok := usbPort(path, device); ok {
			ports = append(ports, port)
		}
	}
	for _, child := range e.children {
		ports = appendPorts(ports, child, device)
	}
	return ports
}

// isUSBDevice reports whether e is a USB device. Its interfaces carry the
// same IDs and location but also an interface number; the drivers below
// them carry the IDs without a location.
func isUSBDevice(e *entry) bool {
	p := e.props
	return p["idVendor"] != "" && p["idProduct"] != "" && p["locationID"] != "" && p["bInterfaceNumber"] == ""
}

func usbPort(path string, device *entry) (Port, bool) {
	p := device.props
	vendor, err1 := strconv.ParseUint(p["idVendor"], 10, 16)
	product, err2 := strconv.ParseUint(p["idProduct"], 10, 16)
	location, err3 := strconv.ParseUint(p["locationID"], 10, 32)
	if err := errors.Join(err1, err2, err3); err != nil {
		return Port{}, false
	}
	return Port{
		Path:     path,
		ID:       USBID{uint16(vendor), uint16(product)},
		Serial:   cmp.Or(p["USB Serial Number"], p["kUSBSerialNumberString"]),
		Product:  cmp.Or(p["USB Product Name"], p["kUSBProductString"]),
		Location: fmt.Sprintf("0x%08x", location),
	}, true
}
