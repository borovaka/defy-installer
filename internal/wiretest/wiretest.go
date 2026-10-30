// SPDX-License-Identifier: MIT

// Package wiretest plays the device's side of recorded serial exchanges in
// tests: the code under test must send the recording's bytes in its order.
//
// A scenario is a command run over several ports, with a transcript of the
// exchange on each. A transcript is a list of events, each one of
//
//	{"host": "text"} or {"host_hex": "..."}      bytes the host writes
//	{"device": "text"} or {"device_hex": "..."}  bytes the device sends back
//	{"dtr": true}, {"baud": 1200}, {"reset_input": true}, {"close": true}
//
// The device's bytes become readable once everything before them has
// happened. Keyboard plays a scenario as the Neuron, which enumerates again
// between the ports. The scenarios in this repository fix the bytes, and
// their order, that each exchange of the installer must keep.
package wiretest

import (
	"bytes"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Event is one step of a transcript: data from the host or the device, or a
// change the host makes to the line.
type Event struct {
	Host, Device []byte
	DTR          *bool
	Baud         int
	ResetInput   bool
	Close        bool
}

func (e Event) String() string {
	switch {
	case e.Host != nil:
		return fmt.Sprintf("host writes %s", show(e.Host))
	case e.Device != nil:
		return fmt.Sprintf("device sends %s", show(e.Device))
	case e.DTR != nil:
		return fmt.Sprintf("DTR %v", *e.DTR)
	case e.Baud != 0:
		return fmt.Sprintf("%d baud", e.Baud)
	case e.ResetInput:
		return "input dropped"
	}
	return "port closed"
}

func show(b []byte) string {
	const most = 32
	if len(b) > most {
		return fmt.Sprintf("%q... (%d bytes)", b[:most], len(b))
	}
	return fmt.Sprintf("%q", b)
}

// UnmarshalJSON reads an event in the transcript format.
func (e *Event) UnmarshalJSON(data []byte) error {
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var err error
	*e, err = parseEvent(fields)
	return err
}

func parseEvent(fields map[string]jsontext.Value) (Event, error) {
	var e Event
	if len(fields) != 1 {
		return e, errors.New("an event has exactly one field")
	}
	for key, value := range fields {
		var err error
		switch key {
		case "host", "device":
			var s string
			err = json.Unmarshal(value, &s)
			if key == "host" {
				e.Host = []byte(s)
			} else {
				e.Device = []byte(s)
			}
		case "host_hex", "device_hex":
			var s string
			var b []byte
			if err = json.Unmarshal(value, &s); err == nil {
				b, err = hex.DecodeString(s)
			}
			if key == "host_hex" {
				e.Host = b
			} else {
				e.Device = b
			}
		case "dtr":
			e.DTR = new(bool)
			err = json.Unmarshal(value, e.DTR)
		case "baud":
			err = json.Unmarshal(value, &e.Baud)
		case "reset_input":
			err = json.Unmarshal(value, &e.ResetInput)
		case "close":
			err = json.Unmarshal(value, &e.Close)
		default:
			err = fmt.Errorf("unknown event %q", key)
		}
		if err != nil {
			return e, fmt.Errorf("%s: %w", key, err)
		}
	}
	return e, nil
}

// TB is the part of testing.TB a Port reports to.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Cleanup(func())
}

// ErrMismatch is what Port returns once the code under test has left the
// transcript. The test has failed by then.
var ErrMismatch = errors.New("the exchange left the transcript")

// Port plays the device's side of a transcript. It has usbserial.Conn's
// methods other than the timeouts', so it stands in for a port wherever
// code takes an interface that Conn satisfies.
type Port struct {
	t        TB
	mu       sync.Mutex
	events   []Event
	next     int // the next event
	offset   int // bytes of the next host event already written
	readable []byte
	failed   bool
	onClose  func() // set by a Keyboard
}

// newPort returns a Port that plays events. When the test ends, every event
// must have happened and every byte the device sent must have been read.
func newPort(t TB, events []Event) *Port {
	p := &Port{t: t, events: events}
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.failed {
			return
		}
		p.release()
		if p.next < len(p.events) {
			t.Errorf("wiretest: the exchange stopped before event %d of %d: %v", p.next, len(p.events), p.events[p.next])
		} else if len(p.readable) > 0 {
			t.Errorf("wiretest: the device's last %s went unread", show(p.readable))
		}
	})
	return p
}

// release makes the device's bytes that are due readable.
func (p *Port) release() {
	for p.next < len(p.events) && p.events[p.next].Device != nil {
		p.readable = append(p.readable, p.events[p.next].Device...)
		p.next++
	}
}

func (p *Port) fail(format string, args ...any) error {
	p.t.Helper()
	p.failed = true
	p.t.Errorf("wiretest: "+format, args...)
	return ErrMismatch
}

// expect takes the next event, which must be a line change matching want.
func (p *Port) expect(want Event, match func(Event) bool) error {
	p.t.Helper()
	if p.failed {
		return ErrMismatch
	}
	p.release()
	if p.next == len(p.events) {
		return p.fail("%v after the end of the transcript", want)
	}
	if got := p.events[p.next]; p.offset > 0 || !match(got) {
		return p.fail("%v, where the transcript has %v", want, p.expected())
	}
	p.next++
	return nil
}

func (p *Port) expected() string {
	e := p.events[p.next]
	if e.Host != nil && p.offset > 0 {
		return fmt.Sprintf("host writes %s (from byte %d)", show(e.Host[p.offset:]), p.offset)
	}
	return e.String()
}

// Write takes bytes the transcript has the host write next.
func (p *Port) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.t.Helper()
	if p.failed {
		return 0, ErrMismatch
	}
	written := 0
	for written < len(b) {
		p.release()
		if p.next == len(p.events) {
			return written, p.fail("host writes %s after the end of the transcript", show(b[written:]))
		}
		e := p.events[p.next]
		if e.Host == nil {
			return written, p.fail("host writes %s, where the transcript has %v", show(b[written:]), e)
		}
		want := e.Host[p.offset:]
		n := min(len(want), len(b)-written)
		if !bytes.Equal(b[written:written+n], want[:n]) {
			at := 0
			for b[written+at] == want[at] {
				at++
			}
			return written, p.fail("host byte %d of event %d is 0x%02x, the transcript has 0x%02x: wrote %s, expected %s",
				p.offset+at, p.next, b[written+at], want[at], show(b[written+at:written+n]), show(want[at:n]))
		}
		written += n
		p.offset += n
		if p.offset == len(e.Host) {
			p.next++
			p.offset = 0
		}
	}
	return written, nil
}

// Read returns the device's bytes that are due. With none due it waits
// 50 ms and fails with an error matching os.ErrDeadlineExceeded, as
// usbserial.Conn does.
func (p *Port) Read(b []byte) (int, error) {
	p.mu.Lock()
	if p.failed {
		p.mu.Unlock()
		return 0, ErrMismatch
	}
	p.release()
	if len(p.readable) > 0 {
		n := copy(b, p.readable)
		p.readable = p.readable[n:]
		p.mu.Unlock()
		return n, nil
	}
	p.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	return 0, fmt.Errorf("wiretest: %w", os.ErrDeadlineExceeded)
}

// SetDTR expects the transcript's next event to set DTR to on.
func (p *Port) SetDTR(on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.expect(Event{DTR: &on}, func(e Event) bool { return e.DTR != nil && *e.DTR == on })
}

// SetBaudRate expects the transcript's next event to change to baud.
func (p *Port) SetBaudRate(baud int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.expect(Event{Baud: baud}, func(e Event) bool { return e.Baud == baud })
}

// ResetInput expects the transcript's next event to drop the input.
func (p *Port) ResetInput() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.expect(Event{ResetInput: true}, func(e Event) bool { return e.ResetInput })
}

// Close expects the transcript's next event to close the port.
func (p *Port) Close() error {
	p.mu.Lock()
	err := p.expect(Event{Close: true}, func(e Event) bool { return e.Close })
	onClose := p.onClose
	p.mu.Unlock()
	if err == nil && onClose != nil {
		onClose()
	}
	return err
}
