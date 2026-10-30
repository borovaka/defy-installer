// SPDX-License-Identifier: MIT

// Archive packs a release archive of the installer, a zip or a gzipped
// tar, that unpacks to a defy-installer folder:
//
//	go run ./packaging/archive -o dist/defy-installer-linux-x64.tar.gz -time 1761300000 \
//		-binary build/defy-installer-linux-amd64 -name defy-installer \
//		internal/settings/defy-installer.yaml packaging/README.txt LICENSE dist/THIRD_PARTY_LICENSES
//
// The archive holds the folder, then the binary under -name, then the
// other files under their own names, in the order given. The folder and
// the binary have mode 0755 and the other files 0644. Every entry has the
// time -time gives, in seconds since 1970 UTC, and no owner, so the same
// files give the same archive. A zip keeps the Unix modes, which macOS
// restores when it unpacks, so the binary stays executable.
//
// Go's archive packages give the same archive on every system, where the
// options of zip, tar, gzip, touch and install differ between GNU's tools,
// the Rust coreutils of newer Ubuntu runners, and macOS.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// folder is the folder the archive unpacks to.
const folder = "defy-installer"

// An entry is a file in the archive, under folder.
type entry struct {
	name string
	mode fs.FileMode
	data []byte
}

func main() {
	out := flag.String("o", "", "the archive to write, ending in .zip or .tar.gz")
	seconds := flag.Int64("time", -1, "the time of every entry, in seconds since 1970 UTC")
	binary := flag.String("binary", "", "the installer's binary")
	name := flag.String("name", "", "the binary's name in the archive")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: archive -o archive -time seconds -binary file -name name file...")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *out == "" || *seconds < 0 || *binary == "" || *name == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*out, time.Unix(*seconds, 0).UTC(), *binary, *name, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "archive:", err)
		os.Exit(1)
	}
}

// run reads the binary and the files and writes their archive to out.
func run(out string, modified time.Time, binary, name string, files []string) error {
	data, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	entries := []entry{{name, 0o755, data}}
	seen := map[string]bool{name: true}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		base := filepath.Base(file)
		if seen[base] {
			return fmt.Errorf("two files named %s", base)
		}
		seen[base] = true
		entries = append(entries, entry{base, 0o644, data})
	}
	var archive []byte
	switch {
	case strings.HasSuffix(out, ".zip"):
		archive, err = zipArchive(entries, modified)
	case strings.HasSuffix(out, ".tar.gz"):
		archive, err = tarArchive(entries, modified)
	default:
		return fmt.Errorf("%s: not a .zip or .tar.gz", out)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(out, archive, 0o644)
}

// zipArchive returns a zip of the entries, compressed at the highest
// level. Each entry records its Unix mode, which macOS's Archive Utility
// restores, and its time both in the MS-DOS fields, in UTC, and in the
// extended timestamp that Info-ZIP's tools read.
func zipArchive(entries []entry, modified time.Time) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})
	dir := &zip.FileHeader{Name: folder + "/", Method: zip.Store, Modified: modified}
	dir.SetMode(fs.ModeDir | 0o755)
	if _, err := zw.CreateHeader(dir); err != nil {
		return nil, err
	}
	for _, e := range entries {
		h := &zip.FileHeader{Name: folder + "/" + e.name, Method: zip.Deflate, Modified: modified}
		h.SetMode(e.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tarArchive returns a gzipped tar of the entries in the POSIX ustar
// format, with user and group 0 and no names for them. The gzip header
// holds no file name and no time.
func tarArchive(entries []entry, modified time.Time) ([]byte, error) {
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	dir := &tar.Header{Typeflag: tar.TypeDir, Name: folder + "/", Mode: 0o755, ModTime: modified, Format: tar.FormatUSTAR}
	if err := tw.WriteHeader(dir); err != nil {
		return nil, err
	}
	for _, e := range entries {
		h := &tar.Header{Typeflag: tar.TypeReg, Name: folder + "/" + e.name, Mode: int64(e.mode), Size: int64(len(e.data)),
			ModTime: modified, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
