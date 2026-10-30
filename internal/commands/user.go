// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/borovaka/defy-installer/internal/flow"
	"github.com/borovaka/defy-installer/internal/release"
)

func fromFlag(usage string) cli.Flag {
	return &cli.StringFlag{Name: "from", Usage: usage}
}

func firmwareFlag(usage string) cli.Flag {
	return &cli.StringFlag{Name: "firmware", Usage: usage, TakesFile: true}
}

func yesFlag(usage string) cli.Flag {
	return &cli.BoolFlag{Name: "yes", Usage: usage + ". Steps that need your hands still wait for Enter"}
}

func verboseFlag() cli.Flag {
	return &cli.BoolFlag{Name: "verbose", Usage: "print the detail of each step: addresses, CRCs and ports"}
}

// sourceFlags are --from and --firmware, which choose the build to
// install; at most one may be given.
func sourceFlags(fromUsage, firmwareUsage string) []cli.MutuallyExclusiveFlags {
	return []cli.MutuallyExclusiveFlags{{Flags: [][]cli.Flag{{fromFlag(fromUsage)}, {firmwareFlag(firmwareUsage)}}}}
}

// installCommand returns install or update, which differ only in name
// and usage.
func (d Deps) installCommand(name, usage string) *cli.Command {
	return &cli.Command{
		Name:  name,
		Usage: usage,
		Description: "Both install and update do what the keyboard needs. On Dygma's firmware they install ZMK, " +
			"which erases the layout stored on the keyboard: back it up in Bazecor first. On ZMK they update it and " +
			"keep your Bluetooth pairings; changes made in ZMK Studio are lost. On a Neuron showing its DEFY drive they " +
			"start ZMK and update it; if the drive comes back instead, they write the Neuron again and keep its " +
			"settings. A Neuron left in its bootloader by a write that did not finish is written again, which clears " +
			"its settings.\n\n" +
			"The build comes from --firmware or --from, then from the settings file, defy-installer.yaml, then from " +
			"the keymap repository the keyboard runs, then from the default layout's latest release. --from and " +
			"--firmware are saved in the settings file once the keyboard runs that build.",
		MutuallyExclusiveFlags: sourceFlags(
			"the keymap to install: default, or your keymap repository as owner/repo, each with an optional @tag "+
				"to stay on that release. Saved for later updates",
			"a zip or a folder of firmware files on this computer, to install instead of a release on GitHub. "+
				"Saved for later updates"),
		Flags: []cli.Flag{
			yesFlag("answer yes to the questions, such as whether you backed up your layout. A keyboard that is up " +
				"to date is not written again"),
			verboseFlag(),
		},
		Action: d.guided(flow.Install, true),
	}
}

func (d Deps) uninstallCommand() *cli.Command {
	return &cli.Command{
		Name:  "uninstall",
		Usage: "put Dygma's 2.3.0 firmware back",
		Description: "Puts Dygma's 2.3.0 firmware back on the whole keyboard: the Neuron, then both halves. It " +
			"downloads Dygma's files from Dygma's GitHub, or reads them from the --firmware folder, and checks each " +
			"against its pinned SHA-256 before it writes anything. ZMK's Bluetooth pairings and any changes made in " +
			"ZMK Studio are lost.",
		Flags: []cli.Flag{
			firmwareFlag("a folder with Dygma's 2.3.0 files, " + release.StockNeuronName + ", " + release.StockS140Name +
				" and " + release.KeyscannerName + ", to use instead of downloading them. Not saved"),
			yesFlag("answer yes when asked whether to continue"), verboseFlag(),
		},
		Action: d.guided(flow.Uninstall, false),
	}
}

func (d Deps) statusCommand() *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "show what the keyboard runs, whether the halves are connected, and their batteries",
		Description: "Writes nothing. It also shows the latest build of the keymap an update installs, when GitHub " +
			"can be reached, and the settings file in use. --from and --firmware show another build's, and are not " +
			"saved.",
		MutuallyExclusiveFlags: sourceFlags("the keymap to compare with the keyboard, in place of the one an update "+
			"installs: default, or owner/repo, each with an optional @tag. Not saved",
			"a zip or a folder of firmware files on this computer, to compare with the keyboard. Not saved"),
		Flags:  []cli.Flag{verboseFlag()},
		Action: d.guided(flow.Status, true),
	}
}

// guided returns the action of install, update, uninstall or status, which
// runs userFlow with the command's options, and with withSettings the
// settings file.
func (d Deps) guided(userFlow func(context.Context, flow.Env, flow.Options) error, withSettings bool) cli.ActionFunc {
	return func(ctx context.Context, cmd *cli.Command) error {
		opts, err := d.userOptions(ctx, cmd, withSettings)
		if err != nil {
			return err
		}
		return userFlow(ctx, d.env(cmd), opts)
	}
}

// userOptions reads the flags of install, update, uninstall and status,
// and with withSettings the settings file. It checks --from before
// anything reaches the keyboard.
func (d Deps) userOptions(ctx context.Context, cmd *cli.Command, withSettings bool) (flow.Options, error) {
	root := cmd.Root()
	opts := flow.Options{
		Command:  cmd.Name,
		Yes:      cmd.Bool("yes"),
		Verbose:  cmd.Bool("verbose"),
		Releases: d.Releases,
		In:       interruptible(ctx, root.Reader),
		Echo:     !isTerminal(root.Reader),
		Discard:  discarder(root.Reader, d.FlushTerminal),
		Terminal: isTerminal(root.Writer),
	}
	if from := cmd.String("from"); from != "" {
		src, err := release.ParseSource(from)
		if err != nil {
			return opts, fmt.Errorf("--from %w", err)
		}
		opts.From = src
	}
	if path := cmd.String("firmware"); path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return opts, err
		}
		opts.Firmware = abs
	}
	if withSettings {
		s, err := d.Settings()
		if err != nil {
			return opts, err
		}
		opts.Settings = s
	}
	return opts, nil
}

// isTerminal reports whether f is a terminal, as a character device is.
func isTerminal(f any) bool {
	file, ok := f.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// flushTerminal discards the input waiting in the terminal f, with
// flushInput, the call each system has for it. It leaves f's blocking
// mode as it is, which Fd would change.
func flushTerminal(f *os.File) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var flushErr error
	if err := conn.Control(func(fd uintptr) { flushErr = flushInput(fd) }); err != nil {
		return err
	}
	return flushErr
}

// discarder returns what discards the input typed ahead into the terminal
// f, with flush. It returns nil when f is not a terminal, so that every
// answer a script gives is read.
func discarder(f io.Reader, flush func(*os.File) error) func() {
	file, ok := f.(*os.File)
	if !ok || !isTerminal(file) {
		return nil
	}
	return func() {
		// Input that a failed flush leaves is read as before.
		_ = flush(file)
	}
}

// interruptible returns r, whose reads stop when ctx ends, so that an
// interrupt stops a wait for Enter. A read cut short goes on in the
// background, and what it reads is dropped, since the run is over.
func interruptible(ctx context.Context, r io.Reader) io.Reader {
	return ctxReader{ctx, r}
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	type result struct {
		n   int
		err error
	}
	buf := make([]byte, len(p))
	done := make(chan result, 1)
	go func() {
		n, err := c.r.Read(buf)
		done <- result{n, err}
	}()
	select {
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	case res := <-done:
		return copy(p, buf[:res.n]), res.err
	}
}
