// SPDX-License-Identifier: MIT

package usbserial

import (
	"context"
	"os"
)

func list(context.Context) ([]Port, error) {
	return listSysfs(os.DirFS("/sys"))
}
