// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nfltr/send/xfer/verified"
)

// Source is what a sender serves: the manifest of its paths and where each
// file entry is on disk.
type Source struct {
	Manifest Manifest
	files    map[int]string        // entry index -> path on disk
	trees    map[int]verified.Tree // entry index -> its tree
}

// NewSource walks paths (each sent under its base name) into a Source.
// Only regular files and directories are sent; anything else, or two
// paths with one base name, is refused before anything is served.
func NewSource(paths []string) (*Source, error) {
	s := &Source{Manifest: Manifest{Version: Version}, files: map[int]string{}, trees: map[int]verified.Tree{}}
	seen := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(p)
		base := filepath.Base(p)
		if base == "." || base == ".." || base == string(filepath.Separator) {
			return nil, fmt.Errorf("%s: name a file or directory", p)
		}
		if seen[base] {
			return nil, fmt.Errorf("two sources named %q", base)
		}
		seen[base] = true
		parent := filepath.Dir(p)
		err := filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() && !d.IsDir() {
				return fmt.Errorf("%s: only files and directories are sent", q)
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(parent, q)
			if err != nil {
				return err
			}
			e := Entry{Path: filepath.ToSlash(rel), Dir: d.IsDir(), Mode: uint32(fi.Mode().Perm())}
			if !e.Dir {
				e.Size, e.MTime = fi.Size(), fi.ModTime().UnixNano()
				t, err := fileTree(q, e)
				if err != nil {
					return err
				}
				e.BLAKE3 = hex.EncodeToString(t.Root[:])
				s.files[len(s.Manifest.Entries)] = q
				s.trees[len(s.Manifest.Entries)] = t
			}
			s.Manifest.Entries = append(s.Manifest.Entries, e)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

// fileTree is the tree of the file at q, refused when it is not e's size
// and mtime after the read.
func fileTree(q string, e Entry) (verified.Tree, error) {
	f, err := os.Open(q) // #nosec G304 -- a file the user offered
	if err != nil {
		return verified.Tree{}, err
	}
	defer f.Close() //nolint:errcheck
	t, err := verified.New(f, e.Size)
	if err == nil {
		err = unchanged(f, e)
	}
	return t, err
}

// Files and Bytes count the source's file entries and their bytes.
func (s *Source) Files() (n int, bytes int64) {
	for _, e := range s.Manifest.Entries {
		if !e.Dir {
			n++
			bytes += e.Size
		}
	}
	return n, bytes
}

// Serve sends s over one session and returns the receiver's answer.
// sent counts the file bytes written to the session (a resume sends less).
func (s *Source) Serve(rw io.ReadWriter) (a Answer, sent int64, err error) {
	c := newConn(rw)
	if err := c.writeJSON(frameManifest, s.Manifest); err != nil {
		return a, 0, err
	}
	if err := c.flush(); err != nil {
		return a, 0, err
	}
	var w want
	t, p, err := c.read(nil)
	switch {
	case err != nil:
		return a, 0, noEOF(err)
	case t == frameAnswer: // refused before anything was sent, or a committed receive telling again (Retell)
		return a, 0, json.Unmarshal(p, &a)
	case t != frameWant:
		return a, 0, fmt.Errorf("%w: frame %q, want the receiver's want", ErrProtocol, t)
	}
	if err := json.Unmarshal(p, &w); err != nil {
		return a, 0, fmt.Errorf("%w: want: %v", ErrProtocol, err)
	}
	parts := map[int]Part{}
	for _, p := range w.Parts {
		parts[p.Index] = p
	}
	buf := make([]byte, 5+maxData) // a data frame: its header, then a chunk
	for i, e := range s.Manifest.Entries {
		if e.Dir {
			continue
		}
		n, err := s.sendFile(c, i, e, parts[i], buf)
		sent += n
		if err != nil {
			return a, sent, err
		}
	}
	if err := c.write(frameDone, nil); err != nil {
		return a, sent, err
	}
	if err := c.flush(); err != nil {
		return a, sent, err
	}
	err = c.readJSON(frameAnswer, &a)
	return a, sent, err
}

// sendFile sends entry i: its start, its tree, every chunk outside the
// part's runs when they are valid (the receiver verified them; spec Open),
// else every chunk, then its end. The receiver verifies each chunk
// against the manifest's root (spec Send, Bad).
func (s *Source) sendFile(c *conn, i int, e Entry, p Part, buf []byte) (int64, error) {
	f, err := os.Open(s.files[i])
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck
	if err := unchanged(f, e); err != nil {
		return 0, err
	}
	kept := len(p.Have) > 0 && validRuns(p.Have, chunks(e.Size))
	if !kept {
		p.Have = nil
	}
	if err := c.writeJSON(frameStart, start{Index: i, Kept: kept}); err != nil {
		return 0, err
	}
	for t := s.trees[i].Outboard; len(t) > 0; t = t[min(len(t), maxData):] {
		if err := c.write(frameTree, t[:min(len(t), maxData)]); err != nil {
			return 0, err
		}
	}
	// The data frames go out one write each, header and chunk read in
	// place, past the emptied buffer: no copy (docs/performance.md).
	if err := c.flush(); err != nil {
		return 0, err
	}
	var n int64
	for _, r := range need(p.Have, chunks(e.Size)) {
		off, end := r[0]*maxData, min(r[1]*maxData, e.Size)
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return n, err
		}
		for ; off < end; off += maxData {
			k, err := io.ReadFull(readFile(f), buf[5:5+min(maxData, end-off)])
			if err != nil {
				return n, fmt.Errorf("%s: %w (changed while being sent?)", e.Path, err)
			}
			if err := c.writeFrame(frameData, buf[:5+k]); err != nil {
				return n, err
			}
			n += int64(k)
		}
	}
	if err := unchanged(f, e); err != nil {
		return n, err
	}
	return n, c.writeJSON(frameEnd, end{Index: i})
}

// ErrChanged is a source file that changed since it was offered, or
// while it was sent.
var ErrChanged = errors.New("changed since it was offered: send it again")

// readFile reads a file being served (a test counts the bytes).
var readFile = func(f *os.File) io.Reader { return f }

// unchanged refuses a file whose size or mtime is no longer the manifest's.
func unchanged(f *os.File, e Entry) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != e.Size || fi.ModTime().UnixNano() != e.MTime {
		return fmt.Errorf("%s %w", e.Path, ErrChanged)
	}
	return nil
}
