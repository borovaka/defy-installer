// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/borovaka/defy-installer/internal/flow"
	"github.com/borovaka/defy-installer/internal/settings"
)

func TestHelp(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		args []string
		want string
	}{
		{nil, rootHelp},
		{[]string{"adv"}, advancedHelp},
		{[]string{"debug", "--help"}, debugHelp},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			out, err := run(t, tt.args...)
			if err != nil || out != tt.want {
				t.Errorf("got %v\n%s", err, out)
			}
		})
	}
	// A command under advanced keeps cli's own help.
	out, err := run(t, "advanced", "neuron", "--help")
	wantOutput(t, out, err, "defy-installer advanced neuron - write the Neuron", "check-s140")
}

func TestUserCommandsCheckTheirFlagsFirst(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"update", "--from", "alice"}, `--from "alice" is not default or owner/repo`},
		// A repository or file given without its flag is refused, not ignored.
		{[]string{"update", "alice/my-defy"}, `unexpected argument "alice/my-defy"; see defy-installer update --help`},
		{[]string{"advanced", "neuron", "start", "x"}, `unexpected argument "x"; see defy-installer advanced neuron start --help`},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			_, err := run(t, tt.args...)
			wantError(t, err, tt.want)
		})
	}
}

func TestASettingsFileWithAMistakeStops(t *testing.T) {
	t.Parallel()
	mistake := errors.New(`defy-installer.yaml:1: keymap "alice": it has no / between the owner and the repository`)
	d := testDeps(t)
	d.Settings = func() (*settings.File, error) { return nil, mistake }
	for _, command := range []string{"install", "update", "status"} {
		t.Run(command, func(t *testing.T) {
			if _, err := runWith(t, d, command); !errors.Is(err, mistake) {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestUserCommandsWithoutANeuron(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), settings.FileName)
	d := testDeps(t)
	d.Env, d.Settings = withoutNeuron(t), settingsAt(path)
	_, err := runWith(t, d, "uninstall")
	if _, ok := errors.AsType[*flow.GuidedError](err); !ok {
		t.Errorf("got %v", err)
	}
	// status writes nothing, and says what it can without the Neuron or
	// the network.
	out, err := runWith(t, d, "status")
	if want := "Neuron       not found. Plug it into this computer with its USB cable.\n" +
		"Settings: " + path + ", not created yet\n"; err != nil || out != want {
		t.Errorf("got %v\n%s\nwant\n%s", err, out, want)
	}
}

// TestFirmwarePathsAreMadeAbsolute changes the current folder, so it does
// not run in parallel.
func TestFirmwarePathsAreMadeAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	d := testDeps(t)
	cmd := d.rootCommand()
	var opts flow.Options
	cmd.Command("update").Action = func(ctx context.Context, c *cli.Command) error {
		var err error
		opts, err = d.userOptions(ctx, c, true)
		return err
	}
	if err := cmd.Run(t.Context(), []string{"defy-installer", "update", "--firmware", "build", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(opts.Firmware) || filepath.Base(opts.Firmware) != "build" || !opts.Yes || opts.Command != "update" {
		t.Errorf("options %+v", opts)
	}
}

func TestInterruptibleReads(t *testing.T) {
	t.Parallel()
	// Reads pass through, in parts as small as the caller asks for.
	r := interruptible(t.Context(), strings.NewReader("y\nmore"))
	got, err := io.ReadAll(io.LimitReader(r, 100))
	if err != nil || string(got) != "y\nmore" {
		t.Errorf("got %q, %v", got, err)
	}
	small := interruptible(t.Context(), strings.NewReader("abcdef"))
	buf := make([]byte, 2)
	var parts []string
	for {
		n, err := small.Read(buf)
		parts = append(parts, string(buf[:n]))
		if err != nil {
			break
		}
	}
	if strings.Join(parts, "") != "abcdef" {
		t.Errorf("parts %q", parts)
	}

	// A read that waits stops when the context ends.
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(t.Context())
	r = interruptible(ctx, pr)
	done := make(chan error)
	go func() {
		_, err := r.Read(make([]byte, 8))
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
	// Once the context has ended, a read reads nothing.
	ended := interruptible(ctx, strings.NewReader("y\n"))
	if n, err := ended.Read(make([]byte, 8)); n > 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("after the end, read %d bytes, %v", n, err)
	}
}

// TestTypedAheadInputIsDiscarded: on a terminal, the input typed before a
// prompt is flushed. Other input is all read.
func TestTypedAheadInputIsDiscarded(t *testing.T) {
	t.Parallel()
	var flushed []string
	flush := func(f *os.File) error {
		flushed = append(flushed, f.Name())
		return nil
	}
	if discarder(strings.NewReader(""), flush) != nil {
		t.Error("input that is not a terminal is discarded")
	}
	// A character device stands in for the terminal.
	tty, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	discard := discarder(tty, flush)
	if discard == nil {
		t.Fatal("a terminal's input is not discarded")
	}
	discard()
	if !slices.Equal(flushed, []string{os.DevNull}) {
		t.Errorf("flushed %q", flushed)
	}
}

func TestFlushingATerminal(t *testing.T) {
	t.Parallel()
	if !slices.Contains([]string{"darwin", "linux", "windows"}, runtime.GOOS) {
		t.Skip("the installer flushes terminals on macOS, Linux and Windows")
	}
	// The system's call reaches the file, which refuses it, since it is
	// not a terminal.
	f, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := flushTerminal(f); err == nil {
		t.Errorf("flushed a plain file")
	}
}
