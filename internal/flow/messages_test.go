// SPDX-License-Identifier: MIT

package flow

import (
	"testing"
	"time"
	// Europe/Sofia for TestBuiltWhen, on systems without the zone files.
	_ "time/tzdata"
)

func TestBuiltWhen(t *testing.T) {
	now := time.Date(2026, 10, 24, 1, 0, 0, 0, time.FixedZone("EEST", 3*3600))
	for when, want := range map[time.Time]string{
		time.Date(2026, 10, 23, 22, 30, 0, 0, time.UTC): "today",
		time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC):  "yesterday",
		time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC):   "on 2 October 2026",
		{}: "",
	} {
		if got := builtWhen(when, now); got != want {
			t.Errorf("%v: %q, want %q", when, got, want)
		}
	}
	// Days on which the clocks change are 23 or 25 hours long, and still
	// one day. Sofia's clocks go back on 25 October 2026 and forward on 28
	// March 2027.
	sofia, err := time.LoadLocation("Europe/Sofia")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		when, now time.Time
		want      string
	}{
		{time.Date(2026, 10, 25, 12, 0, 0, 0, sofia), time.Date(2026, 10, 26, 10, 0, 0, 0, sofia), "yesterday"},
		{time.Date(2026, 10, 24, 23, 30, 0, 0, sofia), time.Date(2026, 10, 25, 23, 30, 0, 0, sofia), "yesterday"},
		{time.Date(2026, 10, 25, 0, 30, 0, 0, sofia), time.Date(2026, 10, 25, 23, 30, 0, 0, sofia), "today"},
		{time.Date(2027, 3, 27, 23, 0, 0, 0, sofia), time.Date(2027, 3, 28, 1, 0, 0, 0, sofia), "yesterday"},
		{time.Date(2027, 3, 27, 1, 0, 0, 0, sofia), time.Date(2027, 3, 29, 0, 30, 0, 0, sofia), "on 27 March 2027"},
	} {
		if got := builtWhen(tt.when, tt.now); got != tt.want {
			t.Errorf("%v at %v: %q, want %q", tt.when, tt.now, got, tt.want)
		}
	}
}
