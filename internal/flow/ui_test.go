// SPDX-License-Identifier: MIT

package flow

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestBars(t *testing.T) {
	var out strings.Builder
	u := newUI(&out, strings.NewReader(""), nil)
	b := u.bar("  ", "Writing the Neuron", len(longestBar))
	for done := range 9 {
		b.update(done, 8)
	}
	b.update(8, 8)
	want := "  Writing the Neuron      [######                    ] 25%\n" +
		"  Writing the Neuron      [#############             ] 50%\n" +
		"  Writing the Neuron      [###################       ] 75%\n" +
		"  Writing the Neuron      [##########################] 100%\n"
	if out.String() != want {
		t.Errorf("got\n%s\nwant\n%s", out.String(), want)
	}
	out.Reset()
	u.terminal = true
	b = u.bar("", "Writing the Neuron", 0)
	b.update(0, 2)
	b.update(1, 2)
	u.say("a line between")
	b.update(2, 2)
	want = "\rWriting the Neuron  [                          ] 0%" +
		"\rWriting the Neuron  [#############             ] 50%\n" +
		"a line between\n" +
		"\rWriting the Neuron  [##########################] 100%\n"
	if out.String() != want {
		t.Errorf("got %q\nwant %q", out.String(), want)
	}
}

func TestQuestions(t *testing.T) {
	for _, tt := range []struct {
		input string
		yes   bool
		want  bool
	}{
		{"y\n", false, true},
		{"YES\n", false, true},
		{" yes \r\n", false, true},
		{"n\n", false, false},
		{"\n", false, false},
		{"yep\n", false, false},
		{"", false, false},
		{"", true, true},
	} {
		var out strings.Builder
		u := newUI(&out, strings.NewReader(tt.input), nil)
		u.yes = tt.yes
		got, err := u.ask(t.Context(), "Continue?")
		if err != nil || got != tt.want {
			t.Errorf("%q, yes %v: got %v, %v", tt.input, tt.yes, got, err)
		}
	}
}

// TestPromptsDiscardTypedAhead: on a terminal, what was typed before a
// question or a wait for Enter does not answer it, such as a key a woken
// half typed through the Neuron.
func TestPromptsDiscardTypedAhead(t *testing.T) {
	for _, prompt := range []func(u *ui) error{
		func(u *ui) error { return u.enter(t.Context()) },
		func(u *ui) error {
			yes, err := u.ask(t.Context(), "Write it again anyway?")
			if yes {
				return errors.New("answered yes")
			}
			return err
		},
	} {
		discarded := 0
		u := newUI(io.Discard, strings.NewReader("y\n\n"), func() { discarded++ })
		// The typed input waits in the ui's buffer.
		if _, err := u.in.Peek(1); err != nil {
			t.Fatal(err)
		}
		if err := prompt(u); err != nil && !errors.Is(err, errNoInput) || discarded != 1 {
			t.Errorf("got %v after %d discards", err, discarded)
		}
	}
	// --yes asks nothing, so there is nothing to discard.
	discarded := 0
	u := newUI(io.Discard, strings.NewReader(""), func() { discarded++ })
	u.yes = true
	if yes, err := u.ask(t.Context(), "Continue?"); !yes || err != nil || discarded != 0 {
		t.Errorf("got %v, %v after %d discards", yes, err, discarded)
	}
}
