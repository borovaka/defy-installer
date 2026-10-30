// SPDX-License-Identifier: MIT

package flow

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
	"github.com/borovaka/defy-installer/internal/wiretest"
)

// The ports of the scenarios' Neuron, which defy has too.
var (
	studioPort  = wiretest.StudioPort
	updaterPort = wiretest.UpdaterPort
	bootPort    = wiretest.BootPort
	stockPort   = wiretest.StockPort
	loaderPort  = wiretest.LoaderPort
)

const testSerial = wiretest.Serial

// keyboard is the scenarios' Neuron, and what the flow said.
type keyboard struct {
	*wiretest.Keyboard
	out bytes.Buffer
}

// newKeyboard plays s from the Neuron in mode, "zmk", "boot", "stock" or
// "loader". returns is the mode the bootloader starts.
func newKeyboard(t *testing.T, s *wiretest.Scenario, mode, returns string) (*keyboard, Env) {
	k := &keyboard{Keyboard: s.Keyboard(t, mode, returns)}
	env := Env{
		List: k.List,
		Open: func(path string, _ time.Duration) (Port, error) {
			p, err := k.Open(path)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Out:  &k.out,
		Rand: nonce(0xabcdef01),
	}
	return k, env
}

// nonce is a Rand that gives one updater session nonce.
func nonce(n uint32) io.Reader {
	return bytes.NewReader(binary.LittleEndian.AppendUint32(nil, n))
}

// recordedImage is the ZMK image the scenarios were recorded with.
func recordedImage(t *testing.T) *firmware.NeuronImage {
	t.Helper()
	im, err := firmware.ParseNeuronImage("zmk.hex", ft.RecordedNeuronHex())
	if err != nil {
		t.Fatal(err)
	}
	return im
}

// recordedStock stands in for Dygma's files as the scenarios did. The pins
// are not checked here; the commands check them when they load the files.
func recordedStock() *firmware.StockNeuron {
	pages := func(spans []ft.Span) ([]firmware.Page, uint32) {
		laid, end := ft.Pages(firmware.NeuronPageSize, spans...)
		var pages []firmware.Page
		for _, p := range laid {
			// The S140 file's MBR records are never written.
			if p.Addr >= firmware.NeuronCodeStart {
				pages = append(pages, firmware.Page(p))
			}
		}
		return pages, end
	}
	app, s140 := ft.RecordedStock()
	appPages, appEnd := pages(app)
	sdPages, sdEnd := pages(s140)
	return &firmware.StockNeuron{Name: "Wireless_neuron.hex", End: appEnd, Pages: appPages,
		SoftDevice: &firmware.SoftDevice{Name: "s140.hex", End: sdEnd, Pages: sdPages}}
}

// recordedHalves are the half images the scenarios were recorded with.
func recordedHalves(t *testing.T) (left, right *firmware.HalfImage) {
	t.Helper()
	parse := func(name string, side ft.Side) *firmware.HalfImage {
		h, err := firmware.ParseHalfImage(name, ft.HalfPackage(ft.RecordedHalfApp(0xa5), 17, side))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	return parse("left.bin", ft.Left), parse("right.bin", ft.Right)
}

// neuronAt is the Neuron at port, with the usual timeout.
func neuronAt(port string) Neuron {
	return Neuron{Port: port, Timeout: DefaultNeuronTimeout}
}

// scenarios runs, for each command recorded in testdata, the flow that
// replaces it, from the keyboard state the recording started in.
var scenarios = map[string]struct {
	mode, returns string
	run           func(t *testing.T, ctx context.Context, env Env) error
	want          error
}{
	"neuron-write-keep-settings.json": {"zmk", "zmk", func(t *testing.T, ctx context.Context, env Env) error {
		return WriteNeuron(ctx, env, neuronAt("/dev/studio"), recordedImage(t), true)
	}, nil},
	"neuron-write-from-bootloader.json": {"boot", "zmk", func(t *testing.T, ctx context.Context, env Env) error {
		return WriteNeuron(ctx, env, neuronAt("/dev/boot"), recordedImage(t), false)
	}, nil},
	"neuron-bootloader.json": {"zmk", "zmk", func(t *testing.T, ctx context.Context, env Env) error {
		return EnterBootloader(ctx, env, neuronAt("/dev/studio"))
	}, nil},
	"neuron-start.json": {"boot", "zmk", func(t *testing.T, ctx context.Context, env Env) error {
		return StartNeuron(ctx, env, neuronAt("/dev/boot"))
	}, nil},
	"neuron-stock.json": {"zmk", "stock", func(t *testing.T, ctx context.Context, env Env) error {
		return RestoreStock(ctx, env, neuronAt("/dev/studio"), recordedStock())
	}, nil},
	"neuron-s140-check.json": {"boot", "zmk", func(t *testing.T, ctx context.Context, env Env) error {
		return CheckS140(ctx, env, neuronAt("/dev/boot"), recordedStock().SoftDevice)
	}, errS140Differs},
	// The halves scenarios' PAIRING exchange was added by hand: the request,
	// and the refusal that Neuron firmware older than PAIRING sends.
	"halves-write.json": {"zmk", "zmk", func(t *testing.T, ctx context.Context, env Env) error {
		left, right := recordedHalves(t)
		return WriteHalves(ctx, env, Halves{Serial: testSerial, Timeout: DefaultHalvesTimeout}, left, right, false)
	}, nil},
	"halves-probe.json": {"zmk", "zmk", func(t *testing.T, ctx context.Context, env Env) error {
		return ProbeHalves(ctx, env, Halves{Serial: testSerial, Timeout: DefaultHalvesTimeout})
	}, nil},
	// Recorded from restore, which writes the halves through Dygma's
	// Neuron firmware: RestoreKeyboard on a Neuron already running it.
	"stock-halves.json": {"stock", "stock", func(t *testing.T, ctx context.Context, env Env) error {
		return RestoreKeyboard(ctx, env, Neuron{Timeout: DefaultNeuronTimeout}, stockFiles(t))
	}, nil},
}

// keyscannerStandIn stands in for Dygma's keyscanner.bin as the recording
// did.
func keyscannerStandIn(t *testing.T) *firmware.HalfImage {
	t.Helper()
	h, err := firmware.ParseHalfImage("keyscanner.bin", ft.HalfPackage(ft.RecordedHalfApp(0x0a), 101, ft.Left))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestScenarios replays command runs of an earlier Python version of this
// installer, which is not published: the Go flow must open the same ports
// in the same order, and exchange the same bytes and line changes on each.
func TestScenarios(t *testing.T) {
	files, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if _, ok := scenarios[filepath.Base(path)]; !ok {
			t.Errorf("%s has no test", path)
		}
	}
	for name, sc := range scenarios {
		bubble(t, name, func(t *testing.T) {
			s := wiretest.LoadScenario(t, filepath.Join("testdata", name))
			k, env := newKeyboard(t, s, sc.mode, sc.returns)
			err := sc.run(t, t.Context(), env)
			if !errors.Is(err, sc.want) || (sc.want == nil) != (err == nil) {
				t.Errorf("got %v, want %v", err, sc.want)
			}
			if (err == nil) != (s.Status == 0) {
				t.Errorf("the Python command exited %d", s.Status)
			}
			if t.Failed() {
				t.Logf("output:\n%s", k.out.String())
			}
		})
	}
}
