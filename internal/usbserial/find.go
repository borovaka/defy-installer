// SPDX-License-Identifier: MIT

package usbserial

import (
	"context"
	"fmt"
)

// Lister lists the USB serial ports; List is one.
type Lister func(context.Context) ([]Port, error)

// Find returns the one port at path among ports.
func Find(ports []Port, path string) (Port, error) {
	var found []Port
	for _, p := range ports {
		if p.Path == path {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return Port{}, fmt.Errorf("no serial port %s", path)
	case 1:
		return found[0], nil
	}
	return Port{}, fmt.Errorf("%s is listed more than once", path)
}

// Recheck lists the ports again, and fails unless target is still there
// with the same USB identity and location. Callers recheck a port just
// before they open it.
func Recheck(ctx context.Context, target Port, list Lister) error {
	ports, err := list(ctx)
	if err != nil {
		return err
	}
	now, err := Find(ports, target.Path)
	if err != nil {
		return err
	}
	if now != target {
		return fmt.Errorf("%s: its USB identity changed", target.Path)
	}
	return nil
}
