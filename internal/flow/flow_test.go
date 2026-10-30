// SPDX-License-Identifier: MIT

package flow

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/borovaka/defy-installer/internal/focus/focustest"
	"github.com/borovaka/defy-installer/internal/wiretest"
)

func load(t *testing.T, name string) *wiretest.Scenario {
	t.Helper()
	return wiretest.LoadScenario(t, filepath.Join("testdata", name))
}

// opens returns s with only the opens listed, by index.
func opens(s *wiretest.Scenario, indexes ...int) *wiretest.Scenario {
	out := &wiretest.Scenario{Comment: s.Comment}
	for _, i := range indexes {
		out.Opens = append(out.Opens, s.Opens[i])
	}
	return out
}

func TestStartNeuronFromTheLoaderSaysToReplug(t *testing.T) {
	for _, port := range []string{"", loaderPort.Path} {
		err := StartNeuron(t.Context(), listed(t, loaderPort), Neuron{Port: port, Timeout: DefaultNeuronTimeout})
		wantError(t, err, "/dev/loader is the Neuron's UF2 loader, not its bootloader, so nothing was sent.")
	}
}

// withStock serves /dev/stock from f, and every other port as env does.
func withStock(env Env, f *focustest.Neuron) Env {
	open := env.Open
	env.Open = func(path string, timeout time.Duration) (Port, error) {
		if path == stockPort.Path {
			return f, nil
		}
		return open(path, timeout)
	}
	return env
}

func TestWriteNeuronFromDygmasFirmware(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Dygma's firmware enters its bootloader once the left half, cabled
		// with Dygma's firmware, sees Esc, which is asked for once while the
		// updater waits. The rest is the recorded write from the bootloader.
		k, env := newKeyboard(t, load(t, "neuron-write-from-bootloader.json"), "stock", "zmk")
		f := focustest.New(t)
		f.ReadyAfter = 3
		f.OnReboot = func() { k.SetMode("boot") }
		if err := WriteNeuron(t.Context(), withStock(env, f), Neuron{Timeout: DefaultNeuronTimeout}, recordedImage(t), false); err != nil {
			t.Fatal(err)
		}
		wantOutput(t, k.out.String(), "Entered the Neuron's bootloader from Dygma's firmware through /dev/stock.")
		if n := strings.Count(k.out.String(), "waiting for the left half's top-left Esc key"); n != 1 {
			t.Errorf("asked for Esc %d times:\n%s", n, k.out.String())
		}
	})
}

func TestOnlyNeuronWriteAsksDygmasFirmware(t *testing.T) {
	// The commands that write nothing would leave Dygma's settings erased,
	// so they need the button. listed fails the test if a port opens.
	err := EnterBootloader(t.Context(), listed(t, stockPort), Neuron{Timeout: DefaultNeuronTimeout})
	wantError(t, err, "enter the bootloader with the button")
}

func TestFirmwareThatDoesNotReturn(t *testing.T) {
	bubble(t, "write", func(t *testing.T) {
		// The bootloader stays: a write still succeeds, and says so.
		k, env := newKeyboard(t, opens(load(t, "neuron-write-from-bootloader.json"), 0), "boot", "boot")
		start := time.Now()
		if err := WriteNeuron(t.Context(), env, neuronAt("/dev/boot"), recordedImage(t), false); err != nil {
			t.Fatal(err)
		}
		if waited := time.Since(start); waited < DefaultNeuronTimeout {
			t.Errorf("gave up after %v", waited)
		}
		wantOutput(t, k.out.String(), "The bootloader acknowledged S#.",
			"The Neuron's firmware did not start within 10s. Unplug the Neuron", "Expected: ZMK, as USB 1d50:615e.")
	})
	bubble(t, "start", func(t *testing.T) {
		// Starting is all neuron start does, so there it is an error.
		_, env := newKeyboard(t, opens(load(t, "neuron-start.json"), 0), "boot", "boot")
		err := StartNeuron(t.Context(), env, neuronAt("/dev/boot"))
		if !errors.Is(err, errNotReturned) || !strings.Contains(err.Error(), notReturnedGuidance) {
			t.Errorf("got %v", err)
		}
	})
}

// failingOpens makes the first n opens of path fail, as Windows fails to
// open a port it lists a moment after the device enumerates.
func failingOpens(env Env, path string, n int) Env {
	open := env.Open
	env.Open = func(p string, timeout time.Duration) (Port, error) {
		if p == path && n > 0 {
			n--
			return nil, errors.New("opening " + p + ": Serial port not found")
		}
		return open(p, timeout)
	}
	return env
}

func TestPortsThatDoNotOpenYetAreRetried(t *testing.T) {
	bubble(t, "after entry", func(t *testing.T) {
		// The bootloader's port, listed once the touch has gone.
		_, env := newKeyboard(t, load(t, "neuron-write-keep-settings.json"), "zmk", "zmk")
		if err := WriteNeuron(t.Context(), failingOpens(env, "/dev/boot", 3), neuronAt("/dev/studio"), recordedImage(t), true); err != nil {
			t.Error(err)
		}
	})
	bubble(t, "never opens", func(t *testing.T) {
		// A bootloader port that never opens fails at the timeout.
		_, env := newKeyboard(t, opens(load(t, "neuron-write-from-bootloader.json")), "boot", "zmk")
		env = failingOpens(env, "/dev/boot", 1000)
		start := time.Now()
		err := WriteNeuron(t.Context(), env, neuronAt("/dev/boot"), recordedImage(t), false)
		if err == nil || !strings.Contains(err.Error(), "Serial port not found") || time.Since(start) < DefaultNeuronTimeout {
			t.Errorf("got %v after %v", err, time.Since(start))
		}
	})
}

// TestRestoreKeyboardFromZMK runs the whole return to stock from ZMK: the
// recorded Neuron restore, then the recorded restore of the halves through
// the firmware that came back.
func TestRestoreKeyboardFromZMK(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := load(t, "neuron-stock.json")
		s.Opens = append(s.Opens, load(t, "stock-halves.json").Opens...)
		k, env := newKeyboard(t, s, "zmk", "stock")
		err := RestoreKeyboard(t.Context(), env, neuronAt("/dev/studio"), stockFiles(t))
		if err != nil {
			t.Fatal(err)
		}
		wantOutput(t, k.out.String(),
			"Dygma's Neuron firmware and S140 written, and every page checked.",
			"The firmware is back at /dev/stock, USB 35ef:0012, serial Dygma.",
			"right half: written, validated and finished.",
			"left half: written, validated and finished.",
			"Both halves run Dygma's 2.3.0 keyscanner again.",
			"Dygma's settings at 0x72000..0x77000, which hold the layout, were not written")
	})
}

func TestRestoreKeyboardStopsWhenDygmasFirmwareDoesNotReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The Neuron stays in its bootloader after S#.
		s := load(t, "neuron-stock.json")
		k, env := newKeyboard(t, s, "zmk", "boot")
		err := RestoreKeyboard(t.Context(), env, neuronAt("/dev/studio"), stockFiles(t))
		if !errors.Is(err, errNotReturned) || !strings.Contains(err.Error(), "the halves were not written") {
			t.Errorf("got %v", err)
		}
		if strings.Contains(k.out.String(), "keyscanner") {
			t.Errorf("the halves were started:\n%s", k.out.String())
		}
	})
}

// TestRestoreKeyboardFindsADygmaNeuron: --serial picks the Neuron running
// Dygma's firmware as it picks others.
func TestRestoreKeyboardFindsADygmaNeuron(t *testing.T) {
	for _, tt := range []struct {
		name, port, serial string
		want               bool
		err                string
	}{
		{"its serial number", "", stockPort.Serial, true, ""},
		{"another serial number", "", testSerial, false, ""},
		{"its serial number at --port", "/dev/stock", stockPort.Serial, true, ""},
		{"another serial number at --port", "/dev/stock", testSerial, false, "is not the Neuron with USB serial number"},
	} {
		p, ok, err := runningStock(t.Context(), listed(t, stockPort), Neuron{Port: tt.port, Serial: tt.serial})
		if ok != tt.want || !matches(err, tt.err) || ok && p != stockPort {
			t.Errorf("%s: got %v, %v, %v", tt.name, p, ok, err)
		}
	}
}
