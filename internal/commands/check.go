// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/urfave/cli/v3"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/release"
)

func checkCommand() *cli.Command {
	return &cli.Command{
		Name:  "check",
		Usage: "check firmware files without a keyboard",
		Description: "Runs the checks a write runs before it opens a port, on one kind of firmware file: " +
			"a ZMK Neuron image, the two halves' packages, or Dygma's 2.3.0 files, which uninstall writes.",
		MutuallyExclusiveFlags: []cli.MutuallyExclusiveFlags{{
			Required: true,
			Flags: [][]cli.Flag{
				{fileFlag("neuron", "a ZMK Neuron image (.hex)", false)},
				{fileFlag("left", "the left half's package (.bin)", false),
					fileFlag("right", "the right half's package (.bin)", false)},
				{fileFlag("stock-neuron", "Dygma's 2.3.0 "+release.StockNeuronName+", with --s140", false),
					fileFlag("s140", "Dygma's 2.3.0 "+release.StockS140Name, false)},
				{fileFlag("keyscanner", "Dygma's 2.3.0 "+release.KeyscannerName+", for both halves", false)},
			},
		}},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			w := cmd.Root().Writer
			switch {
			case cmd.IsSet("neuron"):
				return checkNeuron(w, cmd.String("neuron"))
			case cmd.IsSet("left") || cmd.IsSet("right"):
				if !cmd.IsSet("left") || !cmd.IsSet("right") {
					return errors.New("debug check --left and --right go together")
				}
				return checkHalves(w, cmd.String("left"), cmd.String("right"))
			case cmd.IsSet("stock-neuron") || cmd.IsSet("s140"):
				if !cmd.IsSet("stock-neuron") || !cmd.IsSet("s140") {
					return errors.New("debug check --stock-neuron and --s140 go together")
				}
				return checkStockNeuron(w, cmd.String("stock-neuron"), cmd.String("s140"))
			}
			return checkKeyscanner(w, cmd.String("keyscanner"))
		},
	}
}

func checkNeuron(w io.Writer, path string) error {
	im, err := firmware.LoadNeuronImage(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s: ZMK Neuron image, %s in %d pages\n", im.Name, extent(firmware.NeuronCodeStart, im.End), len(im.Pages))
	fmt.Fprintf(w, "SHA-256 %x\n", im.SHA256)
	if _, err := im.EraseEnd(true); err != nil {
		_, err = fmt.Fprintln(w, "It reaches into the ZMK settings, so only a write without --keep-settings, which "+
			"clears them, installs it.")
		return err
	}
	_, err = fmt.Fprintf(w, "It ends %d KiB before the ZMK settings, so an update can keep them.\n",
		(firmware.NeuronSettingsStart-im.End)/1024)
	return err
}

func checkHalves(w io.Writer, left, right string) error {
	sides := [...]firmware.Side{firmware.Left, firmware.Right}
	paths := [...]string{left, right}
	var halves [2]*firmware.HalfImage
	for i, side := range sides {
		h, err := firmware.LoadHalfImage(paths[i])
		if err != nil {
			return err
		}
		if err := h.RequireApplication(side); err != nil {
			return err
		}
		halves[i] = h
	}
	for i, h := range halves {
		fmt.Fprintf(w, "%s: %s package, %d bytes, version 0x%08x, program CRC 0x%08x\n",
			h.Name, sides[i], len(h.Data), h.Version, h.ProgramCRC)
	}
	_, err := fmt.Fprintln(w, "Both packages are intact and built for their sides. "+
		"Their metadata does not show where the firmware came from.")
	return err
}

func checkStockNeuron(w io.Writer, path, s140Path string) error {
	stock, err := firmware.LoadStockNeuron(path, s140Path)
	if err != nil {
		return err
	}
	sd := stock.SoftDevice
	fmt.Fprintf(w, "%s: Dygma's 2.3.0 Neuron firmware, %s in %d pages\n",
		stock.Name, extent(firmware.NeuronStockStart, stock.End), len(stock.Pages))
	fmt.Fprintf(w, "%s: Nordic's S140 7.2.0, %s in %d pages\n", sd.Name, extent(firmware.NeuronCodeStart, sd.End), len(sd.Pages))
	_, err = fmt.Fprintln(w, "Both are Dygma's 2.3.0 files: each matches its pinned SHA-256.")
	return err
}

func checkKeyscanner(w io.Writer, path string) error {
	h, err := firmware.LoadHalfImage(path)
	if err != nil {
		return err
	}
	if err := h.RequireStock(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s: Dygma's 2.3.0 keyscanner for both halves, %d bytes, version 0x%08x. It matches the pinned SHA-256.\n",
		h.Name, len(h.Data), h.Version)
	return err
}

func extent(start, end uint32) string {
	return fmt.Sprintf("0x%05x..0x%05x", start, end)
}
