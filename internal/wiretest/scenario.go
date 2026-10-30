// SPDX-License-Identifier: MIT

package wiretest

import (
	"context"
	"encoding/json/v2"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/borovaka/defy-installer/internal/usbserial"
)

// Scenario is a recorded run of a command: the ports it opened, in order,
// each with its own exchange. In JSON it is a comment, the command's exit
// status, and a list of opens, each a path and that port's transcript.
type Scenario struct {
	Comment string   `json:"comment"`
	Status  int      `json:"status"`
	Opens   []Opened `json:"opens"`
}

// Opened is one port a scenario opened.
type Opened struct {
	Path   string  `json:"path"`
	Events []Event `json:"events"`
}

// LoadScenario reads a scenario file.
func LoadScenario(t testing.TB, path string) *Scenario {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := new(Scenario)
	if err := json.Unmarshal(data, s); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return s
}

// Serial is the USB serial number of the scenarios' Neuron running ZMK.
const Serial = "0123456789ABCDEF"

// The ports of the scenarios' Neuron, as the recordings list them: ZMK's
// two, its bootloader's and that of Dygma's firmware. LoaderPort, the UF2
// loader's, has no recording; it has ZMK's serial number, as the loader
// does.
var (
	StudioPort  = usbserial.Port{Path: "/dev/studio", ID: zmkID, Serial: Serial, Product: "Defy", Location: "1-1"}
	UpdaterPort = usbserial.Port{Path: "/dev/updater", ID: zmkID, Serial: Serial, Product: "Defy", Location: "1-1"}
	BootPort    = usbserial.Port{Path: "/dev/boot", ID: bootloaderID, Serial: "000000000000", Product: "Dygma Bootloader", Location: "1-1"}
	StockPort   = usbserial.Port{Path: "/dev/stock", ID: stockID, Serial: "Dygma", Product: "Defy", Location: "1-1"}
	LoaderPort  = usbserial.Port{Path: "/dev/loader", ID: loaderID, Serial: Serial, Product: "Defy UF2 loader", Location: "1-1"}
)

// The Neuron's USB IDs in each mode, as the neuron package has them.
var (
	zmkID        = usbserial.USBID{Vendor: 0x1d50, Product: 0x615e}
	stockID      = usbserial.USBID{Vendor: 0x35ef, Product: 0x0012}
	bootloaderID = usbserial.USBID{Vendor: 0x35ef, Product: 0x0013}
	loaderID     = usbserial.USBID{Vendor: 0x2fe3, Product: 0x0100}
)

// Keyboard is the scenarios' Neuron. It hands out a scenario's ports in the
// order the scenario opened them, and lists the ports of its mode: "zmk",
// "boot", "stock" or "loader". The mode changes as the Neuron enumerates
// again: to "boot" once the Studio port or the loader's closes after a
// change to 1200 baud, and to the mode the bootloader starts once the
// bootloader's port closes after S#. When the test ends, every port must
// have been opened.
type Keyboard struct {
	t       TB
	mu      sync.Mutex
	opens   []Opened
	next    int
	mode    string
	returns string
}

// Keyboard returns a Keyboard that plays s from mode. returns is the mode
// the bootloader starts.
func (s *Scenario) Keyboard(t TB, mode, returns string) *Keyboard {
	k := &Keyboard{t: t, opens: s.Opens, mode: mode, returns: returns}
	t.Cleanup(func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		if k.next < len(k.opens) {
			t.Errorf("wiretest: the scenario opens %s next, and %d ports in all; %d were opened",
				k.opens[k.next].Path, len(k.opens), k.next)
		}
	})
	return k
}

// List lists the ports of the Neuron's mode.
func (k *Keyboard) List(context.Context) ([]usbserial.Port, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch k.mode {
	case "boot":
		return []usbserial.Port{BootPort}, nil
	case "stock":
		return []usbserial.Port{StockPort}, nil
	case "loader":
		return []usbserial.Port{LoaderPort}, nil
	}
	return []usbserial.Port{StudioPort, UpdaterPort}, nil
}

// SetMode changes the mode, as the Neuron does when a device the scenario
// does not hold restarts it.
func (k *Keyboard) SetMode(mode string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.mode = mode
}

// Open returns a Port that plays the scenario's next port, which must be
// at path.
func (k *Keyboard) Open(path string) (*Port, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.t.Helper()
	if k.next == len(k.opens) {
		k.t.Errorf("wiretest: %s opened after the scenario's last port", path)
		return nil, ErrMismatch
	}
	want := k.opens[k.next]
	if want.Path != path {
		k.t.Errorf("wiretest: open %d is %s, where the scenario opens %s", k.next, path, want.Path)
		return nil, ErrMismatch
	}
	k.next++
	p := newPort(k.t, want.Events)
	p.onClose = func() { k.closed(path, want.Events) }
	return p, nil
}

// closed changes the mode once the port at path, which played events, has
// closed.
func (k *Keyboard) closed(path string, events []Event) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch {
	case (path == StudioPort.Path || path == LoaderPort.Path) &&
		slices.ContainsFunc(events, func(e Event) bool { return e.Baud == 1200 }):
		k.mode = "boot"
	case path == BootPort.Path && slices.ContainsFunc(events, func(e Event) bool { return string(e.Host) == "S#" }):
		k.mode = k.returns
	}
}
