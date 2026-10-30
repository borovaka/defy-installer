// SPDX-License-Identifier: MIT

package usbserial

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"go.bug.st/serial"
)

// fakePort stands in for an open port. Methods the tests do not use are
// left to the embedded nil interface.
type fakePort struct {
	serial.Port
	written  []byte
	maxWrite int           // bytes one Write takes; 0 takes everything
	release  chan struct{} // when set, Write waits for it to close
	mode     serial.Mode
	closed   bool
}

func (p *fakePort) Write(b []byte) (int, error) {
	if p.release != nil {
		<-p.release
	}
	if p.maxWrite > 0 && len(b) > p.maxWrite {
		b = b[:p.maxWrite]
	}
	p.written = append(p.written, b...)
	return len(b), nil
}

func (p *fakePort) Read([]byte) (int, error) { return 0, nil }

func (p *fakePort) SetMode(m *serial.Mode) error {
	p.mode = *m
	return nil
}

func (p *fakePort) Close() error {
	p.closed = true
	return nil
}

func TestWriteFinishesShortWrites(t *testing.T) {
	port := &fakePort{maxWrite: 3}
	c := newConn("COM5", port, serial.Mode{})
	n, err := c.Write([]byte("hello, Neuron"))
	if n != 13 || err != nil || string(port.written) != "hello, Neuron" {
		t.Errorf("got %d, %v, wrote %q", n, err, port.written)
	}
}

func TestStalledWriteFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		port := &fakePort{release: make(chan struct{})}
		c := newConn("COM5", port, serial.Mode{})
		start := time.Now()
		data := []byte("page")
		if _, err := c.Write(data); !errors.Is(err, ErrStalled) {
			t.Fatalf("got %v, want ErrStalled", err)
		}
		if waited := time.Since(start); waited != 2*time.Second {
			t.Errorf("failed after %v, want 2s", waited)
		}
		data[0] = 'X' // the caller may reuse its buffer
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, ErrStalled) {
			t.Errorf("read after a stall: got %v, want ErrStalled", err)
		}
		if err := c.Close(); err != nil {
			t.Error(err)
		}
		// The stuck write goes out once the port lets it.
		close(port.release)
		synctest.Wait()
		if string(port.written) != "page" || !port.closed {
			t.Errorf("wrote %q, closed %v", port.written, port.closed)
		}
	})
}

func TestReadTimesOut(t *testing.T) {
	c := newConn("COM5", &fakePort{}, serial.Mode{})
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("got %v, want os.ErrDeadlineExceeded", err)
	}
}

func TestSetBaudRateKeepsTheLineSettings(t *testing.T) {
	port := &fakePort{}
	c := newConn("COM5", port, serial.Mode{BaudRate: 115200, DataBits: 8})
	if err := c.SetBaudRate(1200); err != nil {
		t.Fatal(err)
	}
	if want := (serial.Mode{BaudRate: 1200, DataBits: 8}); port.mode != want {
		t.Errorf("mode %+v, want %+v", port.mode, want)
	}
}

// A failed open matches the library's error and the kind of failure.
func TestOpenErrorsMatchTheirKind(t *testing.T) {
	cause := errors.New("Serial port busy")
	err := fmt.Errorf("opening COM7: %w", &openError{cause, ErrBusy})
	if !errors.Is(err, ErrBusy) || !errors.Is(err, cause) || errors.Is(err, ErrPermission) {
		t.Errorf("got %v", err)
	}
}
