// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/focus"
	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// RestoreKeyboard puts Dygma's 2.3.0 firmware back on the whole keyboard:
// the Neuron's firmware and S140, then the keyscanner on both halves,
// written through Dygma's Neuron firmware as Bazecor does. The ZMK half
// updater cannot write Dygma's keyscanner, so the Neuron goes first.
//
// A Neuron that already runs Dygma's firmware is left as it is, so a run
// that stopped after the Neuron goes on with the halves.
func RestoreKeyboard(ctx context.Context, env Env, n Neuron, stock *release.Stock) error {
	port, ok, err := runningStock(ctx, env, n)
	if err != nil {
		return err
	}
	if ok {
		env.say("The Neuron already runs Dygma's firmware at %s; going on with the halves.", port.Path)
	} else {
		if port, err = restoreStock(ctx, env, n, stock.Neuron, nil); err != nil {
			return err
		}
		if port.Path == "" {
			return fmt.Errorf("%w within %v, so the halves were not written. %s, then run this again: it goes on "+
				"with the halves", errNotReturned, n.Timeout, replugNoButton)
		}
	}
	env.say("Writing Dygma's keyscanner to both halves through Dygma's Neuron firmware. Its firmware update " +
		"resets both halves; if the Neuron asks for it, press and release the left half's top-left Esc key.")
	if err := restoreHalves(ctx, env, port, n.Timeout, stock.Keyscanner, focus.ReadyWait{}, nil); err != nil {
		return err
	}
	env.say("Both halves run Dygma's 2.3.0 keyscanner again. %s", focus.ReconnectGuidance)
	env.say("Each half reinstalls Dygma's radio firmware as it starts; if wireless typing does not come back, " +
		"unplug that half's cable and switch it off and on.")
	env.say("Dygma's settings at 0x%05x..0x%05x, which hold the layout, were not written, so Dygma's firmware "+
		"finds them as it left them. It erases them when it is asked for its bootloader, as install and advanced "+
		"neuron write ask it; after that, restore the layout from your Bazecor backup.", firmware.NeuronSettingsEnd,
		firmware.NeuronDygmaSettingsEnd)
	return nil
}

// restoreHalves writes Dygma's keyscanner to both halves through Dygma's
// Neuron firmware at port, as Bazecor does, then closes the port. w is the
// wait while Dygma's firmware waits for the left half's Esc key. bar, if
// set, is called for every page.
func restoreHalves(ctx context.Context, env Env, port usbserial.Port, timeout time.Duration, keyscanner *firmware.HalfImage,
	w focus.ReadyWait, bar firmware.HalfProgress) error {
	conn, err := openWhenReady(ctx, env, port.Path, timeout)
	if err != nil {
		return err
	}
	err = focus.Restore(ctx, conn, keyscanner, func(side firmware.Side, done, total int) {
		if bar != nil {
			bar(side, done, total)
		}
		switch {
		case done == total:
			env.say("%v half: written, validated and finished.", side)
		case done%halfDetailStep == 0:
			env.say("%v half: %d of %d bytes written.", side, done, total)
		}
	}, w)
	if closeErr := conn.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("closing %s: %w", port.Path, closeErr))
	}
	return err
}

// runningStock returns the port of a Neuron that already runs Dygma's
// firmware: the one at n.Port, or with no n.Port the only Neuron
// connected. With n.Serial set, the port must have that USB serial number.
func runningStock(ctx context.Context, env Env, n Neuron) (usbserial.Port, bool, error) {
	ports, err := env.List(ctx)
	if err != nil {
		return usbserial.Port{}, false, err
	}
	if n.Port != "" {
		p, err := usbserial.Find(ports, n.Port)
		if err != nil {
			return usbserial.Port{}, false, err
		}
		if n.Serial != "" && p.Serial != n.Serial {
			return usbserial.Port{}, false, fmt.Errorf("%s is not the Neuron with USB serial number %s; nothing was "+
				"sent", n.Port, n.Serial)
		}
		return p, neuron.ModeOf(p) == neuron.Stock, nil
	}
	devices := neurons(ports)
	stock := slices.DeleteFunc(inMode(devices, neuron.Stock), func(d device) bool {
		return n.Serial != "" && d.ports[0].Serial != n.Serial
	})
	switch {
	case len(stock) > 1:
		return usbserial.Port{}, false, errors.New("more than one Neuron runs Dygma's firmware; choose one with --port")
	case len(stock) == 1 && len(devices) == 1:
		return stock[0].ports[0], true, nil
	}
	return usbserial.Port{}, false, nil
}
