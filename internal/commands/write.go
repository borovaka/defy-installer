// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/flow"
	"github.com/borovaka/defy-installer/internal/release"
)

// fileFlag returns a flag that takes a path.
func fileFlag(name, usage string, required bool) *cli.StringFlag {
	return &cli.StringFlag{Name: name, Usage: usage, TakesFile: true, Required: required}
}

func between(lo, hi time.Duration) func(time.Duration) error {
	return func(d time.Duration) error {
		if d <= lo || d >= hi {
			return fmt.Errorf("must be over %v and under %v", lo, hi)
		}
		return nil
	}
}

// serialUsage is --serial's usage, for the neuron and the halves commands.
const serialUsage = "the USB serial number of the Neuron running ZMK, as debug ports lists it (default: the one connected)"

// neuronFlags choose the Neuron a neuron command works on.
func neuronFlags(extra ...cli.Flag) []cli.Flag {
	return append(extra,
		&cli.StringFlag{Name: "port", Usage: "the Neuron's Studio port, its UF2 loader's port, or its bootloader's port " +
			"(default: the one Neuron connected)"},
		&cli.StringFlag{Name: "serial", Usage: serialUsage},
		&cli.DurationFlag{Name: "timeout", Value: flow.DefaultNeuronTimeout, Validator: between(0, 10*time.Minute),
			Usage: "how long the bootloader, or the firmware after it, may take to appear"},
	)
}

func neuronOf(cmd *cli.Command) flow.Neuron {
	return flow.Neuron{Port: cmd.String("port"), Serial: cmd.String("serial"), Timeout: cmd.Duration("timeout")}
}

func (d Deps) neuronCommand() *cli.Command {
	return &cli.Command{
		Name:  "neuron",
		Usage: "write the Neuron through its bootloader, or put Dygma's firmware back on it",
		Description: "A Neuron running ZMK enters its bootloader through its Studio port, and one showing its DEFY " +
			"drive through its UF2 loader's port. From Dygma's firmware, neuron write asks it for its bootloader, as " +
			"Bazecor does; the other commands need the Neuron in its bootloader, with the button.",
		Commands: []*cli.Command{
			{
				Name:  "write",
				Usage: "write a ZMK image, start it and wait for it to come back",
				Description: "Dygma's firmware erases its settings, which hold the layout, on its way into the " +
					"bootloader. Back them up with Bazecor first.",
				Flags: neuronFlags(
					fileFlag("hex", "the ZMK Neuron image (.hex)", true),
					&cli.BoolFlag{Name: "keep-settings", Usage: "keep the ZMK settings and pairings at 0x6C000..0x72000"},
				),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					im, err := firmware.LoadNeuronImage(cmd.String("hex"))
					if err != nil {
						return err
					}
					return flow.WriteNeuron(ctx, d.env(cmd), neuronOf(cmd), im, cmd.Bool("keep-settings"))
				},
			},
			{
				Name:  "stock",
				Usage: "write Dygma's 2.3.0 firmware and S140 back, start it and wait for it to come back",
				Flags: neuronFlags(
					fileFlag("hex", "Dygma's 2.3.0 "+release.StockNeuronName, true),
					fileFlag("s140", "Dygma's 2.3.0 "+release.StockS140Name, true),
				),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					stock, err := firmware.LoadStockNeuron(cmd.String("hex"), cmd.String("s140"))
					if err != nil {
						return err
					}
					return flow.RestoreStock(ctx, d.env(cmd), neuronOf(cmd), stock)
				},
			},
			{
				Name:  "check-s140",
				Usage: "compare S140 on the Neuron with Dygma's, writing nothing, then start the firmware",
				Flags: neuronFlags(fileFlag("s140", "Dygma's 2.3.0 "+release.StockS140Name, true)),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					sd, err := firmware.LoadSoftDevice(cmd.String("s140"))
					if err != nil {
						return err
					}
					return flow.CheckS140(ctx, d.env(cmd), neuronOf(cmd), sd)
				},
			},
			{
				Name:  "bootloader",
				Usage: "enter the bootloader and identify it, writing nothing",
				Flags: neuronFlags(),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return flow.EnterBootloader(ctx, d.env(cmd), neuronOf(cmd))
				},
			},
			{
				Name:  "start",
				Usage: "start the firmware from the bootloader and wait for it to come back",
				Flags: neuronFlags(),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return flow.StartNeuron(ctx, d.env(cmd), neuronOf(cmd))
				},
			},
		},
	}
}

// halvesFlags choose the half updater a halves command uses.
func halvesFlags(extra ...cli.Flag) []cli.Flag {
	return append(extra,
		&cli.StringFlag{Name: "port", Usage: "the half updater's port (default: the one that answers among the Neuron's)"},
		&cli.StringFlag{Name: "serial", Usage: serialUsage},
		&cli.DurationFlag{Name: "timeout", Value: flow.DefaultHalvesTimeout, Validator: between(0, 5*time.Second),
			Usage: "how long to wait for each reply from the updater"},
	)
}

func halvesOf(cmd *cli.Command) flow.Halves {
	return flow.Halves{Port: cmd.String("port"), Serial: cmd.String("serial"), Timeout: cmd.Duration("timeout")}
}

func (d Deps) halvesCommand() *cli.Command {
	return &cli.Command{
		Name:  "halves",
		Usage: "write the halves through the half updater of a Neuron running ZMK",
		Description: "Cable both halves to the Neuron and switch both off. The Neuron keeps its firmware and " +
			"settings, except that halves write resets the split pairing when the Neuron is not paired with both " +
			"halves, or with --reset-pairing. Typing pauses while the halves are written.",
		Commands: []*cli.Command{
			{
				Name:  "write",
				Usage: "write both halves",
				Description: "Before writing, the installer reads the Neuron's split pairing. If the Neuron is not " +
					"paired with both halves, as happens after a neuron write without --keep-settings or on a first " +
					"install, the session resets the pairing before the writes. It clears the Neuron's split bonds and " +
					"erases both halves' settings, which hold their Bluetooth identity and bonds. Each half then pairs " +
					"with the Neuron as a new device, with its cable in or out. --reset-pairing " +
					"resets the pairing even when the Neuron is paired with both halves.",
				Flags: halvesFlags(
					fileFlag("left", "the left half's package (.bin)", true),
					fileFlag("right", "the right half's package (.bin)", true),
					&cli.BoolFlag{Name: "reset-pairing", Usage: "reset the split pairing even when the Neuron is paired with both halves"},
				),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					left, err := firmware.LoadHalfImage(cmd.String("left"))
					if err != nil {
						return err
					}
					right, err := firmware.LoadHalfImage(cmd.String("right"))
					if err != nil {
						return err
					}
					return flow.WriteHalves(ctx, d.env(cmd), halvesOf(cmd), left, right, cmd.Bool("reset-pairing"))
				},
			},
			{
				Name:  "probe",
				Usage: "read the split pairing and both halves' factory bootloaders, writing nothing",
				Flags: halvesFlags(),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return flow.ProbeHalves(ctx, d.env(cmd), halvesOf(cmd))
				},
			},
		},
	}
}

func (d Deps) stockCommand() *cli.Command {
	return &cli.Command{
		Name:  "stock",
		Usage: "put Dygma's 2.3.0 firmware back on the whole keyboard: the Neuron, then both halves",
		Description: "Reads Dygma's 2.3.0 files, " + release.StockNeuronName + ", " + release.StockS140Name + " and " +
			release.KeyscannerName + ", from the --files folder, and checks each against its pinned SHA-256 before " +
			"it opens a port. It writes the Neuron, then both halves through Dygma's Neuron firmware. A Neuron that " +
			"already runs Dygma's firmware goes straight to the halves, so a run that stopped can be run again. Cable " +
			"both halves to the Neuron and switch them off. The ZMK settings are cleared. Dygma's own settings, which " +
			"hold the layout, are not written; if Dygma's firmware erased them on its way into the bootloader, " +
			"restore them from a Bazecor backup.",
		Flags: neuronFlags(fileFlag("files", "the folder with Dygma's three 2.3.0 files", true)),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			stock, err := d.Releases.LoadStock(cmd.String("files"))
			if err != nil {
				return err
			}
			return flow.RestoreKeyboard(ctx, d.env(cmd), neuronOf(cmd), stock)
		},
	}
}
