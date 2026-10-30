// SPDX-License-Identifier: MIT

package release

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchStock(t *testing.T) {
	g := newFakeGitHub(t)
	g.publish("Dygmalab/Firmware-release", "Defy_v2.3.0",
		map[string][]byte{StockNeuronName: []byte("neuron"), KeyscannerName: []byte("keyscanner")})
	g.setIntercept(func(w http.ResponseWriter, r *http.Request, assets bool) bool {
		if assets && strings.HasSuffix(r.URL.Path, "/"+StockS140Name) {
			w.Write([]byte("s140"))
			return true
		}
		return false
	})
	c := g.client("")

	// The real check refuses files that are not Dygma's.
	_, err := c.FetchStock(t.Context())
	wantError(t, err, g.github.URL+"/Dygmalab/Firmware-release/releases/download/Defy_v2.3.0/Wireless_neuron.hex "+
		"is not Dygma's 2.3.0 Wireless_neuron.hex")

	got, err := c.fetchStock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	urls := c.stockURLs()
	for i, want := range []string{"neuron", "s140", "keyscanner"} {
		if f := got[i]; f.name != urls[i] || string(f.data) != want {
			t.Errorf("file %d: %s %q, want %s %q", i, f.name, f.data, urls[i], want)
		}
	}

	g.setIntercept(nil)
	_, err = c.FetchStock(t.Context())
	wantError(t, err, "/Defy/n2_s140_nrf52_7.2.0_softdevice.hex: the server answered 404 Not Found")
}

func TestLoadStock(t *testing.T) {
	dir := writeFolder(t, map[string][]byte{
		StockNeuronName: []byte("neuron"), StockS140Name: []byte("s140"), KeyscannerName: []byte("keyscanner"),
	})
	_, err := (&Client{}).LoadStock(dir)
	wantError(t, err, "is not Dygma's 2.3.0 Wireless_neuron.hex")

	got, err := loadStock(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{StockNeuronName, StockS140Name, KeyscannerName} {
		if f := got[i]; f.name != filepath.Join(dir, name) {
			t.Errorf("file %d: %s", i, f.name)
		}
	}
	os.Remove(filepath.Join(dir, KeyscannerName))
	_, err = (&Client{}).LoadStock(dir)
	wantError(t, err, KeyscannerName)
}
