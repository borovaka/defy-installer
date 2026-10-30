// SPDX-License-Identifier: MIT

package release

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ReadLocal reads the build at path, a zip, as GitHub serves a run's
// artifact, or a folder, and checks it: its images against its manifest,
// and each image as a write checks it. See Build.Manifest for a build
// without manifest.json.
func (c *Client) ReadLocal(path string) (*Build, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		root, err := os.OpenRoot(path)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		f, has := folderFiles(path, root)
		return c.readLocal(path, f, has)
	}
	zr, err := zip.OpenReader(path)
	if errors.Is(err, zip.ErrFormat) {
		return nil, fmt.Errorf("%s is neither a folder nor a zip", path)
	} else if err != nil {
		return nil, err
	}
	defer zr.Close()
	return c.readZip(path, &zr.Reader)
}

// readZip reads a build from zr, the zip at path.
func (c *Client) readZip(path string, zr *zip.Reader) (*Build, error) {
	f, has, err := zipFiles(path, zr)
	if err != nil {
		return nil, err
	}
	return c.readLocal(path, f, has)
}

// readLocal reads a build from the files of the zip or the folder at path:
// the images its manifest lists, or the usual names without one.
func (c *Client) readLocal(path string, f files, has func(name string) bool) (*Build, error) {
	if !has(manifestName) {
		names, err := usualNames(path, has)
		if err != nil {
			return nil, err
		}
		return readBuild(nil, names, f)
	}
	data, err := f.read(manifestName, maxManifestSize)
	if err != nil {
		return nil, err
	}
	m, err := parseManifest(f.label(manifestName), data)
	if err != nil {
		return nil, err
	}
	if err := m.checkInstaller(c.version()); err != nil {
		return nil, err
	}
	return readBuild(m, [3]string{}, f)
}

// folderFiles finds a build's files in the folder dir, opened as root. The
// root keeps every read inside the folder: a symbolic link that leads out
// of it, and on Windows a device name such as CON, fail.
func folderFiles(dir string, root *os.Root) (files, func(string) bool) {
	f := files{
		read: func(name string, limit int64) ([]byte, error) {
			return readFile(root, dir, name, limit)
		},
		label: func(name string) string { return filepath.Join(dir, name) },
	}
	// A file that the root refuses counts, so that reading it says why.
	has := func(name string) bool {
		info, err := root.Stat(name)
		return err == nil && info.Mode().IsRegular() || err != nil && !errors.Is(err, fs.ErrNotExist)
	}
	return f, has
}

// readFile reads the file name in root, the folder dir, if it is at most
// limit bytes.
func readFile(root *os.Root, dir, name string, limit int64) ([]byte, error) {
	file, err := root.Open(name)
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		// Name the file in full, as os.Open does.
		pe.Op, pe.Path = "open", filepath.Join(dir, name)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readAtMost(file, filepath.Join(dir, name), limit)
}

// zipFiles finds a build's files in a zip: at its root, as in the zip
// GitHub serves for a run's artifact, or else inside its one top-level
// folder, as when a folder is zipped. The folder macOS adds for its
// metadata, __MACOSX, and its .DS_Store files do not count.
func zipFiles(zipPath string, zr *zip.Reader) (files, func(string) bool, error) {
	var entries []*zip.File
	atRoot := false
	tops := map[string]bool{}
	for _, e := range zr.File {
		name := strings.ReplaceAll(e.Name, `\`, "/")
		if e.FileInfo().IsDir() || strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
			continue
		}
		entries = append(entries, e)
		top, _, nested := strings.Cut(name, "/")
		if nested {
			tops[top] = true
		} else {
			atRoot = true
		}
	}
	prefix := ""
	if !atRoot {
		if len(tops) != 1 {
			return files{}, nil, fmt.Errorf("%s holds no files at its top level, nor one folder that holds them", zipPath)
		}
		for top := range tops {
			prefix = top + "/"
		}
	}
	byName := map[string]*zip.File{}
	for _, e := range entries {
		name, ok := strings.CutPrefix(strings.ReplaceAll(e.Name, `\`, "/"), prefix)
		if !ok || strings.Contains(name, "/") {
			continue
		}
		if _, dup := byName[name]; dup {
			return files{}, nil, fmt.Errorf("%s holds %s twice", zipPath, prefix+name)
		}
		byName[name] = e
	}
	label := func(name string) string { return filepath.Join(zipPath, prefix+name) }
	f := files{
		read: func(name string, limit int64) ([]byte, error) {
			e, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("%s: %w", label(name), fs.ErrNotExist)
			}
			if e.UncompressedSize64 > uint64(limit) {
				return nil, &tooLargeError{label(name), limit}
			}
			r, err := e.Open()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", label(name), err)
			}
			defer r.Close()
			return readAtMost(r, label(name), limit)
		},
		label: label,
	}
	has := func(name string) bool {
		_, ok := byName[name]
		return ok
	}
	return f, has, nil
}

// readAtMost reads r if it holds at most limit bytes, and returns a
// *tooLargeError if it holds more.
func readAtMost(r io.Reader, name string, limit int64) ([]byte, error) {
	// One byte more than the limit, so that a larger file fails.
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, &tooLargeError{name, limit}
	}
	return data, nil
}
