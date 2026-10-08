package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const manifestName = "goqrexfil-manifest.json"

// manifestEntry describes one file in a directory payload.
type manifestEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// manifest lists every file in a directory payload for verification.
type manifest struct {
	Files []manifestEntry `json:"files"`
}

// resolvePayloadSource reads the payload from stdin, a file, or a directory
// (which is packed into a tar.gz with a manifest). path "-" or "" means stdin.
func resolvePayloadSource(path string) ([]byte, string, error) {
	switch {
	case path == "" || path == "-":
		data, err := io.ReadAll(os.Stdin)
		return data, "stdin", err
	default:
		info, err := os.Stat(path)
		if err != nil {
			return nil, "", err
		}
		if info.IsDir() {
			data, err := packDirectory(path)
			return data, filepath.Base(path), err
		}
		data, err := os.ReadFile(path)
		return data, filepath.Base(path), err
	}
}

// packDirectory tars and gzips dir, embedding a manifest of file hashes.
func packDirectory(dir string) ([]byte, error) {
	var files []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var mf manifest
	for _, rel := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		mf.Files = append(mf.Files, manifestEntry{Path: rel, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	mfJSON, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return nil, err
	}

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for _, rel := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		hdr := &tar.Header{Name: rel, Mode: 0644, Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	mfHdr := &tar.Header{Name: manifestName, Mode: 0644, Size: int64(len(mfJSON))}
	if err := tw.WriteHeader(mfHdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(mfJSON); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(tarBuf.Bytes()); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return gzBuf.Bytes(), nil
}

// isTarGZ reports whether data is a gzip stream (our directory payloads).
func isTarGZ(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

// extractDirectoryPayload unpacks a tar.gz payload into dest, verifying each
// file against the embedded manifest. Returns the number of files written.
func extractDirectoryPayload(data []byte, dest string) (int, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	var mf *manifest
	var order []string
	files := map[string][]byte{}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return 0, err
		}
		if hdr.Name == manifestName {
			var m manifest
			if err := json.Unmarshal(content, &m); err != nil {
				return 0, fmt.Errorf("bad manifest: %w", err)
			}
			mf = &m
			continue
		}
		files[hdr.Name] = content
		order = append(order, hdr.Name)
	}

	if err := os.MkdirAll(dest, 0755); err != nil {
		return 0, err
	}
	count := 0
	for _, name := range order {
		content := files[name]
		if mf != nil {
			if err := verifyEntry(mf, name, content); err != nil {
				return count, err
			}
		}
		outPath := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			return count, err
		}
		if err := os.WriteFile(outPath, content, 0600); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func verifyEntry(mf *manifest, name string, content []byte) error {
	for _, e := range mf.Files {
		if e.Path == name {
			sum := sha256.Sum256(content)
			if hex.EncodeToString(sum[:]) != e.SHA256 {
				return fmt.Errorf("manifest hash mismatch for %s", name)
			}
			if int64(len(content)) != e.Size {
				return fmt.Errorf("manifest size mismatch for %s", name)
			}
			return nil
		}
	}
	return fmt.Errorf("file %s not in manifest", name)
}

// humanSize renders a byte count compactly.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
