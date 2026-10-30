// SPDX-License-Identifier: MIT

package usbserial

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"go.bug.st/serial"
)

var (
	// ErrStalled reports a write that did not finish within the write
	// timeout. The port is then unusable: the stuck write may still go out
	// later.
	ErrStalled = errors.New("a write did not finish")
	// ErrBusy reports a port that another program holds, such as Bazecor
	// or ZMK Studio in a browser.
	ErrBusy = errors.New("another program holds the port")
	// ErrPermission reports a port the user may not open, as on Linux for
	// a user outside the group that owns the port.
	ErrPermission = errors.New("no permission to open the port")
)

// openError is a failed open that also matches one of the errors above.
type openError struct {
	err, kind error
}

func (e *openError) Error() string   { return e.err.Error() }
func (e *openError) Unwrap() []error { return []error{e.err, e.kind} }

// Open's timeouts: how long Read waits for the first byte, and how long
// Write may take.
const (
	readTimeout  = 50 * time.Millisecond
	writeTimeout = 2 * time.Second
)

// Conn is an open serial port.
type Conn struct {
	path         string
	port         serial.Port
	mode         serial.Mode
	writeTimeout time.Duration
	stalled      atomic.Bool
}

// Open opens a serial port for this program alone, at 115200 baud with DTR
// and RTS on. Reads wait at most 50 ms for data, and writes fail after 2 s.
// A port another program holds gives an error matching ErrBusy, and one
// the user may not open an error matching ErrPermission.
func Open(path string) (*Conn, error) {
	mode := serial.Mode{BaudRate: 115200, DataBits: 8}
	port, err := serial.Open(path, &mode)
	if err != nil {
		if pe, ok := errors.AsType[*serial.PortError](err); ok {
			switch pe.Code() {
			case serial.PortBusy:
				return nil, fmt.Errorf("opening %s: %w; another program holds it, such as Bazecor or ZMK Studio in a "+
					"browser: quit it and try again", path, &openError{err, ErrBusy})
			case serial.PermissionDenied:
				return nil, fmt.Errorf("opening %s: %w", path, &openError{err, ErrPermission})
			}
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := port.SetReadTimeout(readTimeout); err != nil {
		port.Close()
		return nil, fmt.Errorf("%s: setting the read timeout: %w", path, err)
	}
	return newConn(path, port, mode), nil
}

func newConn(path string, port serial.Port, mode serial.Mode) *Conn {
	return &Conn{path: path, port: port, mode: mode, writeTimeout: writeTimeout}
}

// Read reads what has arrived, waiting up to the read timeout for the
// first byte. When nothing arrives in time it returns an error that
// matches os.ErrDeadlineExceeded.
func (c *Conn) Read(p []byte) (int, error) {
	if err := c.usable(); err != nil {
		return 0, err
	}
	n, err := c.port.Read(p)
	if err != nil {
		return n, fmt.Errorf("reading %s: %w", c.path, err)
	}
	if n == 0 && len(p) > 0 {
		return 0, fmt.Errorf("reading %s: %w", c.path, os.ErrDeadlineExceeded)
	}
	return n, nil
}

// Write writes all of p, or fails with ErrStalled once the write timeout
// passes.
func (c *Conn) Write(p []byte) (int, error) {
	if err := c.usable(); err != nil {
		return 0, err
	}
	type result struct {
		n   int
		err error
	}
	// A stalled write outlives this call, so it gets its own copy of p.
	data := bytes.Clone(p)
	done := make(chan result, 1)
	go func() {
		var n int
		var err error
		for n < len(data) && err == nil {
			var m int
			m, err = c.port.Write(data[n:])
			if m == 0 && err == nil {
				err = io.ErrShortWrite
			}
			n += m
		}
		done <- result{n, err}
	}()
	timer := time.NewTimer(c.writeTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			return r.n, fmt.Errorf("writing %s: %w", c.path, r.err)
		}
		return r.n, nil
	case <-timer.C:
		c.stalled.Store(true)
		return 0, c.usable()
	}
}

func (c *Conn) usable() error {
	if c.stalled.Load() {
		return fmt.Errorf("%s: %w within %v", c.path, ErrStalled, c.writeTimeout)
	}
	return nil
}

// SetWriteTimeout sets how long Write may take.
func (c *Conn) SetWriteTimeout(d time.Duration) {
	c.writeTimeout = d
}

// SetBaudRate changes the baud rate and keeps the rest of the line settings.
func (c *Conn) SetBaudRate(baud int) error {
	mode := c.mode
	mode.BaudRate = baud
	if err := c.port.SetMode(&mode); err != nil {
		return fmt.Errorf("%s: setting %d baud: %w", c.path, baud, err)
	}
	c.mode = mode
	return nil
}

// SetDTR sets or clears the DTR line.
func (c *Conn) SetDTR(on bool) error {
	if err := c.port.SetDTR(on); err != nil {
		return fmt.Errorf("%s: setting DTR: %w", c.path, err)
	}
	return nil
}

// ResetInput drops whatever has arrived and not been read.
func (c *Conn) ResetInput() error {
	if err := c.port.ResetInputBuffer(); err != nil {
		return fmt.Errorf("%s: dropping input: %w", c.path, err)
	}
	return nil
}

// Close closes the port. After a stalled write it closes in the
// background, since the stuck write may hold the port until the process
// ends.
func (c *Conn) Close() error {
	if c.stalled.Load() {
		go c.port.Close()
		return nil
	}
	if err := c.port.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", c.path, err)
	}
	return nil
}
