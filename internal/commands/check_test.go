// SPDX-License-Identifier: MIT

package commands

import (
	"testing"

	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
)

func TestCheckNeuron(t *testing.T) {
	t.Parallel()
	path := write(t, "zmk.hex", ft.Hex(ft.At(0x1000, ft.NeuronProgram(0x2000, 3, 0x1000))))
	out, err := run(t, "debug", "check", "--neuron", path)
	wantOutput(t, out, err,
		"zmk.hex: ZMK Neuron image, 0x01000..0x03000 in 2 pages",
		"SHA-256 ",
		"It ends 420 KiB before the ZMK settings, so an update can keep them.")

	path = write(t, "full.hex", ft.Hex(ft.At(0x1000, ft.NeuronProgram(0x1000, 3, 0x1000)), ft.At(0x6C000, make([]byte, 16))))
	out, err = run(t, "debug", "check", "--neuron", path)
	wantOutput(t, out, err, "only a write without --keep-settings, which clears them, installs it")
}

func TestCheckHalves(t *testing.T) {
	t.Parallel()
	left := write(t, "left.bin", ft.HalfPackage(ft.ValidHalfApplication(), 7, ft.Left))
	right := write(t, "right.bin", ft.HalfPackage(ft.ValidHalfApplication(), 7, ft.Right))
	out, err := run(t, "debug", "check", "--left", left, "--right", right)
	wantOutput(t, out, err,
		"left.bin: left package, 4608 bytes, version 0x00000007",
		"right.bin: right package, 4608 bytes, version 0x00000007",
		"Both packages are intact and built for their sides.")

	_, err = run(t, "debug", "check", "--left", left)
	wantError(t, err, "debug check --left and --right go together")
}

func TestCheckStockFiles(t *testing.T) {
	t.Parallel()
	// Only Dygma's files pass; these tests have none, so they check the
	// refusals.
	zmk := write(t, "zmk.hex", ft.Hex(ft.At(0x1000, ft.NeuronProgram(64, 3, 0x1000))))
	_, err := run(t, "debug", "check", "--stock-neuron", zmk, "--s140", zmk)
	wantError(t, err, "is not Dygma's 2.3.0 Wireless_neuron.hex")
	_, err = run(t, "debug", "check", "--s140", zmk)
	wantError(t, err, "debug check --stock-neuron and --s140 go together")

	left := write(t, "left.bin", ft.HalfPackage(ft.ValidHalfApplication(), 7, ft.Left))
	_, err = run(t, "debug", "check", "--keyscanner", left)
	wantError(t, err, "is not Dygma's 2.3.0 keyscanner.bin")
}
