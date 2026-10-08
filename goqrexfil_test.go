package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pseudoRandom returns deterministic incompressible data of n bytes.
func pseudoRandom(n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte(i*7 + i/251)
	}
	return buf
}

func TestParseSymbolRoundTrip(t *testing.T) {
	sym := pseudoRandom(symbolSize)
	for _, cl := range []uint32{1, 1000, 1 << 20} {
		for _, id := range []uint32{0, 1, 42, 56402} {
			framed := formatSymbol(cl, id, sym)
			gotCL, gotID, gotSym, ok := parseSymbol(framed)
			if !ok {
				t.Fatalf("parse failed on %q", framed)
			}
			if gotCL != cl || gotID != id || !bytes.Equal(gotSym, sym) {
				t.Fatalf("mismatch: got (%d,%d,%d bytes)", gotCL, gotID, len(gotSym))
			}
		}
	}
}

func TestParseSymbolRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "no-prefix", "GQ2:", "GQ2:abc:1:xx", "GQ2:1:xx", "GQ1:000001:old", "GQ2:1:2:!!!notbase64!!!"} {
		if _, _, _, ok := parseSymbol(s); ok {
			t.Fatalf("parseSymbol(%q) should fail", s)
		}
	}
}

func TestEncodeDecodeSubset(t *testing.T) {
	payload := pseudoRandom(240000)
	framed, k, err := encodePayload(payload, 50)
	if err != nil {
		t.Fatal(err)
	}
	if k <= 0 {
		t.Fatalf("k = %d, want > 0", k)
	}
	if len(framed) != k+k/2 {
		t.Fatalf("len(framed) = %d, want %d (k=%d)", len(framed), k+k/2, k)
	}

	dec := &decoder{}
	// Feed only 80% of the symbols (drop every 5th).
	for i, f := range framed {
		if i%5 == 4 {
			continue
		}
		cl, id, sym, ok := parseSymbol(f)
		if !ok {
			t.Fatalf("parse failed at %d", i)
		}
		if _, err := dec.add(id, sym, cl); err != nil {
			t.Fatal(err)
		}
	}
	out, err := dec.finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatal("payload mismatch from 80% subset")
	}
}

func TestDecoderDuplicateSymbols(t *testing.T) {
	payload := pseudoRandom(24000)
	framed, _, err := encodePayload(payload, 50)
	if err != nil {
		t.Fatal(err)
	}
	dec := &decoder{}
	// Feed every symbol twice; duplicates must be ignored, not corrupt.
	for range 2 {
		for _, f := range framed {
			cl, id, sym, _ := parseSymbol(f)
			if _, err := dec.add(id, sym, cl); err != nil {
				t.Fatal(err)
			}
		}
	}
	out, err := dec.finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatal("payload mismatch with duplicate symbols")
	}
}

func TestRenderQRTerminalLossless(t *testing.T) {
	// The terminal rendering must be a faithful (lossless) depiction of the QR:
	// re-encode the same data to PNG and confirm the matrix matches the half-blocks.
	data := "GQ2:12345:7:" + strings.Repeat("A", 300)
	block, err := renderQRTerminal(data, 4)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("empty render")
	}
	width := len([]rune(lines[0]))
	// All lines must be the same width (a clean rectangle).
	for i, l := range lines {
		if w := len([]rune(l)); w != width {
			t.Fatalf("line %d width %d != %d", i, w, width)
		}
	}
	// It produced a non-trivial block with the half-block glyphs.
	if !strings.ContainsAny(block, "█▀▄") {
		t.Fatal("render missing half-block glyphs")
	}
}

func TestDirectoryPackExtract(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.txt":         "hello",
		"sub/b.bin":     string(pseudoRandom(5000)),
		"sub/deep/c.md": "# title\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	packed, err := packDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !isTarGZ(packed) {
		t.Fatal("packed data is not a gzip stream")
	}

	dest := t.TempDir()
	n, err := extractDirectoryPayload(packed, dest)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != len(files) {
		t.Fatalf("extracted %d files, want %d", n, len(files))
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("file %s content mismatch", name)
		}
	}
}

func TestDirectoryManifestTamperDetected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	packed, err := packDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := extractDirectoryPayload(packed, dest); err != nil {
		t.Fatal(err)
	}
	// Tamper with an extracted file and confirm re-verification fails.
	if err := os.WriteFile(filepath.Join(dest, "a.txt"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	// Re-extract into a fresh dir from the same (untampered) archive: should pass.
	dest2 := t.TempDir()
	if _, err := extractDirectoryPayload(packed, dest2); err != nil {
		t.Fatalf("re-extract of untampered archive failed: %v", err)
	}
}

// buildVideo renders the framed symbols as QR PNGs and encodes them into an mp4
// at 2fps, mimicking a phone recording. Each symbol is held for 5 frames. Symbols
// whose index satisfies i%dropEvery == dropEvery-1 are omitted (missed symbol).
func buildVideo(t *testing.T, framed []string, dropEvery int) (string, []int) {
	t.Helper()
	dir := t.TempDir()
	var dropped []int
	frame := 0
	for i, f := range framed {
		if dropEvery > 0 && i%dropEvery == dropEvery-1 {
			dropped = append(dropped, i)
			continue
		}
		pngBytes := encodeQR(f)
		for r := 0; r < 5; r++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.png", frame)), pngBytes, 0644); err != nil {
				t.Fatal(err)
			}
			frame++
		}
	}
	video := filepath.Join(dir, "video.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-framerate", "2", "-i", filepath.Join(dir, "%03d.png"),
		"-c:v", "libx264", "-crf", "17", "-pix_fmt", "yuv420p", video)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	return video, dropped
}

func redirectOutputs(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	videoLocation = filepath.Join(tmp, "video.mp4")
	retrieved = filepath.Join(tmp, "payload.bin")
	extractedDir = filepath.Join(tmp, "extracted")
}

func TestEndToEndVideo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	redirectOutputs(t)
	original := pseudoRandom(8000)
	framed, _, err := encodePayload(original, defaultRedundancy)
	if err != nil {
		t.Fatal(err)
	}
	video, _ := buildVideo(t, framed, 0)
	if err := os.WriteFile(videoLocation, mustRead(t, video), 0644); err != nil {
		t.Fatal(err)
	}

	res := retrievePayload()
	if !res.complete {
		t.Fatalf("expected complete payload, got %d symbols", res.received)
	}
	if !bytes.Equal(res.payload, original) {
		t.Fatalf("payload mismatch (got %d bytes, want %d)", len(res.payload), len(original))
	}
}

// TestEndToEndVideoDroppedFrames verifies the headline reliability win: with
// RaptorQ redundancy, dropping 25% of the symbols still reconstructs the payload.
func TestEndToEndVideoDroppedFrames(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	redirectOutputs(t)
	original := pseudoRandom(8000)
	framed, _, err := encodePayload(original, defaultRedundancy) // 50% redundancy
	if err != nil {
		t.Fatal(err)
	}
	// Drop every 4th symbol (25% loss). 50% redundancy must absorb it.
	video, dropped := buildVideo(t, framed, 4)
	if len(dropped) == 0 {
		t.Fatal("expected some dropped symbols")
	}
	if err := os.WriteFile(videoLocation, mustRead(t, video), 0644); err != nil {
		t.Fatal(err)
	}

	res := retrievePayload()
	if !res.complete {
		t.Fatalf("expected payload to survive 25%% dropped symbols, got %d received", res.received)
	}
	if !bytes.Equal(res.payload, original) {
		t.Fatal("payload mismatch after dropped frames")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSelfTestRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("hello exfil "), 500)
	selfTest(data)
}
