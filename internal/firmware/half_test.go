// SPDX-License-Identifier: MIT

package firmware

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	ft "github.com/borovaka/defy-installer/internal/firmware/firmwaretest"
)

// TestPackagesMatchThePythonFactory pins firmwaretest.HalfPackage to the
// bytes zmk-defy-wireless's package_factory.py builds, from its own tests.
func TestPackagesMatchThePythonFactory(t *testing.T) {
	for side, want := range map[ft.Side]string{
		ft.Left:  "5945e3bf12ca14f4a0aebf860ae62affa5764e084393829a8f06aa5e14cbe829",
		ft.Right: "1bbe6534c6a3e7838d351a689d750983dca5511ebf8d2cb95fc936d79bb5714b",
	} {
		sum := sha256.Sum256(ft.HalfPackage(ft.ValidHalfApplication(), 0x10203, side))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("side %d: SHA-256 %s, want %s", side, got, want)
		}
	}
}

func TestSealCRC(t *testing.T) {
	// The seal of Dygma's 2.3.0 keyscanner.bin.
	seal, _ := hex.DecodeString("010000001c0000009503f5110060000054480300c16b889c00000001")
	if got := sealCRC(seal); got != 0x11F50395 {
		t.Errorf("seal CRC 0x%08x, want 0x11F50395", got)
	}
}

func TestParseHalfImage(t *testing.T) {
	pkg := ft.HalfPackage(ft.ValidHalfApplication(), 17, ft.Left)
	h, err := ParseHalfImage("left.bin", pkg)
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != 17 || h.Side != Left || h.ProgramCRC != crc32.ChecksumIEEE(pkg[0x1000:0x1140]) {
		t.Errorf("got %+v", h)
	}
	data := h.TransferData()
	if len(data)%HalfSectorSize != 0 || !bytes.HasPrefix(data, pkg) ||
		!bytes.Equal(data[len(pkg):], bytes.Repeat([]byte{0xff}, len(data)-len(pkg))) {
		t.Error("transfer data is not the package padded with 0xFF to whole sectors")
	}
	if err := h.RequireApplication(Left); err != nil {
		t.Error(err)
	}
	wantError(t, h.RequireApplication(Right), "built for the left half, not the right")
	wantError(t, h.RequireStock(), "is not Dygma's 2.3.0 keyscanner.bin")
}

func TestHalfImageWithoutMetadata(t *testing.T) {
	// Dygma's keyscanner starts its program with code, not metadata.
	pkg := ft.HalfPackage(ft.ValidHalfApplication(), 17, ft.Left)
	copy(pkg[0x1000:0x1100], sequence(256))
	pkg = ft.Reseal(pkg)
	h, err := ParseHalfImage("keyscanner.bin", pkg)
	if err != nil {
		t.Fatal(err)
	}
	if h.Side != NoSide {
		t.Errorf("side %v", h.Side)
	}
	wantError(t, h.RequireApplication(Left), "rebuild the matching left or right target")
}

func TestStockKeyscanner(t *testing.T) {
	pkg := ft.HalfPackage(ft.ValidHalfApplication(), 17, ft.Left)
	copy(pkg[0x1000:0x1100], sequence(256))
	pkg = ft.Reseal(pkg)
	pin(t, &stockKeyscannerSHA256, pkg)
	h, err := ParseHalfImage("keyscanner.bin", pkg)
	if err != nil {
		t.Fatal(err)
	}
	if !h.isStock() || h.RequireStock() != nil {
		t.Error("the pinned keyscanner is not stock")
	}
	wantError(t, h.RequireApplication(Left), "is Dygma's 2.3.0 keyscanner")
}

func TestDamagedPackagesAreRejected(t *testing.T) {
	pkg := ft.HalfPackage(ft.ValidHalfApplication(), 17, ft.Left)
	for _, off := range []int{8, 16, 24, 0x1000, 0x1014, 0x1100, 0x1108} {
		damaged := bytes.Clone(pkg)
		damaged[off] ^= 1
		if _, err := ParseHalfImage("damaged.bin", damaged); err == nil {
			t.Errorf("byte 0x%x flipped: accepted", off)
		}
	}
	for name, data := range map[string][]byte{
		"seal only":     pkg[:20],
		"one byte less": pkg[:len(pkg)-1],
		"a page less":   pkg[:len(pkg)-256],
		"a page more":   append(bytes.Clone(pkg), make([]byte, 256)...),
	} {
		if _, err := ParseHalfImage(name, data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestResealedMetadataIsChecked(t *testing.T) {
	pkg := ft.HalfPackage(ft.ValidHalfApplication(), 17, ft.Left)
	for _, change := range []struct {
		offset int
		value  uint32
	}{
		{8, 2},   // schema
		{12, 28}, // size
		{16, 2},  // role
		{16, 99},
		{20, 2},  // side
		{24, 18}, // version
		{28, 1},  // reserved
		{32, 1},  // padding
	} {
		damaged := bytes.Clone(pkg)
		binary.LittleEndian.PutUint32(damaged[0x1000+change.offset:], change.value)
		if _, err := ParseHalfImage("resealed.bin", ft.Reseal(damaged)); err == nil {
			t.Errorf("0x%x at metadata offset %d: accepted", change.value, change.offset)
		}
	}
}

func TestHalfVectorsAreChecked(t *testing.T) {
	for _, v := range []struct{ stack, reset uint32 }{
		{0x20042008, ft.HalfVector + 0x21}, // stack past SRAM
		{0x20000000, ft.HalfVector + 0x21}, // stack at the bottom of SRAM
		{0x20041004, ft.HalfVector + 0x21}, // stack not 8-byte aligned
		{0x20041000, ft.HalfVector + 0x20}, // reset not Thumb
		{0x20041000, 0x10000101},           // reset into the boot stage
		{0x20041000, ft.HalfVector + 0x101},
	} {
		pkg := ft.HalfPackage(ft.HalfApplication(v.stack, v.reset), 1, ft.Left)
		if _, err := ParseHalfImage("vectors.bin", pkg); err == nil {
			t.Errorf("stack 0x%x, reset 0x%x: accepted", v.stack, v.reset)
		}
	}
}

func TestLoadHalfImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "left.bin")
	if err := os.WriteFile(path, ft.HalfPackage(ft.ValidHalfApplication(), 17, ft.Left), 0o644); err != nil {
		t.Fatal(err)
	}
	if h, err := LoadHalfImage(path); err != nil || h.Name != path {
		t.Errorf("got %v", err)
	}
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, make([]byte, HalfStorageStart), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadHalfImage(big)
	wantError(t, err, "overlaps settings storage")
}

func TestHalfPages(t *testing.T) {
	h, err := ParseHalfImage("left.bin", ft.HalfPackage(ft.ValidHalfApplication(), 17, ft.Left))
	if err != nil {
		t.Fatal(err)
	}
	pages := h.Pages()
	if len(pages) != 2*HalfSectorSize/HalfPageSize {
		t.Fatalf("%d pages", len(pages))
	}
	var joined []byte
	for i, p := range pages {
		if p.Addr != HalfPackageStart+uint32(i*HalfPageSize) || CheckHalfPage(p) != nil {
			t.Errorf("page %d at %#x: %v", i, p.Addr, CheckHalfPage(p))
		}
		joined = append(joined, p.Data...)
	}
	if !bytes.Equal(joined, h.TransferData()) {
		t.Error("the pages are not the transfer data")
	}
	page := make([]byte, HalfPageSize)
	for _, bad := range []Page{{0x4f00, page}, {HalfStorageStart, page}, {0x5001, page}, {0x5000, page[:255]},
		{HalfStorageStart - HalfPageSize, append(page, 0)}, {0xffffff00, page}} {
		wantError(t, CheckHalfPage(bad), "not an aligned 256-byte page within 0x5000..0x1c0000")
	}
}

// FuzzParseHalfImage fuzzes a program and seals it, so that inputs pass the
// seal's CRCs and reach the checks behind them.
func FuzzParseHalfImage(f *testing.F) {
	program := ft.HalfPackage(ft.ValidHalfApplication(), 17, ft.Left)[0x1000:0x1140]
	f.Add(program, uint32(17))
	f.Add(append(sequence(256), program[0x100:]...), uint32(17))
	f.Fuzz(func(t *testing.T, program []byte, version uint32) {
		h, err := ParseHalfImage("fuzz.bin", ft.Seal(program, version))
		if err != nil {
			return
		}
		pages := h.Pages()
		if len(h.TransferData())%HalfSectorSize != 0 || len(pages)*HalfPageSize != len(h.TransferData()) {
			t.Errorf("accepted %+v with %d pages", h, len(pages))
		}
		for _, p := range pages {
			if err := CheckHalfPage(p); err != nil {
				t.Error(err)
			}
		}
	})
}
