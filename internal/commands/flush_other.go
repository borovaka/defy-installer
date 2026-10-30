// SPDX-License-Identifier: MIT

//go:build !darwin && !linux && !windows

package commands

// flushInput does nothing on the systems the installer is not built for.
func flushInput(uintptr) error {
	return nil
}
