// SPDX-License-Identifier: MIT

package flow

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// errNoInput reports input that ended while the installer waited for Enter.
var errNoInput = errors.New("the input ended while the installer waited for Enter")

// ui is how the user's commands talk to the user: lines, some of them
// built up in parts, questions, waits for Enter, and progress bars. It is
// also an io.Writer for the detail lines that --verbose adds.
type ui struct {
	out io.Writer
	in  *bufio.Reader
	// yes answers yes to every question; Enter is still waited for.
	yes bool
	// echo prints each answer read, for input that is not a terminal,
	// which shows nothing as it is typed.
	echo bool
	// discard drops the input typed before a question or a wait for
	// Enter, so that only an answer typed after it counts.
	discard func()
	// terminal draws bars again in place; otherwise a bar prints a line at
	// each quarter.
	terminal bool

	// pending is set while a line has started and not ended, and owner is
	// the bar drawing it, if one is.
	pending bool
	owner   *bar
	// blankLast is set when the last line printed was blank, or nothing
	// was printed.
	blankLast bool
}

// newUI returns a ui that prints to out and reads input. discard is
// Options.Discard: if set, the ui's discard runs it, then drops what the
// ui has read and not used. Otherwise the ui keeps all input, such as a
// script's answers.
func newUI(out io.Writer, input io.Reader, discard func()) *ui {
	u := &ui{out: out, in: bufio.NewReader(input), discard: func() {}, blankLast: true}
	if discard != nil {
		u.discard = func() {
			discard()
			u.in.Discard(u.in.Buffered())
		}
	}
	return u
}

// end ends a line that has started.
func (u *ui) end() {
	if u.pending {
		io.WriteString(u.out, "\n")
		u.pending, u.owner = false, nil
	}
}

// say prints a line.
func (u *ui) say(format string, args ...any) {
	u.end()
	fmt.Fprintf(u.out, format+"\n", args...)
	u.blankLast = false
}

// begin starts a line that add goes on with.
func (u *ui) begin(format string, args ...any) {
	u.end()
	fmt.Fprintf(u.out, format, args...)
	u.pending, u.blankLast = true, false
}

// add goes on with the line that begin started. When no line has started,
// or a bar holds the line, it begins a new one, without the leading spaces.
func (u *ui) add(format string, args ...any) {
	if !u.pending || u.owner != nil {
		u.begin("%s", strings.TrimLeft(fmt.Sprintf(format, args...), " "))
		return
	}
	fmt.Fprintf(u.out, format, args...)
}

// blank prints a blank line, unless the last line was blank.
func (u *ui) blank() {
	u.end()
	if !u.blankLast {
		io.WriteString(u.out, "\n")
		u.blankLast = true
	}
}

// Write prints the detail lines of the flows that the user's commands
// share with the advanced ones.
func (u *ui) Write(p []byte) (int, error) {
	u.end()
	u.blankLast = false
	return u.out.Write(p)
}

// readLine reads a line of input. Input that ends without a line break
// gives its last line, then io.EOF.
func (u *ui) readLine(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	line, err := u.in.ReadString('\n')
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if errors.Is(err, io.EOF) && line != "" {
		err = nil
	}
	return strings.TrimRight(line, "\r\n"), err
}

// ask asks a question that y or yes answers, and anything else, or the
// end of the input, does not. With yes set it answers yes itself.
func (u *ui) ask(ctx context.Context, question string) (bool, error) {
	u.begin("%s [y/N] ", question)
	if u.yes {
		u.add("y")
		u.end()
		return true, nil
	}
	u.discard()
	line, err := u.readLine(ctx)
	switch {
	case errors.Is(err, io.EOF):
		u.end()
		return false, nil
	case err != nil:
		u.end()
		return false, err
	}
	if u.echo {
		u.add("%s", line)
		u.end()
	} else {
		// The terminal showed the answer and its line break.
		u.pending = false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// enter waits for Enter, whatever yes says.
func (u *ui) enter(ctx context.Context) error {
	u.end()
	u.discard()
	_, err := u.readLine(ctx)
	if errors.Is(err, io.EOF) {
		return errNoInput
	}
	if err == nil && !u.echo {
		// The terminal showed the line break, which leaves a blank line.
		u.blankLast = true
	}
	return err
}

// barWidth is how many marks a full bar has.
const barWidth = 26

// bar is a write's progress: a label, a bar of marks and a percentage.
type bar struct {
	u        *ui
	prefix   string // the indent and the label, padded
	shown    int    // marks drawn on a terminal, or quarters printed
	pct      int
	finished bool
}

// bar returns a bar labelled label, padded to width so that the bars of a
// step line up.
func (u *ui) bar(indent, label string, width int) *bar {
	return &bar{u: u, prefix: indent + label + strings.Repeat(" ", max(width-len(label), 0)) + "  ", shown: -1, pct: -1}
}

func (b *bar) line(done, total int) string {
	marks := done * barWidth / total
	return fmt.Sprintf("%s[%s%s] %d%%", b.prefix, strings.Repeat("#", marks), strings.Repeat(" ", barWidth-marks),
		done*100/total)
}

// update shows done of total. On a terminal the bar is drawn again in
// place; otherwise a line is printed as each quarter is reached.
func (b *bar) update(done, total int) {
	if b.finished || total <= 0 {
		return
	}
	done = min(max(done, 0), total)
	u := b.u
	if !u.terminal {
		for quarter := done * 4 / total; max(b.shown, 0) < quarter; {
			b.shown = max(b.shown, 0) + 1
			u.say("%s", b.line(b.shown, 4))
		}
		b.finished = b.shown == 4
		return
	}
	marks, pct := done*barWidth/total, done*100/total
	if marks == b.shown && pct == b.pct {
		return
	}
	b.shown, b.pct = marks, pct
	if u.pending && u.owner != b {
		u.end()
	}
	fmt.Fprintf(u.out, "\r%s", b.line(done, total))
	u.pending, u.owner, u.blankLast = true, b, false
	if done == total {
		u.end()
		b.finished = true
	}
}
