// SPDX-License-Identifier: MIT

// Universal joins thin Mach-O executables for x86-64 and arm64 into one
// macOS universal binary, a fat file, as lipo -create does:
//
//	go run ./packaging/universal -o defy-installer defy-installer-darwin-amd64 defy-installer-darwin-arm64
//
// The release workflow runs it on Linux, which has no lipo. The fat file is
// the one lipo writes: a big-endian header that lists each executable's CPU
// type, offset, size and alignment, then the executables. Each starts at a
// multiple of its architecture's page size, 4 KiB for x86-64 and 16 KiB for
// arm64, with the smaller alignment first and zeros in between. The
// executables are copied byte for byte, so the ad-hoc signature that Go's
// linker gives a darwin/arm64 executable stays valid.
package main

import (
	"bytes"
	"cmp"
	"debug/macho"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
)

// alignments holds the log2 of each supported architecture's page size, as
// the fat header records it.
var alignments = map[macho.Cpu]uint32{
	macho.CpuAmd64: 12,
	macho.CpuArm64: 14,
}

// Sizes of the fat header and of each architecture's entry in it.
const (
	fatHeaderSize = 8
	fatArchSize   = 20
)

// An input is a thin executable and the name it was read from.
type input struct {
	name string
	data []byte
}

// An arch is one executable's place in the fat file.
type arch struct {
	input
	cpu    macho.Cpu
	subCpu uint32
	align  uint32
	offset uint32
}

func main() {
	out := flag.String("o", "", "the universal binary to write")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: universal -o output executable executable...")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *out == "" || flag.NArg() < 2 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*out, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "universal:", err)
		os.Exit(1)
	}
}

// run reads the executables and writes their universal binary to out.
func run(out string, names []string) error {
	inputs := make([]input, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		inputs = append(inputs, input{name, data})
	}
	fat, err := universal(inputs)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, fat, 0o755); err != nil {
		return err
	}
	// WriteFile keeps the mode of a file that is already there.
	return os.Chmod(out, 0o755)
}

// universal returns the fat file that holds the executables. It needs at
// least two, each a 64-bit executable for a different supported
// architecture.
func universal(inputs []input) ([]byte, error) {
	if len(inputs) < 2 {
		return nil, errors.New("a universal binary needs at least two executables")
	}
	archs := make([]arch, 0, len(inputs))
	for _, in := range inputs {
		f, err := macho.NewFile(bytes.NewReader(in.data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", in.name, err)
		}
		align, ok := alignments[f.Cpu]
		switch {
		case f.Magic != macho.Magic64:
			return nil, fmt.Errorf("%s: a 32-bit Mach-O file, not a 64-bit one", in.name)
		case !ok:
			return nil, fmt.Errorf("%s: a Mach-O file for %s, not amd64 or arm64", in.name, cpuName(f.Cpu))
		case f.Type != macho.TypeExec:
			return nil, fmt.Errorf("%s: a Mach-O file of type %v, not an executable", in.name, f.Type)
		}
		for _, a := range archs {
			if a.cpu == f.Cpu {
				return nil, fmt.Errorf("%s and %s are both for %s", a.name, in.name, cpuName(f.Cpu))
			}
		}
		archs = append(archs, arch{input: in, cpu: f.Cpu, subCpu: f.SubCpu, align: align})
	}
	// The smaller alignment goes first, as lipo does, which leaves less
	// padding. No two supported architectures share an alignment, so the
	// order of the inputs does not matter.
	slices.SortFunc(archs, func(a, b arch) int { return cmp.Compare(a.align, b.align) })

	end := uint64(fatHeaderSize + fatArchSize*len(archs))
	for i := range archs {
		pageSize := uint64(1) << archs[i].align
		end = (end + pageSize - 1) / pageSize * pageSize
		archs[i].offset = uint32(end)
		end += uint64(len(archs[i].data))
	}
	if end > math.MaxUint32 {
		return nil, errors.New("the executables are too large for a 32-bit fat header")
	}

	fat := make([]byte, 0, end)
	fat = binary.BigEndian.AppendUint32(fat, macho.MagicFat)
	fat = binary.BigEndian.AppendUint32(fat, uint32(len(archs)))
	for _, a := range archs {
		fat = binary.BigEndian.AppendUint32(fat, uint32(a.cpu))
		fat = binary.BigEndian.AppendUint32(fat, a.subCpu)
		fat = binary.BigEndian.AppendUint32(fat, a.offset)
		fat = binary.BigEndian.AppendUint32(fat, uint32(len(a.data)))
		fat = binary.BigEndian.AppendUint32(fat, a.align)
	}
	for _, a := range archs {
		fat = append(fat, make([]byte, int(a.offset)-len(fat))...)
		fat = append(fat, a.data...)
	}
	return fat, nil
}

// cpuName returns Go's name for a CPU type, such as arm64 for
// macho.CpuArm64.
func cpuName(cpu macho.Cpu) string {
	return strings.ToLower(strings.TrimPrefix(cpu.String(), "Cpu"))
}
