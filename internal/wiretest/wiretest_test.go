// SPDX-License-Identifier: MIT

package wiretest

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// recorder collects what a Port reports, so a test can check failures.
type recorder struct {
	errors   []string
	cleanups []func()
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

// end runs the cleanups, as the end of a test would.
func (r *recorder) end() {
	for _, f := range r.cleanups {
		f()
	}
}

var on, off = true, false

func transcript() []Event {
	return []Event{
		{DTR: &on},
		{Host: []byte("HELLO#")},
		{Device: []byte("ACK\r\n")},
		{Host: []byte("S#")},
		{Device: []byte("OK")},
		{DTR: &off},
		{Close: true},
	}
}

func TestPortReportsAWrongByte(t *testing.T) {
	r := &recorder{}
	p := newPort(r, transcript())
	p.SetDTR(true)
	if _, err := p.Write([]byte("HELP#")); !errors.Is(err, ErrMismatch) {
		t.Errorf("got %v", err)
	}
	if len(r.errors) != 1 || !strings.Contains(r.errors[0], "host byte 3 of event 1 is 0x50, the transcript has 0x4c") {
		t.Errorf("reported %q", r.errors)
	}
	// After a mismatch the Port fails everything, and reports nothing more.
	if _, err := p.Read(make([]byte, 1)); !errors.Is(err, ErrMismatch) {
		t.Errorf("read after a mismatch: %v", err)
	}
	r.end()
	if len(r.errors) != 1 {
		t.Errorf("reported %q", r.errors)
	}
}

func TestPortReportsLineChangesOutOfOrder(t *testing.T) {
	r := &recorder{}
	p := newPort(r, transcript())
	if err := p.Close(); !errors.Is(err, ErrMismatch) || len(r.errors) != 1 ||
		!strings.Contains(r.errors[0], "port closed, where the transcript has DTR true") {
		t.Errorf("got %v, reported %q", err, r.errors)
	}
}

func TestPortReportsAnUnfinishedExchange(t *testing.T) {
	r := &recorder{}
	p := newPort(r, transcript())
	p.SetDTR(true)
	p.Write([]byte("HELLO#"))
	r.end()
	if len(r.errors) != 1 || !strings.Contains(r.errors[0], "stopped before event 3 of 7") {
		t.Errorf("reported %q", r.errors)
	}
}

func TestEventsRefuseOtherJSON(t *testing.T) {
	for _, data := range []string{`{"host": "a", "close": true}`, `{"open": true}`} {
		var e Event
		if err := json.Unmarshal([]byte(data), &e); err == nil {
			t.Errorf("%s: accepted", data)
		}
	}
}

// scenario is a scenario that opens the Studio port for the touch, then
// the bootloader's port for S#.
func scenario(t *testing.T) *Scenario {
	path := t.TempDir() + "/s.json"
	os.WriteFile(path, []byte(`{"comment": "c", "argv": ["x"], "status": 1, "opens": [
		{"path": "/dev/studio", "events": [{"baud": 1200}, {"close": true}]},
		{"path": "/dev/boot", "events": [{"host": "S#"}, {"device": "ACK\r\n"}, {"close": true}]}]}`), 0o644)
	return LoadScenario(t, path)
}

func TestKeyboardRefusesOpensOffTheScenario(t *testing.T) {
	r := &recorder{}
	k := scenario(t).Keyboard(r, "zmk", "stock")
	studio, _ := k.Open("/dev/studio")
	studio.SetBaudRate(1200)
	studio.Close()
	if _, err := k.Open("/dev/studio"); !errors.Is(err, ErrMismatch) || len(r.errors) != 1 {
		t.Errorf("a second /dev/studio: %v, reported %q", err, r.errors)
	}
	boot, _ := k.Open("/dev/boot")
	boot.Write([]byte("S#"))
	boot.Read(make([]byte, 5))
	boot.Close()
	if _, err := k.Open("/dev/stock"); !errors.Is(err, ErrMismatch) || len(r.errors) != 2 {
		t.Errorf("an open after the last: %v, reported %q", err, r.errors)
	}
	r.end()
	if len(r.errors) != 2 {
		t.Errorf("reported %q", r.errors)
	}
}

func TestKeyboardReportsPortsNotOpened(t *testing.T) {
	r := &recorder{}
	k := scenario(t).Keyboard(r, "zmk", "zmk")
	studio, _ := k.Open("/dev/studio")
	studio.SetBaudRate(1200)
	studio.Close()
	r.end()
	if len(r.errors) != 1 || !strings.Contains(r.errors[0], "the scenario opens /dev/boot next, and 2 ports in all; 1 were opened") {
		t.Errorf("reported %q", r.errors)
	}
}
