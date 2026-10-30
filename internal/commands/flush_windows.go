// SPDX-License-Identifier: MIT

package commands

import "golang.org/x/sys/windows"

// flushInput discards the input waiting in the console fd.
func flushInput(fd uintptr) error {
	return windows.FlushConsoleInputBuffer(windows.Handle(fd))
}
