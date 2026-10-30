// SPDX-License-Identifier: MIT

package release

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/borovaka/defy-installer/internal/firmware"
)

// Build is a ZMK build to install: the Neuron's image and the halves'
// packages, checked as a write checks them.
type Build struct {
	// Manifest is the build's manifest.json, or nil for a local build
	// without one, which is found by its file names and checked only as a
	// write checks images.
	Manifest *Manifest
	Neuron   *firmware.NeuronImage
	Left     *firmware.HalfImage
	Right    *firmware.HalfImage
}

// The file names scripts/build gives a build's images. A build without a
// manifest holds the Neuron's image and a pair of halves.
const (
	neuronName = "neuron-hybrid.hex"
	leftRadio  = "left-hybrid-radio.bin"
	rightRadio = "right-hybrid-radio.bin"
	leftPlain  = "left-hybrid.bin"
	rightPlain = "right-hybrid.bin"
)

// files are a build's files, in a release or on this computer.
type files struct {
	// read returns the named file if it is at most limit bytes, and a
	// *tooLargeError if it is larger. For a missing file, its error wraps
	// fs.ErrNotExist, or is a *NotFoundError for a release.
	read func(name string, limit int64) ([]byte, error)
	// label names a file in errors.
	label func(name string) string
}

// readBuild reads the images m lists, or without m the Neuron's, the left
// half's and the right's under names, and checks them.
func readBuild(m *Manifest, names [3]string, f files) (*Build, error) {
	var listed [3]*ManifestFile
	if m != nil {
		listed = m.files()
	}
	var data [3][]byte
	for i, l := range listed {
		var err error
		if l == nil {
			data[i], err = f.read(names[i], maxFileSize)
		} else {
			names[i] = l.Name
			data[i], err = readListed(f, l)
		}
		if err != nil {
			return nil, err
		}
	}
	b := &Build{Manifest: m}
	var err error
	if b.Neuron, err = firmware.ParseNeuronImage(f.label(names[0]), data[0]); err != nil {
		return nil, err
	}
	if b.Left, err = parseHalf(f.label(names[1]), data[1], firmware.Left, listed[1]); err != nil {
		return nil, err
	}
	if b.Right, err = parseHalf(f.label(names[2]), data[2], firmware.Right, listed[2]); err != nil {
		return nil, err
	}
	return b, nil
}

// parseHalf checks a package for the half on side, and, when the manifest
// lists it, that it has the listed program CRC and seal version.
func parseHalf(name string, data []byte, side firmware.Side, listed *ManifestFile) (*firmware.HalfImage, error) {
	h, err := firmware.ParseHalfImage(name, data)
	if err != nil {
		return nil, err
	}
	if err := h.RequireApplication(side); err != nil {
		return nil, err
	}
	if listed != nil && (h.ProgramCRC != listed.ProgramCRC32 || h.Version != listed.SealVersion) {
		return nil, fmt.Errorf("%s has program CRC 0x%08x and seal version %d, and the manifest says 0x%08x and %d",
			h.Name, h.ProgramCRC, h.Version, listed.ProgramCRC32, listed.SealVersion)
	}
	return h, nil
}

// readListed reads a file the manifest lists, and checks its size and
// SHA-256. A file that differs from the manifest gives a *mismatchError.
func readListed(f files, listed *ManifestFile) ([]byte, error) {
	data, err := f.read(listed.Name, listed.Size)
	if _, ok := errors.AsType[*tooLargeError](err); ok {
		return nil, &mismatchError{fmt.Sprintf("%s is larger than the %d bytes the manifest says",
			f.label(listed.Name), listed.Size)}
	} else if err != nil {
		return nil, err
	}
	if int64(len(data)) != listed.Size {
		return nil, &mismatchError{fmt.Sprintf("%s is %d bytes, and the manifest says %d",
			f.label(listed.Name), len(data), listed.Size)}
	}
	if sha256.Sum256(data) != listed.SHA256 {
		return nil, &mismatchError{fmt.Sprintf("%s does not match the manifest's SHA-256", f.label(listed.Name))}
	}
	return data, nil
}

// mismatchError is a file that differs from the manifest in size or
// SHA-256.
type mismatchError struct {
	text string
}

func (e *mismatchError) Error() string { return e.text }

// tooLargeError is a file larger than a read allows.
type tooLargeError struct {
	name  string
	limit int64
}

func (e *tooLargeError) Error() string {
	return fmt.Sprintf("%s is larger than %d bytes", e.name, e.limit)
}

// usualNames returns the images of a build without a manifest, among the
// names has finds: the Neuron's, and the -hybrid-radio pair when both are
// there, as scripts/build prefers it, otherwise the -hybrid pair.
func usualNames(where string, has func(name string) bool) ([3]string, error) {
	if !has(neuronName) {
		return [3]string{}, fmt.Errorf("%s has neither %s nor %s", where, manifestName, neuronName)
	}
	for _, pair := range [][2]string{{leftRadio, rightRadio}, {leftPlain, rightPlain}} {
		if has(pair[0]) && has(pair[1]) {
			return [3]string{neuronName, pair[0], pair[1]}, nil
		}
	}
	return [3]string{}, fmt.Errorf("%s has no %s, and neither %s and %s nor %s and %s",
		where, manifestName, leftRadio, rightRadio, leftPlain, rightPlain)
}
