// SPDX-License-Identifier: MIT

package neurontest

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// Loader plays the serial port of the UF2 loader, the CDC ACM port beside
// its DEFY drive. Like ZMK's Studio port, it takes a change to 1200 baud
// with DTR low as the request for the bootloader, which it then enters by
// writing Dygma's boot request and resetting. The real loader checks the
// line 250 ms after the change; Loader checks it when the port closes. It
// sends nothing, and a write fails the test. Reads behave as
// usbserial.Conn's do.
type Loader struct {
	t testing.TB

	// OnRequest, if set, runs once the request is taken, as the Neuron
	// resets into its bootloader.
	OnRequest func()

	// Lines are the changes to the line, in order: "DTR on", "DTR off",
	// "<n> baud" and "close".
	Lines []string
	// Requested is set once the request is taken.
	Requested bool

	baud int
	dtr  bool
}

// NewLoader returns the loader's port as it is once opened: at 115200
// baud with DTR on.
func NewLoader(t testing.TB) *Loader {
	return &Loader{t: t, baud: 115200, dtr: true}
}

// Read waits 50 ms and times out, since the loader sends nothing.
func (l *Loader) Read([]byte) (int, error) {
	time.Sleep(50 * time.Millisecond)
	return 0, os.ErrDeadlineExceeded
}

func (l *Loader) Write(p []byte) (int, error) {
	l.t.Errorf("fake loader: unexpected write %q", p)
	return len(p), nil
}

func (l *Loader) SetDTR(on bool) error {
	l.dtr = on
	if on {
		l.Lines = append(l.Lines, "DTR on")
	} else {
		l.Lines = append(l.Lines, "DTR off")
	}
	return nil
}

func (l *Loader) SetBaudRate(baud int) error {
	l.baud = baud
	l.Lines = append(l.Lines, strconv.Itoa(baud)+" baud")
	return nil
}

func (l *Loader) ResetInput() error { return nil }

// Close closes the port, and takes the request if the line is at 1200
// baud with DTR low.
func (l *Loader) Close() error {
	l.Lines = append(l.Lines, "close")
	if l.baud == 1200 && !l.dtr && !l.Requested {
		l.Requested = true
		if l.OnRequest != nil {
			l.OnRequest()
		}
	}
	return nil
}
