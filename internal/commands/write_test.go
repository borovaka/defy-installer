// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
	"github.com/borovaka/defy-installer/internal/release"
)

func TestFilesAreCheckedBeforeAnyPort(t *testing.T) {
	t.Parallel()
	zmk := write(t, "zmk.hex", ft.Hex(ft.At(0x1000, ft.NeuronProgram(0x2000, 3, 0x1000))))
	full := write(t, "full.hex", ft.Hex(ft.At(0x1000, ft.NeuronProgram(0x1000, 3, 0x1000)), ft.At(0x6C000, make([]byte, 16))))
	s140Layout := write(t, "s140-layout.hex", ft.Hex(ft.At(0x27000, ft.NeuronProgram(64, 3, 0x27000))))
	left := write(t, "left.bin", ft.HalfPackage(ft.ValidHalfApplication(), 7, ft.Left))
	right := write(t, "right.bin", ft.HalfPackage(ft.ValidHalfApplication(), 7, ft.Right))
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"advanced", "neuron", "write", "--hex", s140Layout}, "no vector table at 0x1000"},
		{[]string{"advanced", "neuron", "write", "--hex", full, "--keep-settings"}, "reaches into the ZMK settings"},
		{[]string{"advanced", "neuron", "stock", "--hex", zmk, "--s140", zmk}, "is not Dygma's 2.3.0 Wireless_neuron.hex"},
		{[]string{"advanced", "neuron", "check-s140", "--s140", zmk}, "is not Dygma's n2_s140_nrf52_7.2.0_softdevice.hex"},
		{[]string{"advanced", "halves", "write", "--left", right, "--right", left}, "built for the right half, not the left"},
		{[]string{"advanced", "halves", "write", "--left", left, "--right", zmk}, "factory package"},
		{[]string{"advanced", "halves", "probe", "--timeout", "5s"}, "must be over 0s and under 5s"},
		{[]string{"advanced", "stock", "--files", stockDir(t, zmk, zmk, left)}, "is not Dygma's 2.3.0 Wireless_neuron.hex"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			_, err := run(t, tt.args...)
			wantError(t, err, tt.want)
		})
	}
	// A missing file, in the system's own words.
	_, err := run(t, "advanced", "stock", "--files", t.TempDir())
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("got %v", err)
	}
	wantError(t, err, release.StockNeuronName)
}

// stockDir is a folder with the files stock reads, copied from the paths
// given.
func stockDir(t *testing.T, neuron, s140, keyscanner string) string {
	t.Helper()
	dir := t.TempDir()
	for name, from := range map[string]string{release.StockNeuronName: neuron, release.StockS140Name: s140, release.KeyscannerName: keyscanner} {
		data, err := os.ReadFile(from)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, name), data, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
