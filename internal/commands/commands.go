// SPDX-License-Identifier: MIT

// Package commands defines the command line: commands, flags and output.
package commands

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/borovaka/defy-installer/internal/flow"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/settings"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// Deps holds the commands' links to the keyboard, the settings file,
// GitHub and the terminal. Tests supply their own.
type Deps struct {
	// Env is how the flows reach the keyboard; env gives each command's
	// flow its Out.
	Env flow.Env
	// Settings finds and reads the settings file.
	Settings func() (*settings.File, error)
	// Releases finds builds and Dygma's files.
	Releases flow.Releases
	// FlushTerminal discards the input waiting in the terminal f.
	FlushTerminal func(f *os.File) error
}

// System returns the Deps of the running program: the system's USB serial
// ports, opened with usbserial.Open, the settings file settings.Load
// finds, GitHub, and the terminal.
func System() Deps {
	return Deps{
		Env: flow.Env{
			List: usbserial.List,
			Open: func(path string, timeout time.Duration) (flow.Port, error) {
				conn, err := usbserial.Open(path)
				if err != nil {
					return nil, err
				}
				conn.SetWriteTimeout(min(timeout, maxWriteTimeout))
				return conn, nil
			},
		},
		Settings:      settings.Load,
		Releases:      &release.Client{},
		FlushTerminal: flushTerminal,
	}
}

// maxWriteTimeout caps each port write at usbserial.Open's own limit, so
// that a flow's longer timeout never lets a stalled write wait longer.
const maxWriteTimeout = 2 * time.Second

// env returns d.Env with cmd's output as Out, for cmd's flow.
func (d Deps) env(cmd *cli.Command) flow.Env {
	env := d.Env
	env.Out = cmd.Root().Writer
	return env
}

// rootHelp is the help the installer prints with help, or with no command.
const rootHelp = `Install ZMK on a Dygma Defy wireless, keep it up to date, or put Dygma's firmware back.

  install     Install ZMK
  update      Update ZMK, keeping your Bluetooth pairings
  uninstall   Put Dygma's 2.3.0 firmware back
  status      Show what the keyboard runs, whether the halves are connected, and their batteries

Your own keymap:   defy-installer update --from yourname/your-repo
Troubleshooting:   defy-installer debug help
Developers:        defy-installer advanced help
`

// Run runs the command line args with d, and returns the error for main to
// print.
func Run(ctx context.Context, d Deps, args []string) error {
	return runRoot(ctx, d.rootCommand(), args)
}

func (d Deps) rootCommand() *cli.Command {
	return &cli.Command{
		Name:                          "defy-installer",
		Usage:                         "install ZMK on the Dygma Defy wireless",
		Version:                       release.InstallerVersion(),
		CustomRootCommandHelpTemplate: rootHelp,
		Commands: []*cli.Command{
			d.installCommand("install", "install ZMK"),
			d.installCommand("update", "update ZMK, keeping your Bluetooth pairings"),
			d.uninstallCommand(),
			d.statusCommand(),
			d.advancedCommand(),
			d.debugCommand(),
		},
	}
}

// runRoot runs root with args. cli gives CommandNotFound no way to return
// an error, so the error of an unknown command waits until root.Run
// returns.
func runRoot(ctx context.Context, root *cli.Command, args []string) error {
	var unknown error
	setUsageError(root, func(_ context.Context, cmd *cli.Command, name string) {
		unknown = unknownCommand(cmd, name)
	})
	err := root.Run(ctx, args)
	if unknown != nil {
		return unknown
	}
	return err
}

// advancedHelp is the help advanced prints in place of cli's.
const advancedHelp = `Direct access for developers. Single steps, with no guidance and no downloads.

  neuron write | stock | bootloader | start | check-s140
  halves write | probe
  stock --files <folder>
`

func (d Deps) advancedCommand() *cli.Command {
	return &cli.Command{
		Name:               "advanced",
		Aliases:            []string{"adv"},
		Usage:              "direct access for developers: write the Neuron, the halves, or Dygma's firmware",
		CustomHelpTemplate: advancedHelp,
		Commands:           []*cli.Command{d.neuronCommand(), d.halvesCommand(), d.stockCommand()},
	}
}

// debugHelp is the help debug prints in place of cli's.
const debugHelp = `Read-only checks, safe at any time. Nothing here changes the keyboard.

  ports       The USB serial ports, and which are the Neuron's
  check       Check firmware files without a keyboard
`

func (d Deps) debugCommand() *cli.Command {
	return &cli.Command{
		Name:               "debug",
		Usage:              "read-only checks, safe at any time",
		CustomHelpTemplate: debugHelp,
		Commands:           []*cli.Command{d.portsCommand(), checkCommand()},
	}
}

// init changes cli's global ShowSubcommandHelp. cli shows a command that
// has subcommands with a template of its own, which ignores the command's
// CustomHelpTemplate; advanced and debug have their own.
func init() {
	cli.ShowSubcommandHelp = func(cmd *cli.Command) error {
		if cmd.CustomHelpTemplate == "" {
			return cli.DefaultShowSubcommandHelp(cmd)
		}
		cli.HelpPrinter(cmd.Root().Writer, cmd.CustomHelpTemplate, cmd)
		return nil
	}
}

// setUsageError makes cmd and its subcommands return usage errors with a
// pointer to the help, instead of printing the help themselves: main
// prints the error, once. Commands with subcommands report an unknown one
// to notFound; the others refuse arguments, since none takes any, and a
// repository or file given without its flag would otherwise be ignored.
func setUsageError(cmd *cli.Command, notFound cli.CommandNotFoundFunc) {
	cmd.OnUsageError = func(_ context.Context, cmd *cli.Command, err error, _ bool) error {
		return fmt.Errorf("%w; see %s --help", err, cmd.FullName())
	}
	if len(cmd.Commands) > 0 {
		cmd.CommandNotFound = notFound
	} else {
		cmd.ArgValidator = noArguments
	}
	for _, sub := range cmd.Commands {
		setUsageError(sub, notFound)
	}
}

// noArguments refuses any argument that is not a flag.
func noArguments(_ context.Context, cmd *cli.Command) error {
	if cmd.Args().Present() {
		return fmt.Errorf("unexpected argument %q; see %s --help", cmd.Args().First(), cmd.FullName())
	}
	return nil
}

// unknownCommand returns the error for name, an unknown subcommand of
// cmd.
func unknownCommand(cmd *cli.Command, name string) error {
	return fmt.Errorf("unknown command %q; see %s --help", name, cmd.FullName())
}
