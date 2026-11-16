// Package jsonl writes the seed's output files: JSON Lines (one record per
// line) and whole JSON documents, while hashing and counting what it writes
// so the manifest can describe each file without reading it back.
//
// Output must be byte-identical across runs, so encoding is fixed: struct
// field order as declared, map keys sorted (encoding/json's behaviour), no
// HTML escaping (so "<" and "&" in note text stay as written), and "\n" line
// endings.
package jsonl

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

// FileInfo describes one written file for the manifest.
type FileInfo struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Records int64  `json:"records"`
	SHA256  string `json:"sha256"`
}

// Writer streams records to one file.
type Writer struct {
	rel     string
	f       *os.File
	buf     *bufio.Writer
	sum     hash.Hash
	out     io.Writer
	bytes   int64
	records int64
	scratch bytes.Buffer
	enc     *json.Encoder
}

// Create opens root/rel for writing, creating parent directories.
func Create(root, rel string) (*Writer, error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &Writer{rel: rel, f: f, buf: bufio.NewWriterSize(f, 1<<20), sum: sha256.New()}
	w.out = io.MultiWriter(w.buf, w.sum)
	w.enc = json.NewEncoder(&w.scratch)
	w.enc.SetEscapeHTML(false)
	return w, nil
}

// Write appends one record as a single line.
func (w *Writer) Write(record any) error {
	w.scratch.Reset()
	if err := w.enc.Encode(record); err != nil { // Encode appends "\n"
		return fmt.Errorf("%s: record %d: %w", w.rel, w.records, err)
	}
	n, err := w.out.Write(w.scratch.Bytes())
	w.bytes += int64(n)
	w.records++
	return err
}

// Close flushes the file and returns its description.
func (w *Writer) Close() (FileInfo, error) {
	if err := w.buf.Flush(); err != nil {
		w.f.Close()
		return FileInfo{}, err
	}
	if err := w.f.Close(); err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Path: w.rel, Bytes: w.bytes, Records: w.records, SHA256: hex.EncodeToString(w.sum.Sum(nil))}, nil
}

// WriteDocument writes one indented JSON document (manifest, topic lists) and
// returns its description. Records is 1.
func WriteDocument(root, rel string, doc any) (FileInfo, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return FileInfo{}, fmt.Errorf("%s: %w", rel, err)
	}
	return WriteBytes(root, rel, b.Bytes(), 1)
}

// WriteBytes writes raw bytes and returns their description.
func WriteBytes(root, rel string, data []byte, records int64) (FileInfo, error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return FileInfo{}, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return FileInfo{}, err
	}
	sum := sha256.Sum256(data)
	return FileInfo{Path: rel, Bytes: int64(len(data)), Records: records, SHA256: hex.EncodeToString(sum[:])}, nil
}

// SHA256Hex returns the hex SHA-256 of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
