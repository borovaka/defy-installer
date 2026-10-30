// SPDX-License-Identifier: MIT

package settings

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/borovaka/defy-installer/internal/release"
)

// The settings a file may hold, in the order a missing one is appended.
const (
	keymapKey   = "keymap"
	firmwareKey = "firmware"
)

var settingKeys = []string{keymapKey, firmwareKey}

var bom = []byte("\xef\xbb\xbf")

// windowsPaths is the hint for a backslash in double quotes, which starts
// an escape.
const windowsPaths = `write Windows paths in single quotes, like firmware: 'C:\Users\alice\firmware.zip'`

// document is a settings file as parsed: its text, and where each
// setting's value sits in it.
type document struct {
	path string
	// bom is the UTF-8 byte order mark the file starts with, or nil; text
	// is the rest, which the parser's lines and columns count.
	bom  []byte
	text []byte
	// lines holds where each line starts in text.
	lines []int
	// eol is the file's first line break, which appended lines end with.
	eol string
	// mapping is the file's mapping, nil for a file without one.
	mapping *yaml.Node
	// settings holds the settings the file has, by name.
	settings map[string]*setting
}

// setting is a setting in the file: its value node, where the value's
// text starts and ends in text, and the value, empty for a null.
type setting struct {
	node       *yaml.Node
	start, end int
	value      string
}

// values returns the values the file holds, as written in it.
func (d *document) values() map[string]string {
	values := map[string]string{}
	for key, s := range d.settings {
		values[key] = s.value
	}
	return values
}

// parse reads and checks a settings file. path labels its errors. Each
// setting must be on a line of its own: name, colon and value, with the
// value plain or in quotes.
func parse(path string, data []byte) (*document, error) {
	d := &document{path: path, text: data, eol: "\n", settings: map[string]*setting{}}
	if bytes.HasPrefix(data, bom) {
		d.bom, d.text = bom, data[len(bom):]
	}
	if !utf8.Valid(d.text) {
		return nil, fmt.Errorf("%s is not UTF-8 text; save it as UTF-8", path)
	}
	d.findLines()
	if err := d.checkCharacters(); err != nil {
		return nil, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil && !errors.Is(err, io.EOF) {
		return nil, d.yamlError(err)
	}
	var next yaml.Node
	if err := dec.Decode(&next); err == nil {
		return nil, fmt.Errorf("%s:%d: a second YAML document starts here; the settings file holds one", path, next.Line)
	} else if !errors.Is(err, io.EOF) {
		return nil, d.yamlError(err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return d, nil
	}
	top := root.Content[0]
	if top.Kind == yaml.ScalarNode && top.Tag == "!!null" {
		return d, nil
	}
	if top.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s:%d: this is not a setting; put each setting on a line of its own, "+
			"like keymap: 'default'", path, top.Line)
	}
	if top.Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("%s:%d: the settings are in braces; put each setting on a line of its own, "+
			"like keymap: 'default'", path, top.Line)
	}
	d.mapping = top
	for pair := range slices.Chunk(top.Content, 2) {
		key, value := pair[0], pair[1]
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s:%d: unknown setting; the settings are keymap and firmware", path, key.Line)
		}
		if !slices.Contains(settingKeys, key.Value) {
			return nil, fmt.Errorf("%s:%d: unknown setting %q; the settings are keymap and firmware", path, key.Line, key.Value)
		}
		if first, ok := d.settings[key.Value]; ok {
			return nil, fmt.Errorf("%s:%d: %s is set twice, here and on line %d; keep one", path, key.Line, key.Value,
				first.node.Line)
		}
		if value.Kind != yaml.ScalarNode || value.Anchor != "" ||
			value.Style&(yaml.TaggedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return nil, fmt.Errorf("%s:%d: %s %s; give it one value, plain or in quotes, like %s: ''",
				path, value.Line, key.Value, describe(value), key.Value)
		}
		start, end, ok := d.span(value)
		if !ok || value.Line != key.Line {
			return nil, fmt.Errorf("%s:%d: %s runs over more than one line; put each setting on a line of its own, "+
				"like %s: ''", path, key.Line, key.Value, key.Value)
		}
		if key.Value == firmwareKey && strings.ContainsFunc(value.Value, notInPath) {
			if value.Style&yaml.DoubleQuotedStyle != 0 {
				return nil, fmt.Errorf("%s:%d: firmware %q holds a control character, as a backslash in double "+
					"quotes gives; %s", path, value.Line, value.Value, windowsPaths)
			}
			return nil, fmt.Errorf("%s:%d: firmware %q holds a control character; delete it", path, value.Line, value.Value)
		}
		s := &setting{node: value, start: start, end: end}
		if value.Tag != "!!null" {
			s.value = value.Value
		}
		d.settings[key.Value] = s
	}
	if s, ok := d.settings[keymapKey]; ok && s.value != "" {
		if _, err := release.ParseSource(s.value); err != nil {
			reason := err.Error()
			if se, ok := errors.AsType[*release.SourceError](err); ok {
				reason = se.Reason
			}
			return nil, fmt.Errorf("%s:%d: keymap %q: %s; leave it empty, or %s",
				path, s.node.Line, s.value, reason, release.SourceForm)
		}
	}
	return d, nil
}

// describe says why a setting's value is not a single plain or quoted
// one, after the setting's name.
func describe(n *yaml.Node) string {
	switch {
	case n.Kind == yaml.SequenceNode:
		return "is a list"
	case n.Kind == yaml.MappingNode:
		return "is a group of settings"
	case n.Kind == yaml.AliasNode:
		return "is an alias, *" + n.Value
	case n.Anchor != "":
		return "has an anchor, &" + n.Anchor
	case n.Style&yaml.TaggedStyle != 0:
		return "has a tag, " + n.Tag
	}
	return "is a block of text, after | or >"
}

// notInPath reports whether r is a character that no firmware path holds:
// a control character, or the line separators YAML reads as line breaks.
// A backslash in double quotes gives them: "D:\new" holds a line feed.
func notInPath(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}

// checkCharacters refuses the characters YAML does not allow in a file,
// with the line of the first: control characters other than tab, line
// feed, carriage return and NEL, and U+FFFE and U+FFFF. The parser would
// refuse them without a line.
func (d *document) checkCharacters() error {
	for i, r := range string(d.text) {
		switch {
		case r == '\t' || r == '\n' || r == '\r' || r == 0x85,
			r >= 0x20 && r <= 0x7e, r >= 0xa0 && r <= 0xd7ff, r >= 0xe000 && r <= 0xfffd, r >= 0x10000:
			continue
		}
		line, _ := slices.BinarySearch(d.lines, i+1)
		return fmt.Errorf("%s:%d: the file holds the character U+%04X, which YAML does not allow; delete it",
			d.path, line, r)
	}
	return nil
}

var yamlLine = regexp.MustCompile(`^yaml: (?:line ([0-9]+): )?(.*)$`)

// parserProblems are the problems that yaml.v3's parser reports, rather
// than its scanner. The parser counts its lines from 0 and the scanner
// from 1, and neither gives a line for a problem on the first line.
var parserProblems = []string{
	"did not find expected <stream-start>",
	"did not find expected <document start>",
	"found duplicate %YAML directive",
	"found incompatible YAML document",
	"found duplicate %TAG directive",
	"found undefined tag handle",
	"did not find expected node content",
	"did not find expected '-' indicator",
	"did not find expected key",
	"did not find expected ',' or ']'",
	"did not find expected ',' or '}'",
}

// yamlError gives a parser error the file's name and the line counted
// from 1, in the form the settings' own errors have. An error in an
// escape, which a Windows path in double quotes gives, gets a hint.
func (d *document) yamlError(err error) error {
	m := yamlLine.FindStringSubmatch(err.Error())
	if m == nil {
		return fmt.Errorf("%s: %w", d.path, err)
	}
	problem := m[2]
	// yaml.v3's message spells it hexdecimal.
	if strings.Contains(problem, "escape") || strings.Contains(problem, "hexdecimal") {
		problem += "; " + windowsPaths
	}
	line, _ := strconv.Atoi(m[1])
	switch {
	case strings.HasPrefix(problem, "unknown anchor"):
		// The parser gives no line for an alias without its anchor.
		return fmt.Errorf("%s: %s", d.path, problem)
	case slices.Contains(parserProblems, m[2]) && m[1] != "":
		line++
	case m[1] == "":
		line = 1
	}
	return fmt.Errorf("%s:%d: %s", d.path, line, problem)
}

// findLines finds where each line starts, with the line breaks YAML has:
// CR LF, LF, CR, and Unicode's NEL, LS and PS.
func (d *document) findLines() {
	d.lines = []int{0}
	eol := ""
	for i := 0; i < len(d.text); {
		r, size := utf8.DecodeRune(d.text[i:])
		brk := ""
		switch r {
		case '\r':
			brk = "\r"
			if i+1 < len(d.text) && d.text[i+1] == '\n' {
				brk = "\r\n"
			}
		case '\n', 0x85, 0x2028, 0x2029:
			brk = string(r)
		}
		if brk == "" {
			i += size
			continue
		}
		if eol == "" {
			eol = brk
		}
		i += max(size, len(brk))
		d.lines = append(d.lines, i)
	}
	if eol == "\r\n" {
		d.eol = eol
	}
}

// lineEnd returns where a line, counted from 1, ends in text, before its
// line break.
func (d *document) lineEnd(line int) int {
	if line >= len(d.lines) {
		return len(d.text)
	}
	end := d.lines[line]
	_, size := utf8.DecodeLastRune(d.text[:end])
	end -= size
	if d.text[end] == '\n' && end > 0 && d.text[end-1] == '\r' {
		end--
	}
	return end
}

// offset returns where the parser's line and column, which count from 1
// and in characters, fall in text.
func (d *document) offset(line, column int) (int, bool) {
	if line < 1 || line > len(d.lines) || column < 1 {
		return 0, false
	}
	pos, end := d.lines[line-1], d.lineEnd(line)
	for range column - 1 {
		if pos >= end {
			return 0, false
		}
		_, size := utf8.DecodeRune(d.text[pos:])
		pos += size
	}
	return pos, true
}

// span returns where a value's text starts and ends in text, if it is on
// one line and reads as the value. A null without text, as in "keymap:",
// has an empty span just after its colon.
func (d *document) span(n *yaml.Node) (int, int, bool) {
	start, ok := d.offset(n.Line, n.Column)
	if !ok {
		return 0, 0, false
	}
	text, limit := d.text, d.lineEnd(n.Line)
	var end int
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		end = closing(text[:limit], start, '"')
	case n.Style&yaml.SingleQuotedStyle != 0:
		end = closing(text[:limit], start, '\'')
	case n.Tag == "!!null" && n.Value == "":
		return start, start, start > 0 && text[start-1] == ':'
	default:
		// A plain value ends at the end of its line or at a comment.
		end = start
		for ; end < limit; end++ {
			if text[end] == '#' && end > start && (text[end-1] == ' ' || text[end-1] == '\t') {
				break
			}
		}
		for end > start && (text[end-1] == ' ' || text[end-1] == '\t') {
			end--
		}
	}
	if end < 0 || !reads(text[start:end], n) {
		return 0, 0, false
	}
	return start, end, true
}

// closing returns where the quoted text starting at start ends, just
// after its closing quote, or -1.
func closing(text []byte, start int, quote byte) int {
	if start >= len(text) || text[start] != quote {
		return -1
	}
	for i := start + 1; i < len(text); i++ {
		switch {
		case quote == '"' && text[i] == '\\':
			i++
		case text[i] == quote && quote == '\'' && i+1 < len(text) && text[i+1] == '\'':
			i++
		case text[i] == quote:
			return i + 1
		}
	}
	return -1
}

// reads reports whether text, read alone, is the value n holds.
func reads(text []byte, n *yaml.Node) bool {
	if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) == 0 {
		return string(text) == n.Value
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil || len(doc.Content) != 1 {
		return false
	}
	return doc.Content[0].Kind == yaml.ScalarNode && doc.Content[0].Value == n.Value
}

// change is a setting's new value.
type change struct {
	key, value string
}

// edit returns the file with each change made: the old value's text
// replaced, or the setting appended at the end when the file lacks it.
// Every other byte stays.
func (d *document) edit(changes []change) ([]byte, error) {
	type replacement struct {
		start, end int
		text       string
	}
	var replacements []replacement
	var appended strings.Builder
	for _, c := range changes {
		s, ok := d.settings[c.key]
		if !ok {
			indent := 0
			if d.mapping != nil && len(d.mapping.Content) > 0 {
				indent = d.mapping.Content[0].Column - 1
			}
			text, err := encode(c.value, 0)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&appended, "%s%s: %s%s", strings.Repeat(" ", indent), c.key, text, d.eol)
			continue
		}
		text, err := encode(c.value, s.node.Style)
		if err != nil {
			return nil, err
		}
		if s.start == s.end {
			text = " " + text
		}
		replacements = append(replacements, replacement{s.start, s.end, text})
	}
	out := slices.Clone(d.text)
	slices.SortFunc(replacements, func(a, b replacement) int { return b.start - a.start })
	for _, r := range replacements {
		out = slices.Concat(out[:r.start], []byte(r.text), out[r.end:])
	}
	if appended.Len() > 0 {
		if len(out) > 0 && !bytes.HasSuffix(out, []byte("\n")) && !bytes.HasSuffix(out, []byte("\r")) {
			out = append(out, d.eol...)
		}
		out = append(out, appended.String()...)
	}
	return slices.Concat(d.bom, out), nil
}

// encode writes a value as YAML text in the style of the value it
// replaces: plain where that was plain or there was none, if the value
// reads back as the same text, and otherwise in the quotes that value had.
// A value that needs quotes where there were none gets single quotes,
// which take a backslash as it is. A value that single quotes cannot hold
// gets double quotes from yaml.v3, with escapes for characters such as
// control characters and those above U+FFFF.
func encode(value string, style yaml.Style) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%q is not UTF-8 text", value)
	}
	if style == 0 && !plainSafe(value) {
		style = yaml.SingleQuotedStyle
	}
	text, err := yaml.Marshal(&yaml.Node{Kind: yaml.ScalarNode, Style: style, Value: value})
	return strings.TrimSuffix(string(text), "\n"), err
}

// plainSafe reports whether value can be written without quotes: letters,
// digits, '/', '.' and '_', with '-', '@' and '+' after the first
// character, reading back as the same text rather than as a number, a
// boolean, a date or null.
func plainSafe(value string) bool {
	if value == "" {
		return false
	}
	for i, c := range []byte(value) {
		ok := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
			c == '/' || c == '.' || c == '_' || i > 0 && (c == '-' || c == '@' || c == '+')
		if !ok {
			return false
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(value), &doc); err != nil || len(doc.Content) != 1 {
		return false
	}
	n := doc.Content[0]
	return n.Kind == yaml.ScalarNode && n.Tag == "!!str" && n.Value == value
}
