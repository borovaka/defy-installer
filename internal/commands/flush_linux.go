// SPDX-License-Identifier: MIT

package commands

import "golang.org/x/sys/unix"

// flushInput discards the input waiting in the terminal fd, as
// tcflush(fd, TCIFLUSH) does.
func flushInput(fd uintptr) error {
	return unix.IoctlSetInt(int(fd), unix.TCFLSH, unix.TCIFLUSH)
}
