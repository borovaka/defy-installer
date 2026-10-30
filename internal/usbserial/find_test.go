// SPDX-License-Identifier: MIT

package usbserial

import (
	"context"
	"strings"
	"testing"
)

func TestFind(t *testing.T) {
	a := Port{Path: "COM5", ID: USBID{0x1d50, 0x615e}, Serial: "0123456789ABCDEF", Location: "1-1"}
	b := Port{Path: "COM6", ID: USBID{0x1d50, 0x615e}, Serial: "0123456789ABCDEF", Location: "1-1"}
	if got, err := Find([]Port{a, b}, "COM6"); err != nil || got != b {
		t.Errorf("got %v, %v", got, err)
	}
	for _, tt := range []struct {
		ports []Port
		want  string
	}{
		{[]Port{a}, "no serial port COM6"},
		{[]Port{b, b}, "COM6 is listed more than once"},
	} {
		if _, err := Find(tt.ports, "COM6"); err == nil || err.Error() != tt.want {
			t.Errorf("got %v, want %q", err, tt.want)
		}
	}
}

func TestRecheck(t *testing.T) {
	target := Port{Path: "COM5", ID: USBID{0x1d50, 0x615e}, Serial: "0123456789ABCDEF", Location: "1-1"}
	list := func(ports ...Port) Lister {
		return func(context.Context) ([]Port, error) { return ports, nil }
	}
	ctx := t.Context()
	if err := Recheck(ctx, target, list(target)); err != nil {
		t.Error(err)
	}
	moved := target
	moved.Location = "1-2"
	if err := Recheck(ctx, target, list(moved)); err == nil || !strings.Contains(err.Error(), "USB identity changed") {
		t.Errorf("got %v", err)
	}
	if err := Recheck(ctx, target, list()); err == nil {
		t.Error("a port that went away passed")
	}
}
