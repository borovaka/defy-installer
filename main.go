// SPDX-License-Identifier: MIT

// Defy-installer installs ZMK firmware on the Dygma Defy wireless keyboard.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/borovaka/defy-installer/internal/commands"
	"github.com/borovaka/defy-installer/internal/flow"
)

func main() {
	ctx, stop := notifyInterrupts(os.Stderr)
	status := exitStatus(os.Stderr, commands.Run(ctx, commands.System(), os.Args))
	stop()
	os.Exit(status)
}

// notifyInterrupts returns a context that the first SIGINT or SIGTERM
// cancels. That signal also restores the default action, so a second one
// quits at once, and prints a notice saying so on w. stop ends the watch.
func notifyInterrupts(w io.Writer) (ctx context.Context, stop context.CancelFunc) {
	ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	context.AfterFunc(ctx, func() {
		// stop ends the context with Canceled as its cause, a signal with
		// its own.
		if context.Cause(ctx) == context.Canceled {
			return
		}
		stop()
		// The terminal shows the interrupt as ^C after the line it was on.
		fmt.Fprintln(w, "\nStopping after the current step. Press Ctrl-C again to quit at once.")
	})
	return ctx, stop
}

// exitStatus prints err on w, if there is one, and returns the program's
// exit status: 0 without an error, 130 after an interrupt and 1 after any
// other error.
func exitStatus(w io.Writer, err error) int {
	if err == nil {
		return 0
	}
	status := 1
	if errors.Is(err, context.Canceled) {
		status = 130
	}
	// The errors of install, update, uninstall and status are worded for
	// the user, and end with what to do next.
	if g, ok := errors.AsType[*flow.GuidedError](err); ok {
		fmt.Fprintln(w, g.Message)
	} else if status == 130 {
		fmt.Fprintln(w, "defy-installer:", interrupted(err))
	} else {
		fmt.Fprintln(w, "defy-installer:", err)
	}
	return status
}

// interrupted returns the message of err, which an interrupt caused, with
// Go's "context canceled" from the context replaced by "interrupted".
func interrupted(err error) string {
	msg := err.Error()
	if !strings.Contains(msg, context.Canceled.Error()) {
		return "interrupted: " + msg
	}
	return strings.ReplaceAll(msg, context.Canceled.Error(), "interrupted")
}
