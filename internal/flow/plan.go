// SPDX-License-Identifier: MIT

package flow

import (
	"slices"

	"github.com/borovaka/defy-installer/internal/updater"
)

// halfState is what the installer knows of a half before it writes.
type halfState int

const (
	halfUnknown halfState = iota
	halfCurrent
	halfDiffers
)

var bothCurrent = [2]halfState{halfCurrent, halfCurrent}

// stateOf compares a half's record with the program CRC-32 of the build's
// package for it.
func stateOf(rec updater.HalfRecord, crc uint32) halfState {
	switch {
	case !rec.Known():
		return halfUnknown
	case rec.Runs(crc):
		return halfCurrent
	}
	return halfDiffers
}

// halfStates compares what the halves reported with the build's packages.
// With h nil, from firmware older than HALF VERSIONS, both are unknown.
func halfStates(h *updater.HalfVersions, c *candidate) [2]halfState {
	var states [2]halfState
	if h == nil {
		return states
	}
	for i, side := range sides {
		states[i] = stateOf(h.Side(side), c.halfCRC(side))
	}
	return states
}

// plan is what an update of a keyboard running ZMK writes. planUpdate
// makes it from what the Neuron reported and the build; update follows
// it, and status gives its verdict, so that both call the keyboard up to
// date in the same cases.
type plan struct {
	// neuron is set when the Neuron needs writing: it runs another build,
	// or its firmware is too old to say what it runs.
	neuron bool
	// halves is what the installer knows of each half.
	halves [2]halfState
	// wake is set when a half has not reported, and a key press on it can
	// make it report: the Neuron's firmware reports the halves, and it is
	// paired with both.
	wake bool
	// force writes both halves whatever their PROBEs find: for firmware
	// too old to say what it runs, or a keyboard written again on request.
	force bool
	// unpaired is set when the Neuron is not paired with both halves. The
	// halves cannot reach it over Bluetooth then, so they are written
	// through their cables with a pairing reset, whatever they run.
	unpaired bool
	// written is set when the run has already written the Neuron, before
	// it could read the halves, as from the UF2 loader's drive. neuron is
	// then clear.
	written bool
}

// planUpdate decides what an update writes, from what the Neuron reported
// in rep and the build c.
func planUpdate(rep report, c *candidate) plan {
	force := rep.version == nil
	p := plan{
		neuron:   force || !neuronRuns(rep.version, c.manifest()),
		halves:   halfStates(rep.halves, c),
		force:    force,
		unpaired: rep.pairing != nil && !rep.pairing.Paired(),
	}
	p.wake = !force && !p.unpaired && rep.halves != nil && slices.Contains(p.halves[:], halfUnknown)
	return p
}

// woken returns p once the halves asked to wake have answered, or not, in
// states.
func (p plan) woken(states [2]halfState) plan {
	p.halves, p.wake = states, false
	return p
}

// again returns p for a keyboard written again on request: the Neuron, and
// both halves whatever they hold.
func (p plan) again() plan {
	p.neuron, p.force, p.wake = true, true, false
	return p
}

// afterNeuron returns p for a run that has just written the Neuron with the
// build: only the halves are left.
func (p plan) afterNeuron() plan {
	p.neuron, p.written = false, true
	return p
}

// needsCables reports whether the halves go through the cable step, to be
// written or checked.
func (p plan) needsCables() bool {
	return p.force || p.unpaired || p.halves != bothCurrent
}

// upToDate reports whether nothing needs writing.
func (p plan) upToDate() bool {
	return !p.neuron && !p.needsCables()
}

// lines says what the update writes, before it asks for anything.
func (p plan) lines() []string {
	var lines []string
	differs := slices.Contains(p.halves[:], halfDiffers)
	// newNeuron is set when the run writes the Neuron, or has written it.
	newNeuron := p.neuron || p.written
	switch {
	case !p.needsCables() && p.written:
		lines = append(lines, "Both halves already run this build's images.")
	case !p.needsCables():
		lines = append(lines, "Both halves already run this build's images, so only the Neuron needs writing.")
	case p.unpaired:
		lines = append(lines, "The Neuron is not paired with both halves, so both halves are written and paired "+
			"with it again, which clears the halves' Bluetooth settings.")
	case p.force:
		// The run has said why it writes everything.
	case differs && newNeuron:
		lines = append(lines, "The halves need new firmware too.")
	case differs:
		lines = append(lines, "The Neuron already runs this build, so only the halves need new firmware.")
	case newNeuron:
		lines = append(lines, "The halves did not say what they run, so the installer checks them through their cables.")
	default:
		lines = append(lines, "The Neuron already runs this build. The halves did not say what they run, so the "+
			"installer checks them through their cables.")
	}
	if p.neuron {
		lines = append(lines, "Changes made in ZMK Studio will be lost. Your Bluetooth pairings are kept.")
	}
	return lines
}

// unknownHalves names the halves the installer knows nothing of: the left
// half, the right half, or both.
func unknownHalves(states [2]halfState) (name string, both bool) {
	switch {
	case states[0] == halfUnknown && states[1] == halfUnknown:
		return "both halves", true
	case states[0] == halfUnknown:
		return "the left half", false
	}
	return "the right half", false
}

// wakeLine asks for a key press on each half the installer knows nothing
// of. Shift types nothing, so the key does not reach the installer's own
// terminal through the Neuron.
func wakeLine(states [2]halfState) string {
	name, both := unknownHalves(states)
	if both {
		return "Both halves are asleep. Press Shift on each to wake them."
	}
	return capitalise(name) + " is asleep. Press Shift on it to wake it."
}
