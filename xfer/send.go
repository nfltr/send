// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Source is what a sender serves: the manifest of its paths and where each
// file entry is on disk.
type Source struct {
	Manifest Manifest
	files    map[int]string // entry index -> path on disk
}

// NewSource walks paths (each sent under its base name) into a Source.
// Only regular files and directories are sent; anything else, or two
// paths with one base name, is refused before anything is served.
func NewSource(paths []string) (*Source, error) {
	s := &Source{Manifest: Manifest{Version: Version}, files: map[int]string{}}
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
				s.files[len(s.Manifest.Entries)] = q
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
	case t == frameAnswer: // refused before anything was sent
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
	buf := make([]byte, maxData)
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

// sendFile sends entry i from the part's offset when the part is a prefix
// of the file (spec Open), else from 0, then the whole file's SHA-256.
func (s *Source) sendFile(c *conn, i int, e Entry, p Part, buf []byte) (int64, error) {
	f, err := os.Open(s.files[i])
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck
	if err := unchanged(f, e); err != nil {
		return 0, err
	}
	h := sha256.New()
	var off int64
	if p.Offset > 0 && p.Offset <= e.Size {
		got, err := prefixSum(io.TeeReader(io.LimitReader(f, p.Offset), h), p.Offset)
		if err != nil {
			return 0, err
		}
		if got == p.SHA256 {
			off = p.Offset
		} else { // not a prefix: from 0
			h.Reset()
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return 0, err
			}
		}
	}
	if err := c.writeJSON(frameStart, start{Index: i, Offset: off}); err != nil {
		return 0, err
	}
	var n int64
	for n < e.Size-off {
		k, err := io.ReadFull(f, buf[:min(int64(len(buf)), e.Size-off-n)])
		if err != nil {
			return n, fmt.Errorf("%s: %w (changed while being sent?)", e.Path, err)
		}
		h.Write(buf[:k])
		if err := c.write(frameData, buf[:k]); err != nil {
			return n, err
		}
		n += int64(k)
	}
	if err := unchanged(f, e); err != nil {
		return n, err
	}
	// Size and mtime miss a rewrite in place that restores them: the file
	// read again must be the bytes sent, else they are a mix (review
	// 2026-11-17, F2). ponytail: a second read of every file; a digest
	// in the manifest, hashed before serving, if that read costs too much.
	sum := hexSum(h)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return n, err
	}
	if again, err := prefixSum(f, e.Size); err != nil || again != sum {
		return n, errors.Join(fmt.Errorf("%s: its bytes %w", e.Path, ErrChanged), err)
	}
	return n, c.writeJSON(frameEnd, end{Index: i, SHA256: sum})
}

// ErrChanged is a source file that changed since it was offered, or
// while it was sent.
var ErrChanged = errors.New("changed since it was offered: send it again")

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
