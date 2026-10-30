// SPDX-License-Identifier: MIT

package release

import (
	"context"
	"os"
	"path/filepath"

	"github.com/borovaka/defy-installer/internal/firmware"
)

// The names of the files from Dygma's 2.3.0 release that going back to
// stock writes.
const (
	StockNeuronName = "Wireless_neuron.hex"
	StockS140Name   = "n2_s140_nrf52_7.2.0_softdevice.hex"
	KeyscannerName  = "keyscanner.bin"
)

// Where FetchStock finds Dygma's files on GitHub: the Neuron's firmware and
// the keyscanner among the Defy_v2.3.0 release's files, and S140 in the
// repository at c44c3db, from 2023-05-17, the last commit to change it. A
// fixed commit keeps the address working if Dygma moves or renames the
// file on main.
const (
	stockRelease = "Dygmalab/Firmware-release/releases/download/Defy_v2.3.0/"
	stockS140    = "Dygmalab/Firmware-release/c44c3db39649ca5e60f8167a87be6ead056c0b92/Defy/" + StockS140Name
)

// Stock is Dygma's 2.3.0 firmware: the Neuron's, with the S140 it starts
// through, and the keyscanner for both halves.
type Stock struct {
	Neuron     *firmware.StockNeuron
	Keyscanner *firmware.HalfImage
}

// FetchStock downloads Dygma's 2.3.0 files from Dygma's GitHub and checks
// each against its pinned SHA-256.
func (c *Client) FetchStock(ctx context.Context) (*Stock, error) {
	files, err := c.fetchStock(ctx)
	if err != nil {
		return nil, err
	}
	return parseStock(files)
}

// fetchStock downloads Wireless_neuron.hex, S140 and keyscanner.bin.
func (c *Client) fetchStock(ctx context.Context) ([3]stockFile, error) {
	var got [3]stockFile
	for i, u := range c.stockURLs() {
		data, _, err := c.get(ctx, u, maxFileSize, false)
		if err != nil {
			return [3]stockFile{}, err
		}
		got[i] = stockFile{u, data}
	}
	return got, nil
}

// stockURLs returns the addresses of Wireless_neuron.hex, S140 and
// keyscanner.bin.
func (c *Client) stockURLs() [3]string {
	return [3]string{
		c.github() + "/" + stockRelease + StockNeuronName,
		c.raw() + "/" + stockS140,
		c.github() + "/" + stockRelease + KeyscannerName,
	}
}

// LoadStock reads Dygma's 2.3.0 files from a folder, under the names they
// have in Dygma's release, and checks each against its pinned SHA-256.
func (c *Client) LoadStock(dir string) (*Stock, error) {
	files, err := loadStock(dir)
	if err != nil {
		return nil, err
	}
	return parseStock(files)
}

// loadStock reads Wireless_neuron.hex, S140 and keyscanner.bin from dir.
func loadStock(dir string) ([3]stockFile, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return [3]stockFile{}, err
	}
	defer root.Close()
	var got [3]stockFile
	for i, name := range []string{StockNeuronName, StockS140Name, KeyscannerName} {
		data, err := readFile(root, dir, name, maxFileSize)
		if err != nil {
			return [3]stockFile{}, err
		}
		got[i] = stockFile{filepath.Join(dir, name), data}
	}
	return got, nil
}

// stockFile is one of Dygma's files, with the name its errors give.
type stockFile struct {
	name string
	data []byte
}

// parseStock checks Wireless_neuron.hex, S140 and keyscanner.bin against
// the SHA-256 pinned in the firmware package.
func parseStock(files [3]stockFile) (*Stock, error) {
	neuron, s140, keyscanner := files[0], files[1], files[2]
	stock, err := firmware.ParseStockNeuron(neuron.name, neuron.data, s140.name, s140.data)
	if err != nil {
		return nil, err
	}
	h, err := firmware.ParseHalfImage(keyscanner.name, keyscanner.data)
	if err != nil {
		return nil, err
	}
	if err := h.RequireStock(); err != nil {
		return nil, err
	}
	return &Stock{Neuron: stock, Keyscanner: h}, nil
}
