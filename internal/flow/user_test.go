// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
)

func TestBusyPortsWaitForEnter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		u.d.busy = 1
		out, err := u.run(Install, "\n")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out,
			"Another program, usually Bazecor or ZMK Studio in a browser, is using the keyboard. Quit it, then press "+
				"Enter to try again.",
			"Found your Defy. It runs ZMK from alice/my-defy, commit 3f2a91c.", "Done.")
	})
}

func TestNoDefy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, f := range []func(context.Context, Env, Options) error{Install, Uninstall} {
			u := newUserTest(t, "", "install")
			out, err := u.run(f, "")
			if err == nil || err.Error() != "No Defy found. Plug the Neuron into this computer with its USB cable.\n"+
				"If Bazecor is open, quit it." || out != "" {
				t.Errorf("got %v, printed %q", err, out)
			}
		}
	})
}

func TestVerboseRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// --verbose adds the detail of each step, and bars print a line at
		// each quarter even on a terminal, since detail lines come between.
		u, _, _ := zmkUpdate(t, "alice/my-defy", 0xa5, 0xa5)
		u.opts.Verbose, u.opts.Terminal = true, true
		out, err := u.run(Install, "")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "VERSION: module v0.2.0, source alice/my-defy, commit "+commitA+".",
			"Entered the Neuron's bootloader from ZMK through /dev/studio.")
		if strings.Contains(out, "\r") {
			t.Errorf("a bar was drawn in place:\n%q", out)
		}
	})
}
