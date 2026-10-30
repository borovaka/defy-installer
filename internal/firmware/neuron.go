// SPDX-License-Identifier: MIT

package firmware

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
)

// The Neuron's flash, an nRF52833's, as zmk-defy-wireless's
// docs/neuron-flash.md lays it out. The installer writes only
// NeuronCodeStart..NeuronSettingsEnd: Nordic's MBR sits below, and Dygma's
// settings and bootloader above.
const (
	NeuronPageSize = 0x1000
	// NeuronCodeStart holds the vector table the bootloader starts: the
	// UF2 loader's, ZMK's in an image without the loader, or S140's under
	// Dygma's firmware.
	NeuronCodeStart     = 0x1000
	NeuronSettingsStart = 0x6C000
	NeuronSettingsEnd   = 0x72000
	// NeuronDygmaSettingsEnd ends Dygma's settings, which follow the ZMK
	// settings.
	NeuronDygmaSettingsEnd = 0x77000
	// NeuronStockStart is where Dygma's application sits, behind S140.
	NeuronStockStart = 0x27000

	neuronRAMStart = 0x20000000
	neuronRAMEnd   = 0x20020000
)

// NeuronImage is a ZMK image for the Neuron, with a vector table at
// NeuronCodeStart. The checks do not tell an image with the UF2 loader from
// one without.
type NeuronImage struct {
	Name   string
	SHA256 [32]byte
	// End is one past the image's last byte.
	End uint32
	// Pages are the image's 4 KiB pages by address.
	Pages []Page
}

// LoadNeuronImage reads and checks a ZMK Neuron image.
func LoadNeuronImage(path string) (*NeuronImage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseNeuronImage(path, raw)
}

// ParseNeuronImage checks a ZMK Neuron image: Intel HEX within
// NeuronCodeStart..NeuronSettingsEnd, with a vector table that starts it.
func ParseNeuronImage(name string, raw []byte) (*NeuronImage, error) {
	sum := sha256.Sum256(raw)
	if sum == stockNeuronSHA256 {
		return nil, fmt.Errorf("%s is Dygma's 2.3.0 Neuron firmware; defy-installer uninstall puts Dygma's "+
			"firmware back", name)
	}
	if !isASCII(raw) {
		return nil, fmt.Errorf("%s: Neuron firmware must be an Intel HEX file", name)
	}
	mem, err := parseHex(raw, NeuronCodeStart, NeuronSettingsEnd, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid Neuron firmware (%v); its data must lie within 0x%x..0x%x",
			name, err, NeuronCodeStart, NeuronSettingsEnd)
	}
	vectors, ok := mem.Bytes(NeuronCodeStart, 8)
	if !ok {
		return nil, fmt.Errorf("%s: Neuron firmware has no vector table at 0x%x; "+
			"a build that starts after S140, at 0x%x, is not written", name, NeuronCodeStart, NeuronStockStart)
	}
	stack, reset := binary.LittleEndian.Uint32(vectors), binary.LittleEndian.Uint32(vectors[4:])
	if stack%4 != 0 || stack <= neuronRAMStart || stack > neuronRAMEnd {
		return nil, fmt.Errorf("%s: Neuron firmware's initial stack pointer is outside the nRF52833's RAM", name)
	}
	if reset&1 == 0 || !mem.Has(reset&^1) {
		return nil, fmt.Errorf("%s: Neuron firmware's reset vector is not Thumb code inside the image", name)
	}
	return &NeuronImage{Name: name, SHA256: sum, End: mem.End(), Pages: mem.Pages(NeuronPageSize)}, nil
}

// EraseEnd returns where the erase before writing the image ends:
// NeuronSettingsEnd, or NeuronSettingsStart when the settings are kept. An
// image that reaches past it is refused.
func (im *NeuronImage) EraseEnd(keepSettings bool) (uint32, error) {
	if !keepSettings {
		return NeuronSettingsEnd, nil
	}
	if im.End > NeuronSettingsStart {
		return 0, fmt.Errorf("%s reaches into the ZMK settings at 0x%x..0x%x, which keeping the settings leaves alone",
			im.Name, NeuronSettingsStart, NeuronSettingsEnd)
	}
	return NeuronSettingsStart, nil
}

// SoftDevice is Nordic's S140 7.2.0 from Dygma's 2.3.0 release, which
// Dygma's firmware starts through. The file's MBR records, below
// NeuronCodeStart, are left out: the MBR is never written.
type SoftDevice struct {
	Name  string
	End   uint32
	Pages []Page
}

// LoadSoftDevice reads and checks Dygma's S140 file.
func LoadSoftDevice(path string) (*SoftDevice, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSoftDevice(path, raw)
}

// parseSoftDevice checks Dygma's n2_s140_nrf52_7.2.0_softdevice.hex.
func parseSoftDevice(name string, raw []byte) (*SoftDevice, error) {
	if err := checkPinned(name, raw, s140SHA256, "Dygma's n2_s140_nrf52_7.2.0_softdevice.hex"); err != nil {
		return nil, err
	}
	mem, err := parseHex(raw, NeuronCodeStart, NeuronStockStart, NeuronCodeStart)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid S140 file (%v); apart from the MBR below 0x%x, its data must lie within 0x%x..0x%x",
			name, err, NeuronCodeStart, NeuronCodeStart, NeuronStockStart)
	}
	if _, ok := mem.Bytes(NeuronCodeStart, 8); !ok {
		return nil, fmt.Errorf("%s: S140 file has no vector table at 0x%x", name, NeuronCodeStart)
	}
	return &SoftDevice{Name: name, End: mem.End(), Pages: mem.Pages(NeuronPageSize)}, nil
}

// StockNeuron is Dygma's 2.3.0 Neuron firmware: its application from
// NeuronStockStart, and the S140 it starts through.
type StockNeuron struct {
	Name       string
	End        uint32
	Pages      []Page
	SoftDevice *SoftDevice
}

// LoadStockNeuron reads and checks Dygma's Wireless_neuron.hex and S140
// file.
func LoadStockNeuron(path, s140Path string) (*StockNeuron, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s140, err := os.ReadFile(s140Path)
	if err != nil {
		return nil, err
	}
	return ParseStockNeuron(path, raw, s140Path, s140)
}

// ParseStockNeuron checks Dygma's Wireless_neuron.hex and S140 file
// against their pinned SHA-256 and the flash layout.
func ParseStockNeuron(name string, raw []byte, s140Name string, s140 []byte) (*StockNeuron, error) {
	if err := checkPinned(name, raw, stockNeuronSHA256, "Dygma's 2.3.0 Wireless_neuron.hex"); err != nil {
		return nil, err
	}
	mem, err := parseHex(raw, NeuronStockStart, NeuronSettingsEnd, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid Neuron firmware (%v); its data must lie within 0x%x..0x%x",
			name, err, NeuronStockStart, NeuronSettingsEnd)
	}
	softDevice, err := parseSoftDevice(s140Name, s140)
	if err != nil {
		return nil, err
	}
	return &StockNeuron{Name: name, End: mem.End(), Pages: mem.Pages(NeuronPageSize), SoftDevice: softDevice}, nil
}

func checkPinned(name string, raw []byte, pinned [32]byte, what string) error {
	if sum := sha256.Sum256(raw); sum != pinned {
		return fmt.Errorf("%s is not %s (SHA-256 %x); only the pinned 2.3.0 release file is accepted", name, what, sum)
	}
	return nil
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}
