// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// thin returns a Mach-O header with no load commands, padded to size with
// bytes that differ between CPUs.
func thin(magic uint32, cpu macho.Cpu, subCpu uint32, typ macho.Type, size int) []byte {
	b := binary.LittleEndian.AppendUint32(nil, magic)
	b = binary.LittleEndian.AppendUint32(b, uint32(cpu))
	b = binary.LittleEndian.AppendUint32(b, subCpu)
	b = binary.LittleEndian.AppendUint32(b, uint32(typ))
	// The number and size of the load commands, and the flags.
	b = append(b, make([]byte, 12)...)
	if magic == macho.Magic64 {
		b = append(b, make([]byte, 4)...)
	}
	for len(b) < size {
		b = append(b, byte(cpu)+byte(len(b)))
	}
	return b
}

func amd64Exec(size int) input {
	return input{"amd64", thin(macho.Magic64, macho.CpuAmd64, 3, macho.TypeExec, size)}
}

func arm64Exec(size int) input {
	return input{"arm64", thin(macho.Magic64, macho.CpuArm64, 0, macho.TypeExec, size)}
}

// TestLayout pins the layout lipo -create gives the same two files: x86-64
// first at 4 KiB, then arm64 at the next multiple of 16 KiB.
func TestLayout(t *testing.T) {
	amd64, arm64 := amd64Exec(10000), arm64Exec(5000)
	fat, err := universal([]input{arm64, amd64})
	if err != nil {
		t.Fatal(err)
	}
	header := "cafebabe" + "00000002" +
		"01000007" + "00000003" + "00001000" + "00002710" + "0000000c" +
		"0100000c" + "00000000" + "00004000" + "00001388" + "0000000e"
	if got := hex.EncodeToString(fat[:48]); got != header {
		t.Errorf("header %s, want %s", got, header)
	}
	if len(fat) != 0x4000+5000 {
		t.Errorf("%d bytes, want %d", len(fat), 0x4000+5000)
	}
	if !bytes.Equal(fat[0x1000:0x1000+10000], amd64.data) || !bytes.Equal(fat[0x4000:], arm64.data) {
		t.Error("the executables are not copied byte for byte")
	}
	padding := append(bytes.Clone(fat[48:0x1000]), fat[0x1000+10000:0x4000]...)
	if !bytes.Equal(padding, make([]byte, len(padding))) {
		t.Error("the padding is not zeros")
	}

	f, err := macho.NewFatFile(bytes.NewReader(fat))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Arches) != 2 || f.Arches[0].Cpu != macho.CpuAmd64 || f.Arches[0].SubCpu != 3 ||
		f.Arches[1].Cpu != macho.CpuArm64 || f.Arches[1].SubCpu != 0 {
		t.Errorf("debug/macho reads the architectures as %+v", f.Arches)
	}

	again, err := universal([]input{amd64, arm64})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, fat) {
		t.Error("the order of the inputs changes the output")
	}
}

func TestErrors(t *testing.T) {
	for _, c := range []struct {
		name   string
		inputs []input
		want   string
	}{
		{"one", []input{arm64Exec(100)}, "needs at least two executables"},
		{"same CPU", []input{arm64Exec(100), arm64Exec(200)}, "arm64 and arm64 are both for arm64"},
		{"32-bit", []input{arm64Exec(100), {"386", thin(macho.Magic32, macho.Cpu386, 3, macho.TypeExec, 100)}},
			"386: a 32-bit Mach-O file, not a 64-bit one"},
		{"other CPU", []input{arm64Exec(100), {"ppc64", thin(macho.Magic64, macho.CpuPpc64, 0, macho.TypeExec, 100)}},
			"ppc64: a Mach-O file for ppc64, not amd64 or arm64"},
		{"object", []input{arm64Exec(100), {"amd64.o", thin(macho.Magic64, macho.CpuAmd64, 3, macho.TypeObj, 100)}},
			"amd64.o: a Mach-O file of type Obj, not an executable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := universal(c.inputs)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	var names []string
	for _, in := range []input{amd64Exec(300), arm64Exec(200)} {
		name := filepath.Join(dir, in.name)
		if err := os.WriteFile(name, in.data, 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	out := filepath.Join(dir, "universal")
	// An existing file gets the executable's mode too.
	if err := os.WriteFile(out, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(out, names); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	// Windows keeps no executable bit, so only the size is checked there.
	if info.Size() != 0x4000+200 || runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, size %d; want -rwxr-xr-x, %d", info.Mode().Perm(), info.Size(), 0x4000+200)
	}
}

// TestMatchesLipo builds a small program for both architectures and
// compares the universal binary with the one lipo -create writes. It needs
// lipo, so it runs on macOS only.
func TestMatchesLipo(t *testing.T) {
	lipo, err := exec.LookPath("lipo")
	if err != nil {
		t.Skip("no lipo")
	}
	if testing.Short() {
		t.Skip("builds two executables")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	dir := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":  "module hello\n",
		"main.go": "package main\n\nfunc main() { println(\"hello\") }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	for _, goarch := range []string{"arm64", "amd64"} {
		name := filepath.Join(dir, "hello-"+goarch)
		cmd := exec.Command(goTool, "build", "-trimpath", "-o", name, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH="+goarch, "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build for %s: %v\n%s", goarch, err, out)
		}
		names = append(names, name)
	}
	want := filepath.Join(dir, "lipo")
	if out, err := exec.Command(lipo, append([]string{"-create", "-output", want}, names...)...).CombinedOutput(); err != nil {
		t.Fatalf("lipo: %v\n%s", err, out)
	}
	got := filepath.Join(dir, "universal")
	if err := run(got, names); err != nil {
		t.Fatal(err)
	}
	gotData, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	wantData, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotData, wantData) {
		t.Errorf("%d bytes differ from lipo's %d", len(gotData), len(wantData))
	}
}
