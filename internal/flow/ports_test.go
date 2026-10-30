// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// Ports for the sorting tests: a second Neuron of each kind, at another
// USB location, and ports that are no Neuron's.
var (
	otherStudio   = with(studioPort, func(p *usbserial.Port) { p.Path, p.Serial, p.Location = "/dev/other", "FEDCBA9876543210", "1-2" })
	movedStudio   = with(studioPort, func(p *usbserial.Port) { p.Path, p.Location = "/dev/moved", "1-2" })
	blankStudio   = with(studioPort, func(p *usbserial.Port) { p.Path, p.Serial = "/dev/blank", "000000000000" })
	otherBoot     = with(bootPort, func(p *usbserial.Port) { p.Path = "/dev/boot2" })
	otherStock    = with(stockPort, func(p *usbserial.Port) { p.Path, p.Location = "/dev/stock2", "1-2" })
	otherLoader   = with(loaderPort, func(p *usbserial.Port) { p.Path, p.Serial, p.Location = "/dev/loader2", "FEDCBA9876543210", "1-2" })
	otherKeyboard = usbserial.Port{Path: "/dev/zmk", ID: neuron.ZMKID, Serial: "1234", Product: "Corne", Location: "1-3"}
	ftdi          = usbserial.Port{Path: "/dev/ftdi", ID: usbserial.USBID{Vendor: 0x0403, Product: 0x6001}, Location: "1-4"}
)

// with returns a copy of p changed by change.
func with(p usbserial.Port, change func(*usbserial.Port)) usbserial.Port {
	change(&p)
	return p
}

// listed is an Env that lists ports and fails the test if one opens.
func listed(t *testing.T, ports ...usbserial.Port) Env {
	return Env{
		List: func(context.Context) ([]usbserial.Port, error) { return ports, nil },
		Open: func(string, time.Duration) (Port, error) {
			t.Error("a port was opened")
			return nil, errors.New("no ports")
		},
		Out: &strings.Builder{},
	}
}

func TestNeurons(t *testing.T) {
	for _, tt := range []struct {
		name  string
		ports []usbserial.Port
		want  []device
	}{
		{"ZMK's two ports are one Neuron", []usbserial.Port{studioPort, ftdi, updaterPort},
			[]device{{neuron.ZMK, []usbserial.Port{studioPort, updaterPort}}}},
		{"one serial number at two locations", []usbserial.Port{studioPort, movedStudio},
			[]device{{neuron.ZMK, []usbserial.Port{studioPort}}, {neuron.ZMK, []usbserial.Port{movedStudio}}}},
		{"two serial numbers", []usbserial.Port{otherStudio, studioPort},
			[]device{{neuron.ZMK, []usbserial.Port{otherStudio}}, {neuron.ZMK, []usbserial.Port{studioPort}}}},
		{"each bootloader port", []usbserial.Port{bootPort, otherBoot},
			[]device{{neuron.Bootloader, []usbserial.Port{bootPort}}, {neuron.Bootloader, []usbserial.Port{otherBoot}}}},
		{"each port of Dygma's firmware", []usbserial.Port{stockPort, otherStock},
			[]device{{neuron.Stock, []usbserial.Port{stockPort}}, {neuron.Stock, []usbserial.Port{otherStock}}}},
		{"each UF2 loader port", []usbserial.Port{loaderPort, ftdi, otherLoader},
			[]device{{neuron.Loader, []usbserial.Port{loaderPort}}, {neuron.Loader, []usbserial.Port{otherLoader}}}},
		{"in the order listed", []usbserial.Port{bootPort, updaterPort, stockPort, studioPort},
			[]device{{neuron.Bootloader, []usbserial.Port{bootPort}}, {neuron.ZMK, []usbserial.Port{updaterPort, studioPort}},
				{neuron.Stock, []usbserial.Port{stockPort}}}},
		{"no Neuron's", []usbserial.Port{blankStudio, otherKeyboard, ftdi}, nil},
	} {
		got := neurons(tt.ports)
		if !slices.EqualFunc(got, tt.want, func(a, b device) bool { return a.mode == b.mode && slices.Equal(a.ports, b.ports) }) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestEachCommandSortsPortsAlike runs the "more than one" rule of each
// caller of neurons on the lists where they once differed.
func TestEachCommandSortsPortsAlike(t *testing.T) {
	// The user's commands.
	for _, tt := range []struct {
		ports []usbserial.Port
		want  found
		err   string
	}{
		{[]usbserial.Port{bootPort, otherBoot}, found{}, "More than one Defy is connected"},
		{[]usbserial.Port{studioPort, movedStudio}, found{}, "More than one Defy is connected"},
		{[]usbserial.Port{stockPort, blankStudio}, found{mode: neuron.Stock}, ""},
		{[]usbserial.Port{updaterPort, studioPort, otherKeyboard}, found{neuron.ZMK, testSerial}, ""},
		// The UF2 loader has ZMK's serial number, which the start from the
		// drive follows.
		{[]usbserial.Port{loaderPort, ftdi}, found{neuron.Loader, testSerial}, ""},
		{[]usbserial.Port{loaderPort, otherBoot}, found{}, "More than one Defy is connected"},
		{[]usbserial.Port{loaderPort, otherStudio}, found{}, "More than one Defy is connected"},
	} {
		r := newRun(t.Context(), listed(t, tt.ports...), Options{})
		f, err := r.detect(t.Context())
		if f != tt.want || !matches(err, tt.err) {
			t.Errorf("detect %v: got %+v, %v", paths(tt.ports), f, err)
		}
	}

	// The halves commands.
	for _, tt := range []struct {
		ports []usbserial.Port
		err   string
	}{
		{[]usbserial.Port{studioPort, movedStudio}, "more than one Neuron running ZMK"},
		{[]usbserial.Port{blankStudio, bootPort, studioPort}, ""},
		// The UF2 loader has ZMK's serial number, but no half updater.
		{[]usbserial.Port{loaderPort}, "no Neuron running ZMK found"},
		{[]usbserial.Port{otherLoader, studioPort}, ""},
	} {
		serial, err := neuronSerial(t.Context(), listed(t, tt.ports...), Halves{})
		if !matches(err, tt.err) || err == nil && serial != testSerial {
			t.Errorf("neuronSerial %v: got %q, %v", paths(tt.ports), serial, err)
		}
	}

	// The return to stock.
	for _, tt := range []struct {
		ports   []usbserial.Port
		port    string
		running bool
		err     string
	}{
		{[]usbserial.Port{stockPort, blankStudio, ftdi}, "", true, ""},
		{[]usbserial.Port{stockPort, bootPort}, "", false, ""},
		{[]usbserial.Port{stockPort, otherStock, studioPort}, "", false, "more than one Neuron runs Dygma's firmware"},
		{[]usbserial.Port{stockPort, loaderPort}, "", false, ""},
		{[]usbserial.Port{studioPort, updaterPort, stockPort}, stockPort.Path, true, ""},
		{[]usbserial.Port{stockPort}, "/dev/gone", false, "no serial port /dev/gone"},
	} {
		p, running, err := runningStock(t.Context(), listed(t, tt.ports...), Neuron{Port: tt.port})
		if running != tt.running || !matches(err, tt.err) || running && p != stockPort {
			t.Errorf("runningStock %v: got %v, %v, %v", paths(tt.ports), p.Path, running, err)
		}
	}

	// findNeuron, whose rule the other selectors share. None of these cases
	// reaches a port.
	const ambiguous = "more than one Neuron or Neuron bootloader is connected"
	for _, tt := range []struct {
		ports     []usbserial.Port
		serial    string
		fromStock bool
		want, err string
	}{
		{[]usbserial.Port{bootPort, otherBoot}, "", false, "", ambiguous},
		{[]usbserial.Port{bootPort, stockPort}, "", false, bootPort.Path, ""},
		{[]usbserial.Port{bootPort, stockPort}, "", true, "", ambiguous},
		{[]usbserial.Port{stockPort, blankStudio}, "", true, stockPort.Path, ""},
		{[]usbserial.Port{stockPort, otherStock}, "", false, "", "the Neuron runs Dygma's firmware"},
		{[]usbserial.Port{studioPort, movedStudio}, "", false, "", ambiguous},
		{[]usbserial.Port{bootPort, otherStudio}, testSerial, false, "", "no Neuron running ZMK with USB serial number " + testSerial},
		{[]usbserial.Port{blankStudio, otherKeyboard}, "", false, "", "no Neuron found"},
		// The UF2 loader counts where ZMK does, by ZMK's serial number.
		{[]usbserial.Port{loaderPort, ftdi}, "", false, loaderPort.Path, ""},
		{[]usbserial.Port{loaderPort, stockPort}, "", false, loaderPort.Path, ""},
		{[]usbserial.Port{loaderPort, stockPort}, "", true, "", ambiguous},
		{[]usbserial.Port{loaderPort, bootPort}, "", false, "", ambiguous},
		{[]usbserial.Port{loaderPort, otherLoader}, "", false, "", ambiguous},
		{[]usbserial.Port{loaderPort, otherStudio}, "", false, "", ambiguous},
		{[]usbserial.Port{otherStudio, loaderPort}, testSerial, false, loaderPort.Path, ""},
		{[]usbserial.Port{otherLoader}, testSerial, false, "", "no Neuron running ZMK with USB serial number " + testSerial},
	} {
		env := listed(t, tt.ports...)
		path, err := findNeuron(t.Context(), env, env.entry(DefaultNeuronTimeout), tt.serial, tt.fromStock)
		if path != tt.want || !matches(err, tt.err) {
			t.Errorf("findNeuron %v, serial %q, from stock %v: got %q, %v", paths(tt.ports), tt.serial, tt.fromStock, path, err)
		}
	}
}

// paths lists the ports' paths for a test's message.
func paths(ports []usbserial.Port) []string {
	var out []string
	for _, p := range ports {
		out = append(out, p.Path)
	}
	return out
}
