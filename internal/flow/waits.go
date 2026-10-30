// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/focus"
	"github.com/borovaka/defy-installer/internal/updater"
)

// The waits of the user's commands.
const (
	// escLimit is how long the installer waits for the Esc press Dygma's
	// firmware asks for, before it says the key was not seen.
	escLimit = 2 * time.Minute
	// halvesWait is how long the installer waits for the halves to report
	// what they run: after a key press, or after a write.
	halvesWait = 30 * time.Second
	// wirelessWait is how long the halves may take to connect over
	// Bluetooth before the installer says how to restart them.
	wirelessWait = 2 * time.Minute
	// pollInterval is how often the installer asks the Neuron while it
	// waits for the halves.
	pollInterval = time.Second
)

// notReported follows a halves write whose images the halves have not
// reported.
const notReported = "  The halves have not reported their firmware yet. They may still be installing their radio " +
	"firmware, which takes about half a minute."

// escWait returns the wait for the left half's Esc key, which Dygma's
// firmware asks for before an update when that half is cabled and runs
// Dygma's firmware. It asks for the key once Dygma's firmware first
// reports not ready; after escLimit it says the key was not seen and waits
// for Enter.
func (r *run) escWait() focus.ReadyWait {
	return focus.ReadyWait{
		Waiting: func() {
			r.ui.say("  Press the Esc key on the left half once, the top-left key. Dygma's firmware asks for it " +
				"before an update.")
		},
		Limit: escLimit,
		Expired: func(ctx context.Context) error {
			r.ui.say("  Dygma's firmware did not see the Esc key. Press Esc on the left half, then press Enter.")
			return r.ui.enter(ctx)
		},
	}
}

// waitForReports polls HALF VERSIONS every pollInterval, for up to
// halvesWait, until each half unknown in states has reported, and says
// what each runs.
func (r *run) waitForReports(ctx context.Context, client *updater.Client, c *candidate, states [2]halfState) ([2]halfState, error) {
	r.ui.begin("Waiting...")
	said := 0
	tell := func(format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		if said > 0 {
			s = capitalise(s)
		}
		r.ui.add(" %s.", s)
		said++
	}
	deadline := time.Now().Add(halvesWait)
	for {
		if err := sleep(ctx, pollInterval); err != nil {
			return states, err
		}
		h, err := client.HalfVersions(ctx)
		if err != nil {
			return states, err
		}
		waiting := false
		for i, side := range sides {
			if states[i] != halfUnknown {
				continue
			}
			switch states[i] = stateOf(h.Side(side), c.halfCRC(side)); states[i] {
			case halfUnknown:
				waiting = true
			case halfCurrent:
				tell("the %v half runs this build's image", side)
			default:
				tell("the %v half runs other firmware", side)
			}
		}
		if !waiting {
			r.ui.end()
			return states, nil
		}
		if !time.Now().Before(deadline) {
			tell("no answer after %s", inWords(halvesWait))
			r.ui.end()
			return states, nil
		}
	}
}

// confirmHalves waits up to halvesWait for both halves, still cabled, to
// report the images of the build c, and says whether they did. It fails
// only when ctx ends.
func (r *run) confirmHalves(ctx context.Context, serial string, c *candidate) error {
	said := false
	err := r.useUpdater(ctx, serial, func(client *updater.Client) error {
		err := r.awaitImages(ctx, client, c)
		said = err == nil
		return err
	})
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return err
	case said:
		// The updater's port failed to close after the check.
		r.detail.say("%v", err)
	default:
		r.detail.say("%v", err)
		r.ui.say(notReported)
	}
	return nil
}

func (r *run) awaitImages(ctx context.Context, client *updater.Client, c *candidate) error {
	deadline := time.Now().Add(halvesWait)
	for {
		h, err := client.HalfVersions(ctx)
		switch {
		case errors.Is(err, updater.ErrUnsupported):
			return nil
		case err != nil:
			return err
		case halfStates(&h, c) == bothCurrent:
			r.ui.say("  Both halves run the new firmware.")
			return nil
		case !time.Now().Before(deadline):
			r.ui.say(notReported)
			return nil
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// wireless waits, with the cables still in, until SPLIT STATUS counts both
// halves reporting over Bluetooth: each pairs on its cable's power, once it
// has installed its radio if it needs to. After wirelessWait without both,
// it says how to restart a half, which a radio that still runs Dygma's
// application needs, and waits for Enter before it waits again.
func (r *run) wireless(ctx context.Context, serial string) error {
	r.sayWireless()
	return r.useUpdater(ctx, serial, func(c *updater.Client) error { return r.awaitWireless(ctx, c) })
}

// sayWireless says what the halves do while they pair.
func (r *run) sayWireless() {
	r.ui.say("  Keep the cables in while each half pairs with the Neuron over Bluetooth.")
	r.ui.say("  A half that installs its radio lights yellow and orange first. Each lights cyan once paired.")
}

func (r *run) awaitWireless(ctx context.Context, client *updater.Client) error {
	for {
		r.ui.begin("  Waiting for the halves...")
		shown := 0
		deadline := time.Now().Add(wirelessWait)
		for {
			n, err := client.SplitStatus(ctx)
			if errors.Is(err, updater.ErrUnsupported) {
				r.ui.add(" this Neuron firmware cannot say when they connect.")
				r.ui.end()
				r.ui.say("  Once both halves light cyan, check that typing works.")
				return nil
			}
			if err != nil {
				return err
			}
			switch {
			case n == 2:
				r.ui.add(" 2 of 2 connected.")
				r.ui.end()
				return nil
			case n != shown:
				r.ui.add(" %d of 2 connected...", n)
				shown = n
			}
			if !time.Now().Before(deadline) {
				break
			}
			if err := sleep(ctx, pollInterval); err != nil {
				return err
			}
		}
		r.ui.end()
		after := inWords(wirelessWait)
		switch side, ok := r.missingHalf(ctx, client, shown); {
		case ok:
			r.ui.say("  The %v half has not connected after %s. Unplug its cable, switch it off,", side, after)
		case shown == 1:
			r.ui.say("  One half has not connected after %s. Unplug its cable, switch it off,", after)
		default:
			r.ui.say("  The halves have not connected after %s. For each half, unplug its cable, switch it off,", after)
		}
		r.ui.say("  wait 5 seconds and switch it on. Press Enter to keep waiting.")
		if err := r.ui.enter(ctx); err != nil {
			return err
		}
	}
}

// missingHalf names the half that has not connected, when one of two has:
// the one HALF VERSIONS has no Bluetooth record from, if the other has
// one.
func (r *run) missingHalf(ctx context.Context, client *updater.Client, connected int) (firmware.Side, bool) {
	if connected != 1 {
		return firmware.NoSide, false
	}
	h, err := client.HalfVersions(ctx)
	if err != nil {
		r.detail.say("%v", err)
		return firmware.NoSide, false
	}
	left, right := h.Side(firmware.Left).Link == updater.Bluetooth, h.Side(firmware.Right).Link == updater.Bluetooth
	switch {
	case left && !right:
		return firmware.Right, true
	case right && !left:
		return firmware.Left, true
	}
	return firmware.NoSide, false
}

// sleep waits for d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
