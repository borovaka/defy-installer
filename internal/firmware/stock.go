// SPDX-License-Identifier: MIT

package firmware

import "encoding/hex"

// The pinned SHA-256 sums of Dygma's 2.3.0 files that uninstall and
// advanced stock write, as zmk-defy-wireless's docs/neuron-flash.md lists
// them. The files are Dygma's and not part of this repository.
var (
	stockNeuronSHA256     = sha256Hex("975f37521d543942f9a9225a31929369b07b69a4b92053071e22dbaac82d52d1") // Wireless_neuron.hex
	s140SHA256            = sha256Hex("4f442805983e4b1245817cdf64369e305cc78959dcb30461b2e9dec049aabb24") // n2_s140_nrf52_7.2.0_softdevice.hex
	stockKeyscannerSHA256 = sha256Hex("02cfd5cd6a6291bd95e489f775f33e100a3deec91dbc54d24499dac1ae918c27") // keyscanner.bin
)

func sha256Hex(s string) [32]byte {
	var sum [32]byte
	if n, err := hex.Decode(sum[:], []byte(s)); err != nil || n != len(sum) {
		panic("invalid SHA-256 " + s)
	}
	return sum
}
