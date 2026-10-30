// SPDX-License-Identifier: MIT

package focus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/focus/focustest"
)

func TestEnterBootloaderAsksAsBazecorDoes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := focustest.New(t)
		f.ReadyAfter = 1
		waits := 0
		if err := EnterBootloader(t.Context(), f, ReadyWait{Waiting: func() { waits++ }}); err != nil {
			t.Fatal(err)
		}
		if waits != 1 {
			t.Errorf("waiting ran %d times", waits)
		}
		want := []string{"help", "hardware.version", "version", "upgrade.start", "upgrade.isReady", "upgrade.isReady", "upgrade.neuron"}
		if !slices.Equal(f.Commands, want) || !f.Rebooted {
			t.Errorf("sent %q", f.Commands)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// Another firmware version: nothing is started.
		f := focustest.New(t)
		f.Version = "v2.2.1"
		wantError(t, EnterBootloader(t.Context(), f, ReadyWait{}), "unsupported Neuron firmware version")
		if slices.Contains(f.Commands, "upgrade.start") || f.Rebooted {
			t.Errorf("sent %q", f.Commands)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// Dygma's updater does not start: upgrade.neuron, which erases
		// Dygma's layout, is never sent.
		f := focustest.New(t)
		f.SilentOn = "upgrade.start"
		wantError(t, EnterBootloader(t.Context(), f, ReadyWait{}), "upgrade.start: no complete reply in time")
		if slices.Contains(f.Commands, "upgrade.neuron") || f.Rebooted {
			t.Errorf("sent %q", f.Commands)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// Ready at once: nothing to wait for.
		f := focustest.New(t)
		if err := EnterBootloader(t.Context(), f, ReadyWait{Waiting: func() { t.Error("waiting ran") }}); err != nil {
			t.Fatal(err)
		}
	})
}

// The Esc key may come late: each time the limit passes, Expired runs, and
// the wait goes on without another upgrade.start.
func TestEnterBootloaderWaitsAgainAfterTheLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := focustest.New(t)
		// Polled every 250 ms, the updater gets ready in its second
		// minute.
		f.ReadyAfter = 300
		waits, expiries := 0, 0
		start := time.Now()
		err := EnterBootloader(t.Context(), f, ReadyWait{
			Waiting: func() { waits++ },
			Limit:   time.Minute,
			Expired: func(context.Context) error { expiries++; return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if waits != 1 || expiries != 1 || time.Since(start) < time.Minute {
			t.Errorf("waiting ran %d times and expired %d times in %v", waits, expiries, time.Since(start))
		}
		if n := strings.Count(strings.Join(f.Commands, "\n"), "upgrade.start"); n != 1 || !f.Rebooted {
			t.Errorf("sent upgrade.start %d times: %q", n, f.Commands)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// An error from Expired stops the wait, and nothing is entered.
		f := focustest.New(t)
		f.ReadyAfter = 1000
		stop := errors.New("stopped")
		err := EnterBootloader(t.Context(), f, ReadyWait{
			Limit:   time.Second,
			Expired: func(context.Context) error { return stop },
		})
		if !errors.Is(err, stop) || f.Rebooted {
			t.Errorf("got %v; rebooted %v", err, f.Rebooted)
		}
	})
}
