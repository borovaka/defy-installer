// SPDX-License-Identifier: MIT

// Package flow runs the installer's commands that reach the keyboard: the
// user's commands, install, update, uninstall and status, and the advanced
// ones they are built from, which write the Neuron through its bootloader
// and the halves through the ZMK Neuron's half updater. Each flow checks
// its images before it writes anything, drives the neuron, updater and
// focus packages, and reports as it goes.
package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/updater"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// Port is an open serial port; *usbserial.Conn is one.
type Port interface {
	io.ReadWriter
	SetDTR(on bool) error
	SetBaudRate(baud int) error
	ResetInput() error
	Close() error
}

// Env is how a flow reaches the keyboard, and where it reports.
type Env struct {
	List usbserial.Lister
	// Open opens the port at path at 115200 baud with DTR on. timeout is the
	// flow's own; Open may bound the port's writes by it.
	Open func(path string, timeout time.Duration) (Port, error)
	// Out takes the flow's report, a line at a time.
	Out io.Writer
	// Rand is where updater session nonces come from; nil is crypto/rand.
	Rand io.Reader
}

func (env Env) say(format string, args ...any) {
	fmt.Fprintf(env.Out, format+"\n", args...)
}

// openSession opens a port for a session and drops whatever input waits
// there from before.
func (env Env) openSession(path string, timeout time.Duration) (Port, error) {
	p, err := env.Open(path, timeout)
	if err != nil {
		return nil, err
	}
	if err := p.ResetInput(); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
}

// updaterOpener returns an updater.Opener. With flush it drops waiting
// input, for a session; without, for Identify, which sends HELLO alone.
func (env Env) updaterOpener(flush bool) updater.Opener {
	open := env.Open
	if flush {
		open = env.openSession
	}
	return func(path string, timeout time.Duration) (updater.Conn, error) { return open(path, timeout) }
}

// entry returns a neuron.Entry that reaches the keyboard through env.
func (env Env) entry(timeout time.Duration) *neuron.Entry {
	return &neuron.Entry{
		List: env.List,
		IsUpdater: func(ctx context.Context, p usbserial.Port) (bool, error) {
			return updater.Identify(ctx, p, env.updaterOpener(false))
		},
		Touch: func(path string) error {
			p, err := env.Open(path, timeout)
			if err != nil {
				return err
			}
			return neuron.Touch(p)
		},
		Timeout: timeout,
	}
}
