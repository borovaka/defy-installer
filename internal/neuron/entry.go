// SPDX-License-Identifier: MIT

package neuron

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/borovaka/defy-installer/internal/usbserial"
)

// pollInterval is how often the ports are listed while waiting for the
// Neuron to enumerate again.
const pollInterval = 100 * time.Millisecond

// ErrOnePort reports a Neuron running ZMK that lists only one of its two
// ports, as it does for a moment after it enumerates. Nothing was sent to
// enter the bootloader.
var ErrOnePort = errors.New("the Neuron shows only one of its two ports")

// TouchPort is the part of an open serial port that Touch uses;
// usbserial.Conn is one.
type TouchPort interface {
	SetDTR(on bool) error
	SetBaudRate(baud int) error
	Close() error
}

// Touch asks ZMK on the Neuron for its bootloader through p, the Studio
// port opened at 115200 baud, or the UF2 loader through its own port: it
// raises DTR, changes to 1200 baud, lowers DTR and closes the port. It
// sends and reads no data. It closes p even when a step fails, and tries
// no step again.
func Touch(p TouchPort) error {
	err := p.SetDTR(true)
	if err == nil {
		err = p.SetBaudRate(1200)
	}
	if err == nil {
		err = p.SetDTR(false)
	}
	return errors.Join(err, p.Close())
}

// Entry finds the Neuron's bootloader, enters it when asked, and sees the
// firmware come back after a write. The Neuron enumerates again in each
// mode, so Entry follows it by its USB location.
type Entry struct {
	// List lists the USB serial ports.
	List usbserial.Lister
	// IsUpdater reports whether a ZMK Neuron's port is the half updater's
	// rather than Studio's.
	IsUpdater func(context.Context, usbserial.Port) (bool, error)
	// Touch opens the port at path at 115200 baud and touches it; see
	// Touch.
	Touch func(path string) error
	// Ask, if set, asks Dygma's firmware on a port for its bootloader.
	// Without it, a Neuron running Dygma's firmware needs its button.
	Ask func(context.Context, usbserial.Port) error
	// Timeout is how long the bootloader, or the firmware after it, may
	// take to appear.
	Timeout time.Duration
}

// find returns the one port at path, with the button's guidance when there
// is none.
func find(ports []usbserial.Port, path string) (usbserial.Port, error) {
	p, err := usbserial.Find(ports, path)
	if err != nil && !slices.ContainsFunc(ports, func(p usbserial.Port) bool { return p.Path == path }) {
		return p, fmt.Errorf("%w. %s", err, EntryGuidance)
	}
	return p, err
}

// selected lists the ports and returns them with the one at path, whose
// USB serial number must be serial unless serial is empty.
func (e *Entry) selected(ctx context.Context, path, serial string) ([]usbserial.Port, usbserial.Port, error) {
	ports, err := e.List(ctx)
	if err != nil {
		return nil, usbserial.Port{}, err
	}
	target, err := find(ports, path)
	if err != nil {
		return nil, usbserial.Port{}, err
	}
	if serial != "" && target.Serial != serial {
		return nil, usbserial.Port{}, fmt.Errorf("%s is not the Neuron with USB serial number %s; nothing was sent",
			path, serial)
	}
	return ports, target, nil
}

// notBootloader says why target is not the bootloader's port.
func notBootloader(target usbserial.Port) error {
	if target.ID == StockID {
		return fmt.Errorf("%s is the Neuron running Dygma's firmware, not its bootloader. %s", target.Path, EntryGuidance)
	}
	if ModeOf(target) == Loader {
		return fmt.Errorf("%s is the Neuron's UF2 loader, not its bootloader, so nothing was sent. Unplug the "+
			"Neuron and plug it back in to start ZMK. If the drive comes back, copy the UF2 file again, or run "+
			"defy-installer update", target.Path)
	}
	return fmt.Errorf("%s is not the Neuron's bootloader (USB %v). %s", target.Path, target.ID, EntryGuidance)
}

// noLocation says that target cannot be followed into the bootloader.
func noLocation(target usbserial.Port) error {
	return fmt.Errorf("this computer does not say which USB socket %s is in, so the Neuron cannot be found again "+
		"once it restarts into its bootloader. Enter the bootloader with the button instead. %s", target.Path, EntryGuidance)
}

// FindBootloader returns the port at path, which must be the Neuron's
// bootloader's, with the USB serial number serial unless serial is empty.
// It enters nothing.
func (e *Entry) FindBootloader(ctx context.Context, path, serial string) (usbserial.Port, error) {
	_, target, err := e.selected(ctx, path, serial)
	if err != nil {
		return usbserial.Port{}, err
	}
	if target.ID != BootloaderID {
		return usbserial.Port{}, notBootloader(target)
	}
	return target, nil
}

// EnterBootloader returns the port of the Neuron's bootloader, given the
// path of that port, of the Studio port of a Neuron running ZMK, or of the
// UF2 loader's port. The port must have the USB serial number serial,
// unless serial is empty.
//
// From Studio it touches the port, waits for a new bootloader port at the
// same USB location and for the firmware's to go, and returns the Studio
// port as from. It refuses, before the touch, the half updater's port, a
// Neuron that lists only one of its two ports, a port without a location,
// and a location that already has a bootloader. From the UF2 loader's one
// port it does the same, without the checks of ZMK's two ports, and
// returns the loader's port as from. From a port of Dygma's firmware it
// does the same with Ask in place of the touch, when Ask is set, and
// returns no from.
func (e *Entry) EnterBootloader(ctx context.Context, path, serial string) (boot, from usbserial.Port, err error) {
	before, target, err := e.selected(ctx, path, serial)
	if err != nil {
		return usbserial.Port{}, usbserial.Port{}, err
	}
	if target.ID == BootloaderID {
		return target, usbserial.Port{}, nil
	}
	if ModeOf(target) == Loader {
		boot, err := e.enter(ctx, before, target, func() error { return e.Touch(target.Path) })
		if err != nil {
			return usbserial.Port{}, usbserial.Port{}, err
		}
		return boot, target, nil
	}
	if target.ID == StockID && e.Ask != nil {
		boot, err := e.enter(ctx, before, target, func() error { return e.Ask(ctx, target) })
		return boot, usbserial.Port{}, err
	}
	if !IsZMK(target) {
		return usbserial.Port{}, usbserial.Port{}, notBootloader(target)
	}
	if target.Location == "" {
		return usbserial.Port{}, usbserial.Port{}, noLocation(target)
	}
	var siblings []usbserial.Port
	for _, p := range before {
		if IsZMK(p) && p.Location == target.Location && p.Serial == target.Serial {
			siblings = append(siblings, p)
		}
	}
	found, ok, err := e.selectStudio(ctx, siblings)
	switch {
	case err != nil:
		return usbserial.Port{}, usbserial.Port{}, err
	case !ok:
		return usbserial.Port{}, usbserial.Port{}, fmt.Errorf("%w, %s, so it is not yet clear which one starts its "+
			"bootloader. Wait a few seconds and try again; nothing was sent", ErrOnePort, path)
	case found != target:
		return usbserial.Port{}, usbserial.Port{}, fmt.Errorf("%s is the Neuron's port for writing the halves, "+
			"which cannot start its bootloader; use %s. Nothing was sent", path, found.Path)
	}
	boot, err = e.enter(ctx, before, target, func() error { return e.Touch(target.Path) })
	if err != nil {
		return usbserial.Port{}, usbserial.Port{}, err
	}
	return boot, target, nil
}

// enter asks the firmware on target for the bootloader with request, then
// waits for a new bootloader port at target's USB location and for the
// firmware's ports to go. It refuses, before the request, a port without a
// location and a location that already has a bootloader.
func (e *Entry) enter(ctx context.Context, before []usbserial.Port, target usbserial.Port, request func() error) (usbserial.Port, error) {
	if target.Location == "" {
		return usbserial.Port{}, noLocation(target)
	}
	if slices.ContainsFunc(before, func(p usbserial.Port) bool {
		return p.Location == target.Location && p.ID == BootloaderID
	}) {
		return usbserial.Port{}, fmt.Errorf("a Neuron bootloader is already listed at the USB socket of %s, so a new "+
			"one could not be told from it; nothing was sent. Unplug the Neuron, plug it back in and try again",
			target.Path)
	}
	if err := usbserial.Recheck(ctx, target, e.List); err != nil {
		return usbserial.Port{}, err
	}
	if err := ctx.Err(); err != nil {
		return usbserial.Port{}, err
	}
	if err := request(); err != nil {
		return usbserial.Port{}, fmt.Errorf("requesting the bootloader through %s: %w", target.Path, err)
	}
	var boot usbserial.Port
	ok, err := poll(ctx, e.Timeout, func() (bool, error) {
		ports, err := e.List(ctx)
		if err != nil {
			return false, err
		}
		var matches []usbserial.Port
		running := false
		for _, p := range ports {
			if p.Location != target.Location {
				continue
			}
			if p.ID == BootloaderID && !slices.Contains(before, p) {
				matches = append(matches, p)
			}
			running = running || p.ID == target.ID
		}
		if len(matches) > 1 {
			return false, errors.New("more than one bootloader port appeared at the Neuron's USB socket, so it is not " +
				"clear which one is the Neuron's; nothing was written. Unplug the Neuron, plug it back in and try again")
		}
		if len(matches) == 1 && !running {
			boot = matches[0]
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return usbserial.Port{}, err
	}
	if !ok {
		return usbserial.Port{}, fmt.Errorf("the Neuron did not enter its bootloader within %v. If a halves write "+
			"is running, let it finish. Otherwise enter the bootloader with the button. %s", e.Timeout, EntryGuidance)
	}
	return boot, nil
}

// WaitForFirmware waits for the Neuron's firmware to enumerate at boot's
// USB location once the bootloader's port has gone, and returns its port:
// for ZMK, the Studio port, or the log port of a logging build, once both
// of ZMK's ports are listed. want is the firmware's USB ID, or either
// firmware's when zero. serial is ZMK's USB serial number, or any when
// empty; Dygma's firmware has a serial number of its own, so serial does
// not apply to it.
//
// It reports false when no such port appears within e.Timeout, or at once
// when boot has no location to follow. A Neuron that has just enumerated
// can list a port before the port opens, as Windows does, so a failure to
// tell Studio from the updater is tried again until the timeout, and only
// then returned.
func (e *Entry) WaitForFirmware(ctx context.Context, boot usbserial.Port, want usbserial.USBID, serial string) (usbserial.Port, bool, error) {
	return e.wait(ctx, boot, want, serial, false)
}

// WaitForStart is WaitForFirmware for ZMK, after S# from a bootloader
// entered from the UF2 loader. What starts is the loader, which starts ZMK
// when ZMK's vector table is valid, and shows its drive again otherwise or
// after three starts that ZMK did not confirm. So it also returns the
// loader's port, as soon as it is listed at boot's USB location. serial is
// ZMK's USB serial number, which the loader shares, or any when empty.
func (e *Entry) WaitForStart(ctx context.Context, boot usbserial.Port, serial string) (usbserial.Port, bool, error) {
	return e.wait(ctx, boot, ZMKID, serial, true)
}

// wait is WaitForFirmware, and with loader set WaitForStart.
func (e *Entry) wait(ctx context.Context, boot usbserial.Port, want usbserial.USBID, serial string, loader bool) (usbserial.Port, bool, error) {
	if boot.Location == "" {
		return usbserial.Port{}, false, nil
	}
	var found usbserial.Port
	var studioErr error
	ok, err := poll(ctx, e.Timeout, func() (bool, error) {
		studioErr = nil
		ports, err := e.List(ctx)
		if err != nil {
			return false, err
		}
		var matches []usbserial.Port
		for _, p := range ports {
			if p.Location != boot.Location {
				continue
			}
			if p.ID == BootloaderID {
				return false, nil
			}
			if loader && ModeOf(p) == Loader && (serial == "" || p.Serial == serial) {
				found = p
				return true, nil
			}
			if (IsZMK(p) || p.ID == StockID) && (want == usbserial.USBID{} || p.ID == want) &&
				(serial == "" || p.ID == StockID || p.Serial == serial) {
				matches = append(matches, p)
			}
		}
		if len(matches) == 0 {
			return false, nil
		}
		p, ok, err := e.selectStudio(ctx, matches)
		found, studioErr = p, err
		return ok && err == nil, nil
	})
	if err == nil && !ok {
		err = studioErr
	}
	if err != nil || !ok {
		return usbserial.Port{}, false, err
	}
	return found, true, nil
}

// Studio returns the Studio port among the ports of one Neuron running
// ZMK, telling it from the half updater's port by asking each for HELLO.
func (e *Entry) Studio(ctx context.Context, ports []usbserial.Port) (usbserial.Port, error) {
	p, ok, err := e.selectStudio(ctx, ports)
	if err != nil {
		return usbserial.Port{}, err
	}
	if !ok {
		return usbserial.Port{}, fmt.Errorf("%w; wait a few seconds and try again", ErrOnePort)
	}
	return p, nil
}

// selectStudio returns, among the ports of one Neuron's firmware at one USB
// location, the one that is not the half updater's: the only port of
// Dygma's firmware, or of ZMK's two, Studio's or a logging build's log
// port. It tells ZMK's apart only once both are listed, since a Neuron
// that has just enumerated can list one before the other, and reports
// false while one is there alone.
func (e *Entry) selectStudio(ctx context.Context, ports []usbserial.Port) (usbserial.Port, bool, error) {
	switch {
	case len(ports) == 1 && !IsZMK(ports[0]):
		return ports[0], true, nil
	case len(ports) == 1:
		return usbserial.Port{}, false, nil
	case len(ports) != 2 || !IsZMK(ports[0]) || !IsZMK(ports[1]) || !sameDevice(ports[0], ports[1]):
		return usbserial.Port{}, false, fmt.Errorf("the ports at the Neuron's USB socket, %s, are not the two ports "+
			"of one Neuron running ZMK, so it is not clear which one to use. Unplug the Neuron, plug it back in and "+
			"try again", paths(ports))
	}
	var studio usbserial.Port
	updaters := 0
	for _, p := range ports {
		updater, err := e.IsUpdater(ctx, p)
		if err != nil {
			return usbserial.Port{}, false, err
		}
		if updater {
			updaters++
		} else {
			studio = p
		}
	}
	if updaters != 1 {
		which := "neither"
		if updaters == 2 {
			which = "both"
		}
		return usbserial.Port{}, false, fmt.Errorf("%s of the Neuron's ports, %s, answered as its port for writing "+
			"the halves, so they could not be told apart. Unplug the Neuron, plug it back in and try again",
			which, paths(ports))
	}
	return studio, true, nil
}

// paths lists the ports' paths for a message.
func paths(ports []usbserial.Port) string {
	list := make([]string, len(ports))
	for i, p := range ports {
		list[i] = p.Path
	}
	return strings.Join(list, ", ")
}

// sameDevice reports whether a and b are ports of one USB device.
func sameDevice(a, b usbserial.Port) bool {
	return a.ID == b.ID && a.Serial == b.Serial && a.Location == b.Location && a.Product == b.Product
}

// poll calls try every pollInterval until it reports done, it fails, or
// timeout passes.
func poll(ctx context.Context, timeout time.Duration, try func() (bool, error)) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if done, err := try(); done || err != nil {
			return done, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(min(pollInterval, time.Until(deadline))):
		}
	}
	return false, nil
}
