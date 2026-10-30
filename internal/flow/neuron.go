// SPDX-License-Identifier: MIT

package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/focus"
	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// Neuron says which Neuron a Neuron flow works on.
type Neuron struct {
	// Port is the Studio port of a Neuron running ZMK, the port of its UF2
	// loader, or its bootloader's port. Empty means the one Neuron
	// connected.
	Port string
	// Serial, if set, is the USB serial number the port must have.
	Serial string
	// Timeout is how long the bootloader, or the firmware after it, may
	// take to appear.
	Timeout time.Duration
}

// DefaultNeuronTimeout is the usual Neuron.Timeout.
const DefaultNeuronTimeout = 10 * time.Second

var (
	// errS140Differs reports that S140 on the Neuron is not Dygma's.
	errS140Differs = errors.New("S140 on the Neuron is not Dygma's S140 7.2.0")
	// errNotReturned reports that the Neuron's firmware did not start
	// after the bootloader was told to start it: its USB ports did not
	// appear in time.
	errNotReturned = errors.New("the Neuron's firmware did not start")
)

// notReturned is errNotReturned with the time waited, and what to do.
func notReturned(timeout time.Duration) error {
	return fmt.Errorf("%w within %v. %s", errNotReturned, timeout, notReturnedGuidance)
}

// replugNoButton starts what to do when the Neuron's firmware did not
// start. With the button held, the Neuron would stay in its bootloader.
const replugNoButton = "Unplug the Neuron and plug it back in without holding its button"

// notReturnedGuidance says what to do when the Neuron's firmware did not
// start.
const notReturnedGuidance = replugNoButton + ", then check that typing works."

// bootloaderName names the bootloader that the neuron package checks for.
const bootloaderName = "Dygma's n2_bootloader 1.0.0"

// neuronDetailPages is how many pages a Neuron write goes between detail
// lines.
const neuronDetailPages = 16

// bootloader is the Neuron's bootloader, open.
type bootloader struct {
	env    Env
	entry  *neuron.Entry
	conn   Port // nil once closed
	client *neuron.BootloaderClient
	port   usbserial.Port // the bootloader's port
	studio usbserial.Port // the Studio port it was entered from, if any
}

// askFunc asks Dygma's firmware on a port for its bootloader.
type askFunc func(context.Context, usbserial.Port) error

// openBootloader finds the Neuron's bootloader, entering it from ZMK's
// Studio port or the UF2 loader's port when touch is set, and opens its
// port. With ask set too, it enters from Dygma's firmware as well, through
// ask, which erases Dygma's settings.
func openBootloader(ctx context.Context, env Env, n Neuron, touch bool, ask askFunc) (*bootloader, error) {
	b := &bootloader{env: env, entry: env.entry(n.Timeout)}
	fromStock := touch && ask != nil
	if fromStock {
		b.entry.Ask = ask
	}
	path := n.Port
	if path == "" {
		var err error
		if path, err = findNeuron(ctx, env, b.entry, n.Serial, fromStock); err != nil {
			return nil, err
		}
	}
	var from usbserial.Port
	var err error
	if touch {
		b.port, from, err = b.entry.EnterBootloader(ctx, path, n.Serial)
	} else {
		b.port, err = b.entry.FindBootloader(ctx, path, n.Serial)
	}
	if err != nil {
		return nil, err
	}
	switch {
	case neuron.ModeOf(from) == neuron.Loader:
		// b.studio stays empty, so ZMK is then followed by its USB
		// location alone, as from a bootloader found waiting.
		env.say("Entered the Neuron's bootloader from its UF2 loader through %s.", from.Path)
	case from.Path != "":
		b.studio = from
		env.say("Entered the Neuron's bootloader from ZMK through %s.", b.studio.Path)
	case fromStock && path != b.port.Path:
		env.say("Entered the Neuron's bootloader from Dygma's firmware through %s.", path)
	}
	if err := usbserial.Recheck(ctx, b.port, env.List); err != nil {
		return nil, err
	}
	if b.conn, err = openWhenReady(ctx, env, b.port.Path, n.Timeout); err != nil {
		return nil, err
	}
	b.client = neuron.NewBootloaderClient(b.conn)
	return b, nil
}

// onePortWait is how long the user's commands wait for a Neuron running ZMK
// that lists only one of its two ports, as it does for a moment after it
// enumerates, before they say so; onePortPoll is how often they try.
const (
	onePortWait = 5 * time.Second
	onePortPoll = 500 * time.Millisecond
)

// whenBothPorts runs enter, and runs it again every onePortPoll, for up to
// onePortWait, while it fails with neuron.ErrOnePort. enter finds that
// error before it sends anything, so a retry is safe.
func whenBothPorts(ctx context.Context, enter func() error) error {
	deadline := time.Now().Add(onePortWait)
	for {
		err := enter()
		if !errors.Is(err, neuron.ErrOnePort) || !time.Now().Before(deadline) {
			return err
		}
		if err := sleep(ctx, onePortPoll); err != nil {
			return err
		}
	}
}

// openWhenReady opens for a session a port that may have just appeared. A
// device that has just enumerated can list a port before the port opens,
// as Windows does, so a failed open is tried again every 100 ms until
// timeout passes.
func openWhenReady(ctx context.Context, env Env, path string, timeout time.Duration) (Port, error) {
	deadline := time.Now().Add(timeout)
	for {
		p, err := env.openSession(path, timeout)
		if err == nil || !time.Now().Before(deadline) {
			return p, err
		}
		if err := sleep(ctx, 100*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// askStock returns a neuron.Entry.Ask that asks Dygma's firmware for its
// bootloader, as Bazecor does, which erases Dygma's settings. w waits for
// the left half's Esc key when Dygma's firmware asks for it.
func (env Env) askStock(timeout time.Duration, w focus.ReadyWait) askFunc {
	return func(ctx context.Context, p usbserial.Port) error {
		conn, err := openWhenReady(ctx, env, p.Path, timeout)
		if err != nil {
			return err
		}
		env.say("Asking Dygma's firmware for its bootloader, as Bazecor does. Dygma's firmware erases its own " +
			"settings, which hold the layout, as it goes.")
		err = focus.EnterBootloader(ctx, conn, w)
		closeErr := conn.Close()
		if err != nil {
			return errors.Join(err, closeErr)
		}
		// The Neuron resets into its bootloader, so the close may fail.
		return nil
	}
}

// close closes the bootloader's port, if it is still open.
func (b *bootloader) close() error {
	if b.conn == nil {
		return nil
	}
	conn := b.conn
	b.conn = nil
	if err := conn.Close(); err != nil {
		return fmt.Errorf("closing the bootloader's port %s: %w", b.port.Path, err)
	}
	return nil
}

// progress reports a write: the bootloader once identified, the erase, then
// every neuronDetailPages pages and the last. bar, if set, is called for
// every page too.
func (b *bootloader) progress(eraseEnd uint32, keepSettings bool, bar neuron.Progress) neuron.Progress {
	return func(done, total int) {
		if bar != nil {
			bar(done, total)
		}
		if done == 0 {
			b.env.say("Found %s at %s.", bootloaderName, b.port.Path)
			b.env.say("Erasing 0x%05x..0x%05x.", firmware.NeuronCodeStart, eraseEnd)
			if keepSettings {
				b.env.say("Keeping the ZMK settings at 0x%05x..0x%05x.", firmware.NeuronSettingsStart, firmware.NeuronSettingsEnd)
			}
			return
		}
		if done%neuronDetailPages == 0 || done == total {
			b.env.say("Neuron: %d of %d pages written and checked.", done, total)
		}
	}
}

// sent closes the bootloader's port after S# and says whether the
// bootloader acknowledged it; startErr is Start's result.
func (b *bootloader) sent(startErr error) error {
	if err := b.close(); err != nil {
		return err
	}
	if startErr == nil {
		b.env.say("The bootloader acknowledged S#.")
	} else {
		b.env.say("S# was not acknowledged: %v", startErr)
	}
	return nil
}

// finish is sent, then waits for the firmware with USB ID want, or for any
// firmware when want is zero. It returns the firmware's port, and whether
// it came back in time.
func (b *bootloader) finish(ctx context.Context, startErr error, want usbserial.USBID) (usbserial.Port, bool, error) {
	if err := b.sent(startErr); err != nil {
		return usbserial.Port{}, false, err
	}
	p, ok, err := b.entry.WaitForFirmware(ctx, b.port, want, b.studio.Serial)
	if err != nil || !ok {
		return usbserial.Port{}, false, err
	}
	b.env.say("The firmware is back at %s, USB %v, serial %s.", p.Path, p.ID, cmp.Or(p.Serial, "unavailable"))
	return p, true, nil
}

// findNeuron returns the port of the one Neuron connected: its bootloader's,
// its UF2 loader's, the Studio port of ZMK, or with fromStock the port of
// Dygma's firmware. With serial set, only ZMK or the UF2 loader with that
// USB serial number counts, since the bootloader's serial number is its
// own.
func findNeuron(ctx context.Context, env Env, entry *neuron.Entry, serial string, fromStock bool) (string, error) {
	ports, err := env.List(ctx)
	if err != nil {
		return "", err
	}
	var boots, zmk, loaders, stock []device
	for _, d := range neurons(ports) {
		ours := serial == "" || d.ports[0].Serial == serial
		switch {
		case d.mode == neuron.Bootloader && serial == "":
			boots = append(boots, d)
		case d.mode == neuron.ZMK && ours:
			zmk = append(zmk, d)
		case d.mode == neuron.Loader && ours:
			loaders = append(loaders, d)
		case d.mode == neuron.Stock && ours:
			stock = append(stock, d)
		}
	}
	candidates := len(boots) + len(zmk) + len(loaders)
	if fromStock {
		candidates += len(stock)
	}
	switch {
	case candidates > 1:
		return "", errors.New("more than one Neuron or Neuron bootloader is connected; choose one with --port")
	case len(boots) == 1:
		return boots[0].ports[0].Path, nil
	case len(loaders) == 1:
		return loaders[0].ports[0].Path, nil
	case fromStock && len(stock) == 1:
		return stock[0].ports[0].Path, nil
	case len(zmk) == 1:
		studio, err := entry.Studio(ctx, zmk[0].ports)
		if err != nil {
			return "", err
		}
		return studio.Path, nil
	case len(stock) > 0:
		return "", fmt.Errorf("the Neuron runs Dygma's firmware. advanced neuron write asks it for its bootloader, as "+
			"Bazecor does; for this command, enter the bootloader with the button: %s", neuron.EntryGuidance)
	case serial != "":
		return "", fmt.Errorf("no Neuron running ZMK with USB serial number %s", serial)
	}
	return "", errors.New("no Neuron found; connect it by USB, or give its port with --port")
}

// WriteNeuron writes a ZMK image to the Neuron, keeping the ZMK settings
// when asked, starts it and waits for it to come back.
func WriteNeuron(ctx context.Context, env Env, n Neuron, im *firmware.NeuronImage, keepSettings bool) error {
	ask := env.askStock(n.Timeout, focus.ReadyWait{Waiting: func() {
		env.say("Dygma's firmware is waiting for the left half's top-left Esc key, because the left half is cabled " +
			"and runs Dygma's firmware. Press and release it now, or unplug the half's cable and run this again.")
	}})
	_, back, err := writeNeuron(ctx, env, n, im, keepSettings, ask, nil)
	if err != nil {
		return err
	}
	if !back {
		env.say("The Neuron's firmware did not start within %v. %s Expected: ZMK, as USB %v.", n.Timeout,
			notReturnedGuidance, neuron.ZMKID)
	}
	return nil
}

// writeNeuron is WriteNeuron, with ask entering the bootloader from Dygma's
// firmware, and bar, if set, called for every page. It returns ZMK's
// Studio port, and whether ZMK came back.
func writeNeuron(ctx context.Context, env Env, n Neuron, im *firmware.NeuronImage, keepSettings bool, ask askFunc,
	bar neuron.Progress) (usbserial.Port, bool, error) {
	eraseEnd, err := im.EraseEnd(keepSettings)
	if err != nil {
		return usbserial.Port{}, false, err
	}
	env.say("Neuron image %s: 0x%05x..0x%05x in %d pages.", im.Name, firmware.NeuronCodeStart, im.End, len(im.Pages))
	b, err := openBootloader(ctx, env, n, true, ask)
	if err != nil {
		return usbserial.Port{}, false, err
	}
	defer b.close()
	if err := b.client.Write(ctx, im, keepSettings, b.progress(eraseEnd, keepSettings, bar)); err != nil {
		return usbserial.Port{}, false, err
	}
	env.say("Neuron written, and every page checked.")
	return b.finish(ctx, b.client.Start(ctx), neuron.ZMKID)
}

// RestoreStock writes Dygma's firmware and S140 back to the Neuron, starts
// it and waits for it to come back.
func RestoreStock(ctx context.Context, env Env, n Neuron, stock *firmware.StockNeuron) error {
	_, err := restoreStock(ctx, env, n, stock, nil)
	return err
}

// restoreStock is RestoreStock, with bar, if set, called for every page.
// It returns the port of Dygma's firmware, or none when the firmware did
// not start.
func restoreStock(ctx context.Context, env Env, n Neuron, stock *firmware.StockNeuron, bar neuron.Progress) (usbserial.Port, error) {
	sd := stock.SoftDevice
	env.say("Dygma's Neuron firmware %s: 0x%05x..0x%05x in %d pages.", stock.Name, firmware.NeuronStockStart, stock.End, len(stock.Pages))
	env.say("S140 %s: 0x%05x..0x%05x in %d pages.", sd.Name, firmware.NeuronCodeStart, sd.End, len(sd.Pages))
	b, err := openBootloader(ctx, env, n, true, nil)
	if err != nil {
		return usbserial.Port{}, err
	}
	defer b.close()
	if err := b.client.WriteStock(ctx, stock, b.progress(firmware.NeuronSettingsEnd, false, bar)); err != nil {
		return usbserial.Port{}, err
	}
	env.say("Dygma's Neuron firmware and S140 written, and every page checked.")
	p, back, err := b.finish(ctx, b.client.Start(ctx), neuron.StockID)
	if err != nil {
		return usbserial.Port{}, err
	}
	if !back {
		env.say("The Neuron's firmware did not start within %v. %s Expected: Dygma's firmware, as USB %v.", n.Timeout,
			notReturnedGuidance, neuron.StockID)
	}
	return p, nil
}

// CheckS140 compares S140 on the Neuron with Dygma's, page by page, without
// erasing or writing, then starts the firmware. It fails with
// errS140Differs when a page differs.
func CheckS140(ctx context.Context, env Env, n Neuron, sd *firmware.SoftDevice) error {
	env.say("S140 %s: 0x%05x..0x%05x in %d pages.", sd.Name, firmware.NeuronCodeStart, sd.End, len(sd.Pages))
	b, err := openBootloader(ctx, env, n, true, nil)
	if err != nil {
		return err
	}
	defer b.close()
	check, err := b.client.CheckS140(ctx, sd)
	if err != nil {
		return err
	}
	env.say("%v. Nothing was erased or written.", check)
	_, back, err := b.finish(ctx, b.client.Start(ctx), usbserial.USBID{})
	if err != nil {
		return err
	}
	if !back {
		return notReturned(n.Timeout)
	}
	if !check.Matches() {
		return errS140Differs
	}
	return nil
}

// EnterBootloader enters the Neuron's bootloader, identifies it and leaves
// it waiting there. Nothing is written.
func EnterBootloader(ctx context.Context, env Env, n Neuron) error {
	b, err := openBootloader(ctx, env, n, true, nil)
	if err != nil {
		return err
	}
	defer b.close()
	if err := b.client.Identify(ctx); err != nil {
		return err
	}
	env.say("Found %s at %s. Nothing was written.", bootloaderName, b.port.Path)
	return b.close()
}

// StartNeuron identifies the Neuron's bootloader, starts the firmware and
// waits for it to come back. It takes only the bootloader's port.
func StartNeuron(ctx context.Context, env Env, n Neuron) error {
	b, err := openBootloader(ctx, env, n, false, nil)
	if err != nil {
		return err
	}
	defer b.close()
	if err := b.client.Identify(ctx); err != nil {
		return err
	}
	_, back, err := b.finish(ctx, b.client.Start(ctx), usbserial.USBID{})
	if err != nil {
		return err
	}
	if !back {
		return notReturned(n.Timeout)
	}
	return nil
}

// startFromDrive enters the bootloader of a Neuron showing its UF2
// loader's drive, from the loader's port, identifies it and starts the
// firmware, as StartNeuron does. What starts is the loader, which starts
// ZMK when ZMK is complete and shows its drive again otherwise. It returns
// the port that came back, ZMK's Studio port or the loader's, and whether
// either did within n.Timeout. n.Serial is ZMK's USB serial number, which
// the loader shares.
func startFromDrive(ctx context.Context, env Env, n Neuron) (usbserial.Port, bool, error) {
	b, err := openBootloader(ctx, env, n, true, nil)
	if err != nil {
		return usbserial.Port{}, false, err
	}
	defer b.close()
	if err := b.client.Identify(ctx); err != nil {
		return usbserial.Port{}, false, err
	}
	if err := b.sent(b.client.Start(ctx)); err != nil {
		return usbserial.Port{}, false, err
	}
	p, ok, err := b.entry.WaitForStart(ctx, b.port, n.Serial)
	switch {
	case err != nil || !ok:
		return usbserial.Port{}, false, err
	case neuron.ModeOf(p) == neuron.Loader:
		env.say("The UF2 loader shows its DEFY drive again, at %s.", p.Path)
	default:
		env.say("ZMK is back at %s, USB %v, serial %s.", p.Path, p.ID, cmp.Or(p.Serial, "unavailable"))
	}
	return p, true, nil
}
