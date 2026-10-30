// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
)

// bubble runs f as the subtest name, in a synctest bubble of its own, so
// that a failure names its case and the cases after it still run.
func bubble(t *testing.T, name string, f func(t *testing.T)) {
	t.Run(name, func(t *testing.T) { synctest.Test(t, f) })
}

// wantOutput fails unless out contains each of lines.
func wantOutput(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
}

// wantNoOutput fails if out contains any of lines.
func wantNoOutput(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(out, line) {
			t.Errorf("output has %q:\n%s", line, out)
		}
	}
}

// wantLines fails unless out holds lines, in order, each a whole line.
func wantLines(t *testing.T, out string, lines ...string) {
	t.Helper()
	got := strings.Split(out, "\n")
	i := 0
	for _, line := range lines {
		j := slices.Index(got[i:], line)
		if j < 0 {
			t.Errorf("output lacks the line %q after line %d:\n%s", line, i, out)
			return
		}
		i += j + 1
	}
}

// screen is what a terminal shows of out: each line as it was drawn last,
// after its last carriage return.
func screen(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		lines[i] = line[strings.LastIndex(line, "\r")+1:]
	}
	return strings.Join(lines, "\n")
}

// wantScreen fails unless a terminal shows out as lines, and nothing more.
func wantScreen(t *testing.T, out string, lines ...string) {
	t.Helper()
	if got, want := screen(out), strings.Join(lines, "\n")+"\n"; got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// wantError fails unless err's message contains want.
func wantError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("error %v, want one containing %q", err, want)
	}
}

// matches reports whether err contains want, or is nil when want is.
func matches(err error, want string) bool {
	if want == "" {
		return err == nil
	}
	return err != nil && strings.Contains(err.Error(), want)
}

// cancelOn is an Out that cancels a context once a line it is given
// appears.
type cancelOn struct {
	strings.Builder
	line   string
	cancel context.CancelFunc
}

func (c *cancelOn) Write(p []byte) (int, error) {
	if strings.Contains(string(p), c.line) {
		c.cancel()
	}
	return c.Builder.Write(p)
}
