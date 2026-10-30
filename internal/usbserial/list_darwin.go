// SPDX-License-Identifier: MIT

package usbserial

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// ioregArgs makes ioreg print, for each serial port in the I/O Kit registry,
// the chain of entries from the registry root down to the port, with their
// properties. A port finds its USB device in that chain however many hubs
// and drivers sit between them.
var ioregArgs = []string{"-a", "-r", "-t", "-l", "-c", "IOSerialBSDClient", "-d", "1"}

func list(ctx context.Context) ([]Port, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", ioregArgs...).Output()
	if err != nil {
		return nil, fmt.Errorf("ioreg: %w", err)
	}
	return parseIoreg(bytes.NewReader(out))
}
