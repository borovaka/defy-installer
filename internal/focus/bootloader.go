// SPDX-License-Identifier: MIT

package focus

import (
	"context"
	"fmt"
	"io"
)

// EnterBootloader asks Dygma's 2.3.0 firmware for its bootloader, as
// Bazecor's firmware update does: it starts the updater, waits until the
// updater may flash, then sends upgrade.neuron. Dygma's firmware then
// erases its own settings, the layout among them, and resets into the
// bootloader without replying.
//
// The updater may flash at once when the left half is not cabled or holds
// no valid program; otherwise it waits for the left half's top-left Esc
// key. w sets that wait. upgrade.start goes once, however long the wait.
func EnterBootloader(ctx context.Context, rw io.ReadWriter, w ReadyWait) error {
	c := &client{rw: rw}
	if err := identify(ctx, c); err != nil {
		return err
	}
	if err := c.empty(ctx, "upgrade.start"); err != nil {
		return err
	}
	if err := waitReady(ctx, c, w); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := rw.Write([]byte("upgrade.neuron\n")); err != nil {
		return fmt.Errorf("upgrade.neuron: %w", err)
	}
	return nil
}
