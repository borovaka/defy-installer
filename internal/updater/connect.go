// SPDX-License-Identifier: MIT

package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// Conn is an open serial port; *usbserial.Conn is one. The updater keeps a
// session only while DTR is on. A Read with nothing to return must give up
// within a short time, 50 ms for usbserial.Conn, with an error matching
// os.ErrDeadlineExceeded; the client then reads again until its own
// timeout passes.
type Conn interface {
	io.ReadWriter
	SetDTR(on bool) error
	Close() error
}

// Opener opens the serial port at path. timeout is how long the caller
// waits for a reply; an opener may bound writes by it.
type Opener func(path string, timeout time.Duration) (Conn, error)

// closePort clears DTR, which ends any session and releases the halves,
// then closes the port.
func closePort(conn Conn) error {
	dtr := conn.SetDTR(false)
	return errors.Join(dtr, conn.Close())
}

// Identify reports whether port is the half updater's: it opens the port,
// sets DTR, sends HELLO and nothing else, then clears DTR and closes it. A
// port that sends nothing back, such as ZMK Studio's, is not the updater's,
// and nor is one whose first bytes are not an updater frame's, such as the
// log port of the Neuron's logging builds. A reply that starts as an
// updater frame but stops part way or fails its checks is an error, and so
// is a failure to close the updater's port.
func Identify(ctx context.Context, port usbserial.Port, open Opener) (bool, error) {
	if !neuron.IsZMK(port) || port.Location == "" {
		return false, fmt.Errorf("%s is not a ZMK Neuron's port with a USB serial number and location", port.Path)
	}
	c, err := dial(ctx, port, open, helloTimeout, nil)
	switch {
	case errors.Is(err, errNoReply), errors.Is(err, errNotFrame):
		return false, nil
	case err != nil:
		return false, err
	}
	if err := c.Close(); err != nil {
		return false, err
	}
	return true, nil
}

// Connection is the half updater's port, open with DTR on, and a client
// that has passed HELLO on it.
type Connection struct {
	Client *Client
	conn   Conn
	// Port is the port as the system lists it.
	Port usbserial.Port
}

// Close clears DTR, which ends any session and releases the halves, then
// closes the port.
func (c *Connection) Close() error {
	return closePort(c.conn)
}

// Connect finds the half updater among the ports of the ZMK Neuron with
// USB serial number serial, or takes the port at path, which must be one
// of them. It sends HELLO to each port, with a reply timeout of at most
// 350 ms, and needs exactly one to answer; a port that fails to open or
// answer is skipped, unless it was chosen by path. It lists the ports again
// before it opens each one and once the updater has answered, and fails if
// the port's USB identity has changed.
//
// timeout is the client's reply timeout; a request goes twice at most, so
// it must be under 5 s, half the updater's idle limit. nonces is where the
// client's session nonces come from; nil is crypto/rand.
func Connect(ctx context.Context, list usbserial.Lister, open Opener, serial, path string, timeout time.Duration,
	nonces io.Reader) (*Connection, error) {
	if strings.Trim(strings.TrimSpace(serial), "0") == "" {
		return nil, errors.New("the Neuron's USB serial number must not be blank or all zeros")
	}
	if path != "" && strings.TrimSpace(path) == "" {
		return nil, errors.New("the updater's port path is blank")
	}
	if timeout <= 0 || timeout >= idleLimit/2 {
		return nil, fmt.Errorf("the reply timeout of %v must be positive and let a request go twice within the updater's %v idle limit",
			timeout, idleLimit)
	}
	candidates, err := candidates(ctx, list, serial, path)
	if err != nil {
		return nil, err
	}
	var found []*Connection
	closeFound := func(err error) error {
		for _, c := range found {
			err = errors.Join(err, c.Close())
		}
		return err
	}
	var skipped []error
	for _, target := range candidates {
		if err := usbserial.Recheck(ctx, target, list); err != nil {
			return nil, closeFound(err)
		}
		c, err := dial(ctx, target, open, timeout, nonces)
		if err != nil {
			if path != "" || ctx.Err() != nil {
				return nil, closeFound(err)
			}
			skipped = append(skipped, fmt.Errorf("%s: %w", target.Path, err))
			continue
		}
		found = append(found, c)
	}
	switch len(found) {
	case 0:
		return nil, errors.Join(append([]error{
			fmt.Errorf("none of the Neuron's %d ports answered the updater's HELLO", len(candidates))}, skipped...)...)
	case 1:
	default:
		return nil, closeFound(fmt.Errorf("%d of the Neuron's ports answered the updater's HELLO, not one", len(found)))
	}
	if err := usbserial.Recheck(ctx, found[0].Port, list); err != nil {
		return nil, closeFound(err)
	}
	return found[0], nil
}

// candidates lists the ports Connect tries: the one at path, or every port
// of the ZMK Neuron with USB serial number serial. They must all be that
// Neuron's, at one USB location.
func candidates(ctx context.Context, list usbserial.Lister, serial, path string) ([]usbserial.Port, error) {
	ports, err := list(ctx)
	if err != nil {
		return nil, err
	}
	var found []usbserial.Port
	if path != "" {
		p, err := usbserial.Find(ports, path)
		if err != nil {
			return nil, err
		}
		found = append(found, p)
	} else {
		for _, p := range ports {
			if neuron.IsZMK(p) && p.Serial == serial {
				found = append(found, p)
			}
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no port of a ZMK Neuron with USB serial number %s", serial)
	}
	paths := make(map[string]bool)
	for _, p := range found {
		if !neuron.IsZMK(p) || p.Location == "" || p.Serial != serial {
			return nil, fmt.Errorf("%s is not a port of the ZMK Neuron with USB serial number %s and a USB location", p.Path, serial)
		}
		if p.Location != found[0].Location {
			return nil, fmt.Errorf("the Neuron with USB serial number %s is at more than one USB location", serial)
		}
		if paths[p.Path] {
			return nil, fmt.Errorf("%s is listed more than once", p.Path)
		}
		paths[p.Path] = true
	}
	return found, nil
}

// dial opens target, sets DTR and sends HELLO. It closes the port again
// if anything fails.
func dial(ctx context.Context, target usbserial.Port, open Opener, timeout time.Duration, nonces io.Reader) (*Connection, error) {
	conn, err := open(target.Path, timeout)
	if err != nil {
		return nil, err
	}
	c := &Client{nonces: nonces, port: conn, timeout: timeout}
	err = conn.SetDTR(true)
	if err == nil {
		err = c.hello(ctx, min(timeout, helloTimeout))
	}
	if err != nil {
		return nil, errors.Join(err, closePort(conn))
	}
	return &Connection{Client: c, conn: conn, Port: target}, nil
}
