// SPDX-License-Identifier: MIT

package commands

import "golang.org/x/sys/unix"

// flushInput discards the input waiting in the terminal fd, as
// tcflush(fd, TCIFLUSH) does: TIOCFLUSH with FREAD.
func flushInput(fd uintptr) error {
	const fread = 0x1
	return unix.IoctlSetPointerInt(int(fd), unix.TIOCFLUSH, fread)
}
