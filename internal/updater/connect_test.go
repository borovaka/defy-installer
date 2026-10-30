// SPDX-License-Identifier: MIT

package updater

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/usbserial"
)

// connect runs Connect with a 100 ms timeout.
func connect(t *testing.T, list usbserial.Lister, o *opener, path string) (*Connection, error) {
	return Connect(t.Context(), list, o.open, testSerial, path, 100*time.Millisecond, nil)
}

func TestConnectRefusesAnotherDevice(t *testing.T) {
	with := func(change func(*usbserial.Port)) usbserial.Port {
		p := updaterPort
		change(&p)
		return p
	}
	for _, p := range []usbserial.Port{
		with(func(p *usbserial.Port) { p.Location = "" }),
		with(func(p *usbserial.Port) { p.Serial = "different" }),
	} {
		o := &opener{}
		_, err := connect(t, listing(p), o, p.Path)
		wantError(t, err, "is not a port of the ZMK Neuron with USB serial number "+testSerial)
		if len(o.calls) > 0 {
			t.Errorf("%+v: opened %v", p, o.calls)
		}
	}
}

func TestConnectRefusesAnotherSerial(t *testing.T) {
	other := updaterPort
	other.Serial = "FEDCBA9876543210"
	o := &opener{}
	_, err := connect(t, listing(other), o, "")
	wantError(t, err, "no port of a ZMK Neuron with USB serial number "+testSerial)
	_, err = connect(t, listing(updaterPort), o, "/dev/missing")
	wantError(t, err, "no serial port /dev/missing")
	if len(o.calls) > 0 {
		t.Errorf("opened %v", o.calls)
	}
}

func TestConnectRefusesAmbiguousPorts(t *testing.T) {
	moved := updaterPort
	moved.Path, moved.Location = "/dev/other", "1-2"
	for _, tt := range []struct {
		ports []usbserial.Port
		path  string
		want  string
	}{
		{[]usbserial.Port{updaterPort, moved}, "", "at more than one USB location"},
		{[]usbserial.Port{updaterPort, updaterPort}, "", "/dev/updater is listed more than once"},
		{[]usbserial.Port{updaterPort, updaterPort}, "/dev/updater", "/dev/updater is listed more than once"},
	} {
		o := &opener{}
		_, err := connect(t, listing(tt.ports...), o, tt.path)
		wantError(t, err, tt.want)
		if len(o.calls) > 0 {
			t.Errorf("opened %v", o.calls)
		}
	}
}

func TestConnectSendsStudioOnlyHello(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		studio, device := newFake(t), newFake(t)
		studio.Silent = true
		o := &opener{devices: map[string]*fakeUpdater{studioPort.Path: studio, updaterPort.Path: device}}
		conn, err := connect(t, listing(studioPort, updaterPort), o, "")
		if err != nil {
			t.Fatal(err)
		}
		if conn.Port != updaterPort || conn.conn != Conn(device) || conn.Client.failed {
			t.Errorf("connected to %+v", conn)
		}
		if !studio.Closed || !slices.Equal(studio.DTR, []bool{true, false}) {
			t.Errorf("Studio's port: closed %v, DTR %v", studio.Closed, studio.DTR)
		}
		// HELLO, and its resend.
		if len(studio.Requests) != 2 || !bytes.Equal(studio.Requests[0].Wire, helloFrame) ||
			!bytes.Equal(studio.Requests[1].Wire, helloFrame) {
			t.Errorf("Studio's port got %v", ops(studio.Requests))
		}
		wantOps(t, "requests", device.Executed, OpHello)
		if device.Closed || !slices.Equal(device.DTR, []bool{true}) {
			t.Errorf("the updater's port: closed %v, DTR %v", device.Closed, device.DTR)
		}
		if err := conn.Close(); err != nil || !device.Closed || !slices.Equal(device.DTR, []bool{true, false}) {
			t.Errorf("Close: %v; closed %v, DTR %v", err, device.Closed, device.DTR)
		}
	})
}

func TestConnectSkipsABusyPort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		device := newFake(t)
		o := &opener{
			devices: map[string]*fakeUpdater{updaterPort.Path: device},
			errs:    map[string]error{studioPort.Path: errors.New("Studio owns the port")},
		}
		conn, err := connect(t, listing(studioPort, updaterPort), o, "")
		if err != nil || conn.conn != Conn(device) {
			t.Fatalf("got %+v, %v", conn, err)
		}
		wantOps(t, "requests", device.Executed, OpHello)

		// A port chosen by path must open and answer.
		if _, err = connect(t, listing(studioPort, updaterPort), o, studioPort.Path); err == nil || err.Error() != "Studio owns the port" {
			t.Errorf("got %v", err)
		}
		studio := newFake(t)
		studio.Silent = true
		o.devices[studioPort.Path], o.errs = studio, nil
		_, err = connect(t, listing(studioPort, updaterPort), o, studioPort.Path)
		if !errors.Is(err, errNoReply) || strings.Contains(err.Error(), "none of") {
			t.Errorf("got %v", err)
		}
		if !studio.Closed {
			t.Error("Studio's port was left open")
		}
	})
}

func TestConnectWaitsBrieflyForHello(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		studio, device := newFake(t), newFake(t)
		studio.Silent = true
		o := &opener{devices: map[string]*fakeUpdater{studioPort.Path: studio, updaterPort.Path: device}}
		start := time.Now()
		conn, err := Connect(t.Context(), listing(studioPort, updaterPort), o.open, testSerial, "", DefaultTimeout,
			nonce(0x12345678))
		if err != nil {
			t.Fatal(err)
		}
		// Studio's port gets two attempts of 350 ms; the client keeps 3 s.
		if waited := time.Since(start); waited != 700*time.Millisecond || conn.Client.timeout != DefaultTimeout {
			t.Errorf("waited %v; the client's timeout is %v", waited, conn.Client.timeout)
		}
		// The client takes its session nonces from where Connect was told.
		if err := conn.Client.open(t.Context()); err != nil || conn.Client.session != 0x12345678 {
			t.Errorf("session %#x, %v", conn.Client.session, err)
		}
	})
}

func TestConnectNeedsOneUpdater(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		other, device := newFake(t), newFake(t)
		o := &opener{devices: map[string]*fakeUpdater{studioPort.Path: other, updaterPort.Path: device}}
		_, err := connect(t, listing(studioPort, updaterPort), o, "")
		wantError(t, err, "2 of the Neuron's ports answered the updater's HELLO")
		for _, d := range []*fakeUpdater{other, device} {
			if !d.Closed || !slices.Equal(d.DTR, []bool{true, false}) {
				t.Errorf("closed %v, DTR %v", d.Closed, d.DTR)
			}
			wantOps(t, "requests", d.Executed, OpHello)
		}

		// None: the reasons come with the error.
		studio := newFake(t)
		studio.Silent = true
		o = &opener{
			devices: map[string]*fakeUpdater{studioPort.Path: studio},
			errs:    map[string]error{updaterPort.Path: errors.New("permission denied")},
		}
		_, err = connect(t, listing(studioPort, updaterPort), o, "")
		wantError(t, err, "none of the Neuron's 2 ports answered the updater's HELLO")
		wantError(t, err, "/dev/studio: updater HELLO: no reply")
		wantError(t, err, "/dev/updater: permission denied")
	})
}

func TestConnectRechecksThePort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, afterHello := range []bool{false, true} {
			device := newFake(t)
			lists := 0
			list := func(context.Context) ([]usbserial.Port, error) {
				lists++
				// Listing, the recheck before opening, then the one after HELLO.
				if lists <= 1 || afterHello && lists <= 2 {
					return []usbserial.Port{updaterPort}, nil
				}
				changed := updaterPort
				changed.Serial = "changed"
				return []usbserial.Port{changed}, nil
			}
			o := &opener{devices: map[string]*fakeUpdater{updaterPort.Path: device}}
			_, err := connect(t, list, o, "")
			wantError(t, err, "USB identity changed")
			if afterHello {
				if !device.Closed {
					t.Error("the port was left open")
				}
				wantOps(t, "requests", device.Executed, OpHello)
			} else if len(o.calls) > 0 {
				t.Errorf("opened %v", o.calls)
			}
		}
	})
}

func TestConnectChecksItsArgumentsFirst(t *testing.T) {
	list := func(context.Context) ([]usbserial.Port, error) {
		t.Error("listed the ports")
		return nil, nil
	}
	o := &opener{}
	for _, timeout := range []time.Duration{0, -time.Second, 5 * time.Second} {
		_, err := Connect(t.Context(), list, o.open, testSerial, "", timeout, nil)
		wantError(t, err, "10s idle limit")
	}
}

func TestConnectStopsWhenCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		studio, device := newFake(t), newFake(t)
		studio.Silent = true
		o := &opener{devices: map[string]*fakeUpdater{studioPort.Path: studio, updaterPort.Path: device}}
		list := listing(studioPort, updaterPort)
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		_, err := Connect(ctx, list, o.open, testSerial, "", 100*time.Millisecond, nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
		if !studio.Closed || len(device.Requests) > 0 {
			t.Errorf("Studio closed %v; the updater's port got %v", studio.Closed, ops(device.Requests))
		}
	})
}

func TestIdentify(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, silent := range []bool{false, true} {
			device := newFake(t)
			device.Silent = silent
			o := &opener{devices: map[string]*fakeUpdater{updaterPort.Path: device}}
			found, err := Identify(t.Context(), updaterPort, o.open)
			if err != nil || found == silent {
				t.Errorf("silent %v: got %v, %v", silent, found, err)
			}
			if !device.Closed || !slices.Equal(device.DTR, []bool{true, false}) {
				t.Errorf("silent %v: closed %v, DTR %v", silent, device.Closed, device.DTR)
			}
			for _, r := range device.Requests {
				if !bytes.Equal(r.Wire, helloFrame) {
					t.Errorf("silent %v: sent %x", silent, r.Wire)
				}
			}
		}
	})
}

func TestIdentifyRefusesPartialOrCorruptReplies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, filter := range []func(request, []byte) []byte{
			func(_ request, b []byte) []byte { return b[:5] },
			func(_ request, b []byte) []byte { return append(b[:len(b)-1:len(b)-1], b[len(b)-1]^1) },
		} {
			device := newFake(t)
			device.Filter = filter
			o := &opener{devices: map[string]*fakeUpdater{updaterPort.Path: device}}
			found, err := Identify(t.Context(), updaterPort, o.open)
			if err == nil || found {
				t.Errorf("got %v, %v", found, err)
			}
			if !device.Closed {
				t.Error("the port was left open")
			}
		}
	})
}

// logPort is the log port of the Neuron's logging builds: it prints its
// log whatever it is sent. Each Read returns at most size bytes, after
// quiet reads that time out.
type logPort struct {
	size, quiet int
	closed      bool
	dtr         []bool
}

func (l *logPort) Read(p []byte) (int, error) {
	if l.quiet > 0 {
		l.quiet--
		time.Sleep(50 * time.Millisecond)
		return 0, fmt.Errorf("log: %w", os.ErrDeadlineExceeded)
	}
	return copy(p[:min(len(p), l.size)], "[00:00:12.345,678] <dbg> zmk: position_state_changed\r\n"), nil
}

func (l *logPort) Write(b []byte) (int, error) { return len(b), nil }

func (l *logPort) SetDTR(on bool) error {
	l.dtr = append(l.dtr, on)
	return nil
}

func (l *logPort) Close() error {
	l.closed = true
	return nil
}

func TestIdentifyTakesALogPortForAnotherPort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, log := range []*logPort{{size: 1}, {size: 5}, {size: 64}, {size: 2, quiet: 3}} {
			found, err := Identify(t.Context(), updaterPort, func(string, time.Duration) (Conn, error) { return log, nil })
			if found || err != nil {
				t.Errorf("%+v: got %v, %v", log, found, err)
			}
			if !log.closed || !slices.Equal(log.dtr, []bool{true, false}) {
				t.Errorf("%+v: closed %v, DTR %v", log, log.closed, log.dtr)
			}
		}
		// Connect finds the updater beside the log port.
		device := newFake(t)
		open := func(path string, _ time.Duration) (Conn, error) {
			if path == studioPort.Path {
				return &logPort{size: 64}, nil
			}
			return device, nil
		}
		conn, err := Connect(t.Context(), listing(studioPort, updaterPort), open, testSerial, "", 100*time.Millisecond, nil)
		if err != nil || conn.conn != Conn(device) {
			t.Errorf("got %+v, %v", conn, err)
		}
	})
}

func TestIdentifyChecksThePort(t *testing.T) {
	o := &opener{errs: map[string]error{updaterPort.Path: errors.New("access denied")}}
	if _, err := Identify(t.Context(), updaterPort, o.open); err == nil || err.Error() != "access denied" {
		t.Errorf("got %v", err)
	}
	if len(o.calls) != 1 {
		t.Errorf("opened %v", o.calls)
	}
	o.calls = nil
	p := updaterPort
	p.Location = ""
	_, err := Identify(t.Context(), p, o.open)
	wantError(t, err, "is not a ZMK Neuron's port")
	if len(o.calls) > 0 {
		t.Errorf("opened %v", o.calls)
	}
}
