// SPDX-License-Identifier: MIT

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/borovaka/defy-installer/internal/flow"
)

func TestExitStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"no error", nil, 0, ""},
		{"an error", errors.New("no such file"), 1, "defy-installer: no such file\n"},
		// The errors of the user's commands are printed bare.
		{"a guided error", &flow.GuidedError{Message: "No Defy found.\nIf Bazecor is open, quit it.", Err: errors.New("no port")},
			1, "No Defy found.\nIf Bazecor is open, quit it.\n"},
		{"a guided interrupt", &flow.GuidedError{Message: "Stopped. Nothing was written.", Err: context.Canceled},
			130, "Stopped. Nothing was written.\n"},
		// Go's "context canceled" reads as "interrupted".
		{"an interrupt", context.Canceled, 130, "defy-installer: interrupted\n"},
		{"an interrupt and an error", fmt.Errorf("%w; %w", context.Canceled, errors.New("the Neuron may hold a partial image")),
			130, "defy-installer: interrupted; the Neuron may hold a partial image\n"},
		{"an interrupted step", fmt.Errorf("uploading 4096 bytes: %w", context.Canceled), 130,
			"defy-installer: uploading 4096 bytes: interrupted\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			if status := exitStatus(&out, tc.err); status != tc.status || out.String() != tc.want {
				t.Errorf("status %d, printed %q; want %d, %q", status, out.String(), tc.status, tc.want)
			}
		})
	}
}

// childEnv makes the test binary, run again, the child that
// TestTheFirstInterruptSaysASecondQuits interrupts.
const childEnv = "DEFY_INSTALLER_INTERRUPT_CHILD"

// TestTheFirstInterruptSaysASecondQuits runs the test binary again, as a
// child that waits for interrupts, and interrupts it twice.
func TestTheFirstInterruptSaysASecondQuits(t *testing.T) {
	if os.Getenv(childEnv) != "" {
		// A watch that stop ends prints nothing.
		_, stop := notifyInterrupts(os.Stderr)
		stop()
		ctx, _ := notifyInterrupts(os.Stderr)
		fmt.Println("waiting")
		<-ctx.Done()
		time.Sleep(time.Minute)
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot send an interrupt to a process")
	}
	if signal.Ignored(os.Interrupt) {
		t.Skip("this process ignores interrupts, and so would the child")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTheFirstInterruptSaysASecondQuits$")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	cmd.Stderr = w
	err = cmd.Start()
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); line != "waiting\n" {
		t.Fatalf("the child printed %q, %v", line, err)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	want := "\nStopping after the current step. Press Ctrl-C again to quit at once.\n"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(stderr, got); err != nil || string(got) != want {
		t.Fatalf("printed %q, %v; want %q", got, err, want)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	rest, _ := io.ReadAll(stderr)
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() ||
		status.Signal() != syscall.SIGINT || len(rest) > 0 {
		t.Errorf("after the second interrupt: %v, and printed %q", err, rest)
	}
}
