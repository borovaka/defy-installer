// SPDX-License-Identifier: MIT

package neuron

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/usbserial"
)

var (
	studioPort  = usbserial.Port{Path: "/dev/app", ID: ZMKID, Serial: "0123456789ABCDEF", Location: "1-1", Product: "Defy"}
	updaterPort = with(studioPort, func(p *usbserial.Port) { p.Path = "/dev/app-updater" })
	bootPort    = usbserial.Port{Path: "/dev/boot", ID: BootloaderID, Serial: "000000000000", Location: "1-1", Product: "Dygma Bootloader"}
	otherBoot   = with(bootPort, func(p *usbserial.Port) { p.Path, p.Location = "/dev/other", "1-2" })
	stockPort   = usbserial.Port{Path: "/dev/stock", ID: StockID, Serial: "Dygma", Location: "1-1", Product: "Defy"}
	loaderPort  = usbserial.Port{Path: "/dev/loader", ID: LoaderID, Serial: studioPort.Serial, Location: "1-1", Product: loaderProduct}
)

// with returns a copy of p changed by change.
func with(p usbserial.Port, change func(*usbserial.Port)) usbserial.Port {
	change(&p)
	return p
}

// sequence lists each of its states in turn, then the last one again and
// again.
type sequence struct {
	states [][]usbserial.Port
	calls  int
}

func states(s ...[]usbserial.Port) *sequence {
	return &sequence{states: s}
}

func ports(p ...usbserial.Port) []usbserial.Port {
	return p
}

func (s *sequence) list(context.Context) ([]usbserial.Port, error) {
	s.calls++
	state := s.states[0]
	if len(s.states) > 1 {
		s.states = s.states[1:]
	}
	return slices.Clone(state), nil
}

// fixture is an Entry that records its touches and updater checks. A port
// is the updater's when updater says so, or with updater nil when it is at
// updaterPort's path.
type fixture struct {
	Entry
	touches    []string
	identified []string
	touchErr   error
	updater    func(usbserial.Port) (bool, error)
}

func newFixture(list usbserial.Lister) *fixture {
	f := &fixture{}
	f.Entry = Entry{
		List: list,
		Touch: func(path string) error {
			f.touches = append(f.touches, path)
			return f.touchErr
		},
		IsUpdater: func(_ context.Context, p usbserial.Port) (bool, error) {
			f.identified = append(f.identified, p.Path)
			if f.updater == nil {
				return isUpdaterPort(p)
			}
			return f.updater(p)
		},
		Timeout: 500 * time.Millisecond,
	}
	return f
}

func isUpdaterPort(p usbserial.Port) (bool, error) {
	return p.Path == updaterPort.Path, nil
}

func TestFindBootloader(t *testing.T) {
	ctx := t.Context()
	other := usbserial.Port{Path: "/dev/b", ID: usbserial.USBID{Vendor: 1, Product: 2}}
	boot := usbserial.Port{Path: "/dev/a", ID: BootloaderID}
	f := newFixture(states(ports(other, boot)).list)
	if got, err := f.FindBootloader(ctx, "/dev/a", ""); err != nil || got != boot {
		t.Errorf("got %v, %v", got, err)
	}
	for _, tt := range []struct {
		ports []usbserial.Port
		want  string
	}{
		{ports(usbserial.Port{Path: "/dev/a", ID: StockID}), "/dev/a is the Neuron running Dygma's firmware, not its bootloader. " + EntryGuidance},
		{ports(usbserial.Port{Path: "/dev/a", ID: LoaderID, Product: loaderProduct}), "/dev/a is the Neuron's UF2 loader, not its bootloader, so " +
			"nothing was sent. Unplug the Neuron and plug it back in to start ZMK. If the drive comes back, copy the " +
			"UF2 file again, or run defy-installer update"},
		{ports(usbserial.Port{Path: "/dev/a", ID: usbserial.USBID{Vendor: 0x2e8a, Product: 0x3}}), "/dev/a is not the Neuron's bootloader (USB 2e8a:0003). " + EntryGuidance},
		{ports(with(studioPort, func(p *usbserial.Port) { p.Path = "/dev/a" })), "/dev/a is not the Neuron's bootloader (USB 1d50:615e)"},
		{nil, "no serial port /dev/a. " + EntryGuidance},
	} {
		f := newFixture(states(tt.ports).list)
		_, err := f.FindBootloader(ctx, "/dev/a", "")
		wantError(t, err, tt.want)
		if len(f.touches)+len(f.identified) != 0 {
			t.Errorf("touched %q, identified %q", f.touches, f.identified)
		}
	}
}

func TestEnterBootloaderTakesABootloaderPortAsItIs(t *testing.T) {
	f := newFixture(states(ports(studioPort, bootPort)).list)
	boot, studio, err := f.EnterBootloader(t.Context(), bootPort.Path, "")
	if err != nil || boot != bootPort || studio != (usbserial.Port{}) {
		t.Errorf("got %v, %v, %v", boot, studio, err)
	}
	if len(f.touches) != 0 {
		t.Errorf("touched %q", f.touches)
	}
}

func TestEnterBootloaderRefusesOtherIdentitiesAndAMissingLocation(t *testing.T) {
	for _, target := range []usbserial.Port{
		with(studioPort, func(p *usbserial.Port) { p.Location = "" }),
		with(studioPort, func(p *usbserial.Port) { p.ID = StockID }),
	} {
		f := newFixture(states(ports(target)).list)
		if _, _, err := f.EnterBootloader(t.Context(), target.Path, ""); err == nil {
			t.Errorf("%+v: accepted", target)
		}
		if len(f.touches) != 0 {
			t.Errorf("%+v: touched", target)
		}
	}
	f := newFixture(states(ports(with(studioPort, func(p *usbserial.Port) { p.Location = "" }))).list)
	_, _, err := f.EnterBootloader(t.Context(), studioPort.Path, "")
	wantError(t, err, "does not say which USB socket /dev/app is in", "with the button instead. "+EntryGuidance)
}

func TestEnterBootloaderRefusesBeforeTheTouch(t *testing.T) {
	changed := with(studioPort, func(p *usbserial.Port) { p.Serial = "another" })
	owned := func(usbserial.Port) (bool, error) { return false, errors.New("port is owned") }
	for _, tt := range []struct {
		name       string
		listed     [][]usbserial.Port
		path       string
		serial     string
		updater    func(usbserial.Port) (bool, error)
		identified int // the ports asked whether they are the updater's
		want       []string
	}{
		{"another serial number", [][]usbserial.Port{ports(studioPort)}, studioPort.Path, "DIFFERENT", nil, 0,
			[]string{"/dev/app is not the Neuron with USB serial number DIFFERENT; nothing was sent"}},
		{"identity changed", [][]usbserial.Port{ports(studioPort, updaterPort), ports(changed, updaterPort)},
			studioPort.Path, "", nil, 2, []string{"USB identity changed"}},
		{"listed twice", [][]usbserial.Port{ports(studioPort, studioPort)}, studioPort.Path, "", nil, 0,
			[]string{"listed more than once"}},
		{"bootloader already listed", [][]usbserial.Port{ports(studioPort, updaterPort, bootPort)}, studioPort.Path, "", nil, 2,
			[]string{"a Neuron bootloader is already listed at the USB socket of /dev/app"}},
		// A Neuron that has just enumerated can list one port before the
		// other, and either can come first.
		{"Studio alone", [][]usbserial.Port{ports(studioPort)}, studioPort.Path, "", nil, 0,
			[]string{"the Neuron shows only one of its two ports, /dev/app", "Wait a few seconds and try again; nothing was sent"}},
		{"updater alone", [][]usbserial.Port{ports(updaterPort)}, updaterPort.Path, "", nil, 0,
			[]string{"the Neuron shows only one of its two ports, /dev/app-updater", "Wait a few seconds and try again; nothing was sent"}},
		{"updater check error", [][]usbserial.Port{ports(studioPort, updaterPort)}, studioPort.Path, "", owned, 1,
			[]string{"port is owned"}},
		{"updater port", [][]usbserial.Port{ports(studioPort, updaterPort)}, updaterPort.Path, "", nil, 2,
			[]string{"/dev/app-updater is the Neuron's port for writing the halves, which cannot start its " +
				"bootloader; use /dev/app. Nothing was sent"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(states(tt.listed...).list)
			f.updater = tt.updater
			_, _, err := f.EnterBootloader(t.Context(), tt.path, tt.serial)
			wantError(t, err, tt.want...)
			if len(f.touches) != 0 || len(f.identified) != tt.identified {
				t.Errorf("touched %q, identified %q", f.touches, f.identified)
			}
		})
	}
}

func TestEnterBootloaderFollowsTheLocationAndWaitsForTheFirmwareToGo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := states(ports(studioPort, updaterPort, otherBoot), ports(studioPort, updaterPort, otherBoot),
			ports(studioPort, updaterPort, bootPort, otherBoot), ports(bootPort, otherBoot))
		f := newFixture(s.list)
		boot, studio, err := f.EnterBootloader(t.Context(), studioPort.Path, studioPort.Serial)
		if err != nil || boot != bootPort || studio != studioPort {
			t.Errorf("got %v, %v, %v", boot, studio, err)
		}
		if s.calls != 4 || !slices.Equal(f.touches, []string{studioPort.Path}) {
			t.Errorf("listed %d times, touched %q", s.calls, f.touches)
		}
	})
}

func TestEnterBootloaderAcceptsTheBootloaderAtTheFirmwaresPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boot := with(bootPort, func(p *usbserial.Port) { p.Path = studioPort.Path })
		f := newFixture(states(ports(studioPort, updaterPort), ports(studioPort, updaterPort), ports(boot)).list)
		got, _, err := f.EnterBootloader(t.Context(), studioPort.Path, "")
		if err != nil || got != boot {
			t.Errorf("got %v, %v", got, err)
		}
	})
}

func TestEnterBootloaderTimesOutWithoutFallingBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(states(ports(studioPort, updaterPort, otherBoot)).list)
		start := time.Now()
		_, _, err := f.EnterBootloader(t.Context(), studioPort.Path, "")
		wantError(t, err, "the Neuron did not enter its bootloader within 500ms. If a halves write is running, "+
			"let it finish. Otherwise enter the bootloader with the button. "+EntryGuidance)
		if waited := time.Since(start); waited != f.Timeout {
			t.Errorf("waited %v", waited)
		}
		if len(f.touches) != 1 {
			t.Errorf("touched %q", f.touches)
		}
	})
}

func TestEnterBootloaderIgnoresABootloaderElsewhere(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(states(ports(studioPort, updaterPort), ports(studioPort, updaterPort), ports(otherBoot)).list)
		_, _, err := f.EnterBootloader(t.Context(), studioPort.Path, "")
		wantError(t, err, "did not enter its bootloader")
	})
}

func TestEnterBootloaderRefusesTwoNewBootloaders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		second := with(bootPort, func(p *usbserial.Port) { p.Path = "/dev/boot-two" })
		f := newFixture(states(ports(studioPort, updaterPort), ports(studioPort, updaterPort), ports(bootPort, second)).list)
		_, _, err := f.EnterBootloader(t.Context(), studioPort.Path, "")
		wantError(t, err, "more than one bootloader port appeared")
	})
}

func TestEnterBootloaderReturnsTheTouchError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := states(ports(studioPort, updaterPort))
		f := newFixture(s.list)
		f.touchErr = errors.New("port is owned")
		_, _, err := f.EnterBootloader(t.Context(), studioPort.Path, "")
		wantError(t, err, "port is owned")
		if len(f.touches) != 1 || s.calls != 2 {
			t.Errorf("touched %q, listed %d times", f.touches, s.calls)
		}
	})
}

func TestEnterBootloaderStopsWhenCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		s := states(ports(studioPort, updaterPort))
		f := newFixture(func(ctx context.Context) ([]usbserial.Port, error) {
			if s.calls == 2 {
				cancel()
			}
			return s.list(ctx)
		})
		f.Timeout = time.Hour
		start := time.Now()
		_, _, err := f.EnterBootloader(ctx, studioPort.Path, "")
		if !errors.Is(err, context.Canceled) || time.Since(start) != 0 {
			t.Errorf("got %v after %v", err, time.Since(start))
		}
	})
}

func TestWaitForFirmwareRequiresTheLocationIdentityAndSerial(t *testing.T) {
	for _, p := range []usbserial.Port{
		with(studioPort, func(p *usbserial.Port) { p.Location = "1-2" }),
		with(studioPort, func(p *usbserial.Port) { p.Serial = "different" }),
		with(studioPort, func(p *usbserial.Port) { p.Product = "Other ZMK" }),
		with(studioPort, func(p *usbserial.Port) { p.Serial = "" }),
		with(studioPort, func(p *usbserial.Port) { p.ID.Vendor = 0x1234 }),
	} {
		synctest.Test(t, func(t *testing.T) {
			f := newFixture(states(ports(p)).list)
			start := time.Now()
			got, ok, err := f.WaitForFirmware(t.Context(), bootPort, ZMKID, studioPort.Serial)
			if ok || err != nil {
				t.Errorf("%+v: got %v, %v, %v", p, got, ok, err)
			}
			if waited := time.Since(start); waited != f.Timeout {
				t.Errorf("waited %v", waited)
			}
		})
	}
}

func TestWaitForFirmwareWhileTheBootloaderRemains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(states(ports(studioPort, bootPort)).list)
		if got, ok, err := f.WaitForFirmware(t.Context(), bootPort, ZMKID, ""); ok || err != nil {
			t.Errorf("got %v, %v, %v", got, ok, err)
		}
	})
}

func TestWaitForStockFirmwareTakesItsOwnSerial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(states(ports(stockPort)).list)
		got, ok, err := f.WaitForFirmware(t.Context(), bootPort, StockID, studioPort.Serial)
		if err != nil || !ok || got != stockPort {
			t.Errorf("got %v, %v, %v", got, ok, err)
		}
		if len(f.identified) != 0 {
			t.Errorf("identified %q", f.identified)
		}
	})
}

// TestWaitForFirmwareWaitsForBothPorts: neither of ZMK's ports is taken
// for Studio's while it is listed alone.
func TestWaitForFirmwareWaitsForBothPorts(t *testing.T) {
	for _, p := range []usbserial.Port{studioPort, updaterPort} {
		synctest.Test(t, func(t *testing.T) {
			f := newFixture(states(ports(p)).list)
			start := time.Now()
			if got, ok, err := f.WaitForFirmware(t.Context(), bootPort, usbserial.USBID{}, ""); ok || err != nil {
				t.Errorf("got %v, %v, %v", got, ok, err)
			}
			if len(f.identified) != 0 || time.Since(start) != f.Timeout {
				t.Errorf("identified %q in %v", f.identified, time.Since(start))
			}
		})
	}
}

func TestWaitForFirmwareWithoutALocation(t *testing.T) {
	s := states(ports(studioPort))
	f := newFixture(s.list)
	boot := with(bootPort, func(p *usbserial.Port) { p.Location = "" })
	if got, ok, err := f.WaitForFirmware(t.Context(), boot, usbserial.USBID{}, ""); ok || err != nil {
		t.Errorf("got %v, %v, %v", got, ok, err)
	}
	if s.calls != 0 {
		t.Errorf("listed %d times", s.calls)
	}
}

func TestWaitForFirmwareRefusesTwoMatchingPorts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(states(ports(studioPort, with(studioPort, func(p *usbserial.Port) { p.Path = "/dev/app-two" }))).list)
		_, _, err := f.WaitForFirmware(t.Context(), bootPort, usbserial.USBID{}, "")
		wantError(t, err, "neither of the Neuron's ports, /dev/app, /dev/app-two, answered as its port for writing the halves")
	})
}

func TestWaitForFirmwareTellsStudioByTheUpdaterNotByOrder(t *testing.T) {
	for _, listed := range [][]usbserial.Port{ports(studioPort, updaterPort), ports(updaterPort, studioPort)} {
		synctest.Test(t, func(t *testing.T) {
			f := newFixture(states(listed).list)
			f.updater = isUpdaterPort
			got, ok, err := f.WaitForFirmware(t.Context(), bootPort, usbserial.USBID{}, "")
			if err != nil || !ok || got != studioPort {
				t.Errorf("got %v, %v, %v", got, ok, err)
			}
		})
	}
}

func TestSelectStudioDoesNotMergeDifferentSerials(t *testing.T) {
	f := newFixture(nil)
	other := with(studioPort, func(p *usbserial.Port) { p.Path, p.Serial = "/dev/other", "different" })
	_, _, err := f.selectStudio(t.Context(), ports(studioPort, other))
	wantError(t, err, "the ports at the Neuron's USB socket, /dev/app, /dev/other, are not the two ports of one Neuron")
	if len(f.identified) != 0 {
		t.Errorf("identified %q", f.identified)
	}
}

func TestStudio(t *testing.T) {
	f := newFixture(nil)
	f.updater = isUpdaterPort
	got, err := f.Studio(t.Context(), ports(updaterPort, studioPort))
	if err != nil || got != studioPort || !slices.Equal(f.identified, []string{updaterPort.Path, studioPort.Path}) {
		t.Errorf("got %v, %v after identifying %v", got, err, f.identified)
	}
	_, err = f.Studio(t.Context(), ports(updaterPort))
	wantError(t, err, "only one of its two ports")
	if len(f.identified) != 2 {
		t.Errorf("identified %q", f.identified)
	}
	f.updater = func(usbserial.Port) (bool, error) { return true, nil }
	_, err = f.Studio(t.Context(), ports(updaterPort, studioPort))
	wantError(t, err, "both of the Neuron's ports", "could not be told apart")
}

// TestWaitForFirmwareRetriesAPortThatDoesNotOpenYet: Windows lists a port
// of a device that has just enumerated before the port opens.
func TestWaitForFirmwareRetriesAPortThatDoesNotOpenYet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(states(ports(studioPort, updaterPort)).list)
		failures := 2
		f.updater = func(p usbserial.Port) (bool, error) {
			if failures > 0 {
				failures--
				return false, errors.New("opening " + p.Path + ": Serial port not found")
			}
			return isUpdaterPort(p)
		}
		got, ok, err := f.WaitForFirmware(t.Context(), bootPort, ZMKID, studioPort.Serial)
		if err != nil || !ok || got != studioPort {
			t.Errorf("got %v, %v, %v", got, ok, err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// A port that never opens fails, with its error, at the timeout.
		f := newFixture(states(ports(studioPort, updaterPort)).list)
		f.updater = func(p usbserial.Port) (bool, error) {
			return false, errors.New("opening " + p.Path + ": Serial port not found")
		}
		start := time.Now()
		_, ok, err := f.WaitForFirmware(t.Context(), bootPort, ZMKID, "")
		wantError(t, err, "Serial port not found")
		if ok || time.Since(start) < f.Timeout {
			t.Errorf("gave up after %v", time.Since(start))
		}
	})
}

func TestEnterBootloaderAsksDygmasFirmware(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := 0
		f := newFixture(func(context.Context) ([]usbserial.Port, error) {
			if asked > 0 {
				return ports(bootPort, otherBoot), nil
			}
			return ports(stockPort, otherBoot), nil
		})
		f.Ask = func(_ context.Context, p usbserial.Port) error {
			if p != stockPort {
				t.Errorf("asked %v", p)
			}
			asked++
			return nil
		}
		boot, studio, err := f.EnterBootloader(t.Context(), stockPort.Path, "")
		if err != nil || boot != bootPort || studio != (usbserial.Port{}) || asked != 1 || len(f.touches) > 0 {
			t.Errorf("got %v, %v, %v after %d requests and %d touches", boot, studio, err, asked, len(f.touches))
		}
	})
	// Without Ask, Dygma's firmware needs the button.
	f := newFixture(states(ports(stockPort)).list)
	_, _, err := f.EnterBootloader(t.Context(), stockPort.Path, "")
	wantError(t, err, "is the Neuron running Dygma's firmware, not its bootloader")
	// No location: nothing is asked.
	f = newFixture(states(ports(with(stockPort, func(p *usbserial.Port) { p.Location = "" }))).list)
	f.Ask = func(context.Context, usbserial.Port) error {
		t.Error("asked without a location")
		return nil
	}
	_, _, err = f.EnterBootloader(t.Context(), stockPort.Path, "")
	wantError(t, err, "does not say which USB socket /dev/stock is in")
	// A failed request says so.
	f = newFixture(states(ports(stockPort)).list)
	f.Ask = func(context.Context, usbserial.Port) error { return errors.New("unsupported Neuron firmware version") }
	_, _, err = f.EnterBootloader(t.Context(), stockPort.Path, "")
	wantError(t, err, "requesting the bootloader through /dev/stock: unsupported Neuron firmware version")
}

func TestEnterBootloaderFromTheLoader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := states(ports(loaderPort, otherBoot), ports(loaderPort, otherBoot),
			ports(loaderPort, bootPort, otherBoot), ports(bootPort, otherBoot))
		f := newFixture(s.list)
		boot, from, err := f.EnterBootloader(t.Context(), loaderPort.Path, loaderPort.Serial)
		if err != nil || boot != bootPort || from != loaderPort {
			t.Errorf("got %v, %v, %v", boot, from, err)
		}
		// The loader has one port, so nothing asks which is the updater's.
		if s.calls != 4 || !slices.Equal(f.touches, []string{loaderPort.Path}) || len(f.identified) != 0 {
			t.Errorf("listed %d times, touched %q, identified %q", s.calls, f.touches, f.identified)
		}
	})
}

func TestEnterBootloaderFromTheLoaderRefusesBeforeTheTouch(t *testing.T) {
	for _, tt := range []struct {
		name   string
		listed [][]usbserial.Port
		serial string
		want   string
	}{
		{"another serial number", [][]usbserial.Port{ports(loaderPort)}, "DIFFERENT",
			"/dev/loader is not the Neuron with USB serial number DIFFERENT; nothing was sent"},
		{"no location", [][]usbserial.Port{ports(with(loaderPort, func(p *usbserial.Port) { p.Location = "" }))}, "",
			"does not say which USB socket /dev/loader is in"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(states(tt.listed...).list)
			_, _, err := f.EnterBootloader(t.Context(), loaderPort.Path, tt.serial)
			wantError(t, err, tt.want)
			if len(f.touches)+len(f.identified) != 0 {
				t.Errorf("touched %q, identified %q", f.touches, f.identified)
			}
		})
	}
}

// WaitForStart takes the drive only from the Neuron's own loader, at the
// bootloader's USB location with ZMK's serial number, and WaitForFirmware
// never takes it.
func TestWaitForStartTakesOnlyTheNeuronsLoader(t *testing.T) {
	for _, p := range []usbserial.Port{
		with(loaderPort, func(p *usbserial.Port) { p.Location = "1-2" }),
		with(loaderPort, func(p *usbserial.Port) { p.Serial = "different" }),
		// Zephyr's samples share the loader's testing ID.
		with(loaderPort, func(p *usbserial.Port) { p.Product = "USBD MSC sample" }),
	} {
		synctest.Test(t, func(t *testing.T) {
			f := newFixture(states(ports(p)).list)
			start := time.Now()
			got, ok, err := f.WaitForStart(t.Context(), bootPort, studioPort.Serial)
			if ok || err != nil || time.Since(start) != f.Timeout {
				t.Errorf("%+v: got %v, %v, %v after %v", p, got, ok, err, time.Since(start))
			}
		})
	}
	synctest.Test(t, func(t *testing.T) {
		// Any serial number counts when none is given.
		f := newFixture(states(ports(bootPort), ports(with(loaderPort, func(p *usbserial.Port) { p.Serial = "" }))).list)
		if got, ok, err := f.WaitForStart(t.Context(), bootPort, ""); err != nil || !ok || got.Path != loaderPort.Path {
			t.Errorf("got %v, %v, %v", got, ok, err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(states(ports(loaderPort)).list)
		if got, ok, err := f.WaitForFirmware(t.Context(), bootPort, usbserial.USBID{}, ""); ok || err != nil {
			t.Errorf("got %v, %v, %v", got, ok, err)
		}
	})
}
