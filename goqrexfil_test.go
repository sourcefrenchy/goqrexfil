package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjk/smaz"
)

func TestPayloadInChunksRoundTrip(t *testing.T) {
	for _, size := range []int{1, 7, 320, 1000} {
		for _, n := range []int{1, 5, 320, 321, 1000, 1001, 4096} {
			payload := strings.Repeat("A", n)
			chunks := payloadInChunks(payload, size)
			joined := strings.Join(chunks, "")
			if joined != payload {
				t.Fatalf("size=%d n=%d: round trip mismatch (got %d bytes)", size, n, len(joined))
			}
			for i, c := range chunks {
				if len(c) == 0 {
					t.Fatalf("size=%d n=%d: empty chunk at %d", size, n, i)
				}
				if i < len(chunks)-1 && len(c) != size {
					t.Fatalf("size=%d n=%d: chunk %d len %d, want %d", size, n, i, len(c), size)
				}
			}
		}
	}
}

func TestPayloadInChunksNoDroppedBytes(t *testing.T) {
	// Regression: the old implementation dropped a byte per chunk boundary.
	payload := "0123456789"
	chunks := payloadInChunks(payload, 3)
	want := []string{"012", "345", "678", "9"}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks %v, want %d", len(chunks), chunks, len(want))
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestChunkFrameRoundTrip(t *testing.T) {
	for _, seq := range []int{0, 1, 42, 999999} {
		data := "c29tZS1kYXRhLWJ5dGVz"
		framed := formatChunk(seq, data)
		gotSeq, gotData, ok := parseChunk(framed)
		if !ok {
			t.Fatalf("seq=%d: parse failed on %q", seq, framed)
		}
		if gotSeq != seq || gotData != data {
			t.Fatalf("seq=%d: got (%d,%q)", seq, gotSeq, gotData)
		}
	}
}

func TestParseChunkRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "no-prefix", "GQ1:", "GQ1:abc:xyz", "GQ1:-1:x", "GQ1:000001"} {
		if _, _, ok := parseChunk(s); ok {
			t.Fatalf("parseChunk(%q) should fail", s)
		}
	}
}

func TestReassembleDetectsGaps(t *testing.T) {
	chunks := map[int]string{0: "a", 1: "b", 3: "d"} // missing seq 2
	res := reassemble(chunks)
	if res.complete {
		t.Fatal("expected incomplete result")
	}
	if res.received != 3 || res.total != 4 {
		t.Fatalf("got received=%d total=%d, want 3/4", res.received, res.total)
	}
	if got := formatMissing(res.missing); got != "3" {
		t.Fatalf("formatMissing = %q, want %q", got, "3")
	}
}

func TestReassembleComplete(t *testing.T) {
	original := []byte("hello world")
	encoded := base64.StdEncoding.EncodeToString(smaz.Encode(nil, original))
	chunks := make(map[int]string)
	for i := 0; i < len(encoded); i += 4 {
		end := i + 4
		if end > len(encoded) {
			end = len(encoded)
		}
		chunks[i/4] = encoded[i:end]
	}
	res := reassemble(chunks)
	if !res.complete {
		t.Fatal("expected complete result")
	}
	if !bytes.Equal(res.payload, original) {
		t.Fatalf("payload = %q, want %q", res.payload, original)
	}
}

func TestParseResume(t *testing.T) {
	set, err := parseResume("3,7,12", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 3 || !set[2] || !set[6] || !set[11] {
		t.Fatalf("bad set: %v", set)
	}
	if _, err := parseResume("0", 20); err == nil {
		t.Fatal("expected error for 0")
	}
	if _, err := parseResume("21", 20); err == nil {
		t.Fatal("expected error for out of range")
	}
	if _, err := parseResume("x", 20); err == nil {
		t.Fatal("expected error for non-numeric")
	}
	if set, _ := parseResume("", 20); set != nil {
		t.Fatal("expected nil for empty")
	}
}

func TestSelfTestRoundTrip(t *testing.T) {
	// Exercises encodeQR -> PNG -> DecodeQRCode -> parseChunk end to end.
	data := bytes.Repeat([]byte("hello exfil "), 500)
	selfTest(data)
}

// pseudoRandom returns deterministic incompressible data of n bytes.
func pseudoRandom(n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte(i*7 + i/251)
	}
	return buf
}

// buildVideo renders the framed chunks as QR PNGs and encodes them into an
// mp4 at 2fps, mimicking a phone recording of the client display. Each QR is
// held for 5 frames (like the 550ms client display), so the server gets
// multiple chances per chunk. Frames whose chunk index satisfies
// i%dropEvery == dropEvery-1 are omitted (missed chunk), and the remaining
// frames are renumbered so ffmpeg's image2 demuxer accepts them. Returns the
// video path and the dropped chunk seqs.
func buildVideo(t *testing.T, chunks []string, dropEvery int) (string, []int) {
	t.Helper()
	dir := t.TempDir()
	var dropped []int
	frame := 0
	for i, chunk := range chunks {
		if dropEvery > 0 && i%dropEvery == dropEvery-1 {
			dropped = append(dropped, i)
			continue
		}
		pngBytes := encodeQR(formatChunk(i, chunk))
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

// copyFile copies src to dst, replacing dst if it exists.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndVideo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	original := pseudoRandom(8000)
	encoded := base64.StdEncoding.EncodeToString(smaz.Encode(nil, original))
	chunks := payloadInChunks(encoded, QRCDataMaxBytes)

	video, _ := buildVideo(t, chunks, 0)
	if err := os.MkdirAll("./public", 0755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, video, videoLocation)

	res := retrievePayload()
	if !res.complete {
		t.Fatalf("expected complete payload, got %d/%d missing=%v", res.received, res.total, res.missing)
	}
	if !bytes.Equal(res.payload, original) {
		t.Fatalf("payload mismatch (got %d bytes, want %d)", len(res.payload), len(original))
	}
}

func TestEndToEndVideoWithGaps(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	original := pseudoRandom(8000)
	encoded := base64.StdEncoding.EncodeToString(smaz.Encode(nil, original))
	chunks := payloadInChunks(encoded, QRCDataMaxBytes)

	// Drop every 4th frame: those chunks must be reported as missing.
	video, dropped := buildVideo(t, chunks, 4)
	if err := os.MkdirAll("./public", 0755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, video, videoLocation)

	res := retrievePayload()
	if res.complete {
		t.Fatal("expected incomplete payload with dropped frames")
	}
	if res.total != len(chunks) {
		t.Fatalf("total = %d, want %d", res.total, len(chunks))
	}
	if len(res.missing) != len(dropped) {
		t.Fatalf("missing = %v, want %v", res.missing, dropped)
	}
	for i, m := range res.missing {
		if m != dropped[i] {
			t.Fatalf("missing[%d] = %d, want %d", i, m, dropped[i])
		}
	}
}
