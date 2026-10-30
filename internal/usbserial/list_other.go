// SPDX-License-Identifier: MIT

//go:build !darwin && !linux && !windows

package usbserial

import (
	"context"
	"errors"
)

func list(context.Context) ([]Port, error) {
	return nil, errors.ErrUnsupported
}
