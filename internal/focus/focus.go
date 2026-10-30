// SPDX-License-Identifier: MIT

// Package focus talks to Dygma's 2.3.0 Neuron firmware over Focus, its
// line protocol, and through it writes Dygma's keyscanner back to both
// halves, as Bazecor's firmware update does.
//
// A request is a command and a newline, or a command, a space and a binary
// payload. A reply is lines of text, then a line holding ".".
//
// The port given to EnterBootloader and Restore is the Neuron's, open. A
// read from it must wait a short time at most and then fail with an error
// matching os.ErrDeadlineExceeded, and a write must write everything or
// fail, as usbserial.Conn's do.
package focus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// replyTimeout is how long a request waits for its reply.
const replyTimeout = 10 * time.Second

// maxReply is the longest reply taken, in bytes.
const maxReply = 16384

// client sends Focus requests to Dygma's Neuron firmware over rw. The
// exported functions stop at its first error, so it is never used after one.
type client struct {
	rw io.ReadWriter
}

// request sends command, with payload when it is not nil, and returns the
// reply with surrounding white space trimmed.
func (c *client) request(ctx context.Context, command string, payload []byte) (string, error) {
	if command == "" || strings.ContainsFunc(command, func(r rune) bool { return r < 32 || r > 126 }) {
		return "", fmt.Errorf("invalid Focus command %q", command)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	reply, err := c.exchange(command, payload)
	if err != nil {
		return "", fmt.Errorf("%s: %w", command, err)
	}
	return reply, nil
}

func (c *client) exchange(command string, payload []byte) (string, error) {
	wire := []byte(command)
	if payload != nil {
		wire = append(append(wire, ' '), payload...)
	} else {
		wire = append(wire, '\n')
	}
	deadline := time.Now().Add(replyTimeout)
	if _, err := c.rw.Write(wire); err != nil {
		return "", err
	}
	var reply, line []byte
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		n, err := c.rw.Read(buf)
		if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			return "", err
		}
		for _, b := range buf[:n] {
			if b != '\t' && b != '\n' && b != '\r' && (b < 32 || b > 126) {
				return "", errors.New("the reply is not printable ASCII")
			}
			if b != '\n' {
				line = append(line, b)
			} else if string(bytes.TrimRight(line, "\r")) == "." {
				return strings.TrimSpace(string(reply)), nil
			} else {
				reply = append(append(reply, line...), '\n')
				line = line[:0]
			}
			if len(reply)+len(line) > maxReply {
				return "", fmt.Errorf("reply longer than %d bytes", maxReply)
			}
		}
	}
	return "", errors.New("no complete reply in time")
}

// boolean sends command and returns its reply, which must be true or false.
func (c *client) boolean(ctx context.Context, command string, payload []byte) (bool, error) {
	reply, err := c.request(ctx, command, payload)
	if err != nil {
		return false, err
	}
	if reply != "true" && reply != "false" {
		return false, fmt.Errorf("%s: expected true or false, received %q", command, reply)
	}
	return reply == "true", nil
}

// require sends command, whose reply must be true.
func (c *client) require(ctx context.Context, command string, payload []byte) error {
	ok, err := c.boolean(ctx, command, payload)
	if err == nil && !ok {
		err = fmt.Errorf("%s: the Neuron refused it", command)
	}
	return err
}

// empty sends command, whose reply must be empty.
func (c *client) empty(ctx context.Context, command string) error {
	reply, err := c.request(ctx, command, nil)
	if err == nil && reply != "" {
		err = fmt.Errorf("%s: unexpected reply %q", command, reply)
	}
	return err
}
