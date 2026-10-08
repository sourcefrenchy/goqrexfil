package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/gin-gonic/gin"
	"github.com/kjk/smaz"
	goqr "github.com/liyue201/goqr"
	log "github.com/sirupsen/logrus"
	"github.com/zserge/lorca"
	"golang.org/x/crypto/blake2b"
)

const (
	serverPort        = "9999"                    // TCP port to run the web service on
	videoLocation     = "./public/video.mp4"      // location of video uploaded to the web service
	ffmpegQuality     = "16"                      // Quality for frames to images conversion. 1-31. 5 for great, 10 for acceptable (this helps reduce file size)
	ffmpegImageScale  = "scale='min(iw,1600)':-1" // Cap very large (4K) frames at 1600px wide; never downscale normal frames, since shrinking the QR below ~600px breaks recognition
	QRCDataMaxBytes   = 320                       // 230 was safe. If this gets too big, QR code will be hard to read...
	secsBeforeDisplay = 3                         // 3 seconds before starting to display QR codes
	msBetweenFrames   = 550                       // milliseconds between QR codes displayed to allow proper recording
	retrieved         = "./payload/payload.bin"   // path to output payload file when video and all qr code data is retrieved

	chunkPrefix   = "GQ1:"    // prefix identifying a framed chunk: GQ1:<seq>:<data>
	chunkSeqWidth = 6         // zero-padded width of the sequence number
	maxUploadSize = 512 << 20 // 512 MB upload cap
)

// ffmpegPath resolves the ffmpeg binary, preferring PATH and falling back to
// common install locations (Homebrew on Apple Silicon/Intel, MacPorts).
func ffmpegPath() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	for _, p := range []string{
		"/opt/homebrew/bin/ffmpeg",
		"/usr/local/bin/ffmpeg",
		"/opt/local/bin/ffmpeg",
		"/usr/bin/ffmpeg",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "ffmpeg"
}

// encodeQR renders chunk as a PNG QR code image.
func encodeQR(chunk string) []byte {
	qrCode, err := qr.Encode(chunk, qr.H, qr.Unicode)
	if err != nil {
		log.Fatal(err)
	}
	qrCode, err = barcode.Scale(qrCode, 600, 600)
	if err != nil {
		log.Fatal(err)
	}
	var buff bytes.Buffer
	if err := png.Encode(&buff, qrCode); err != nil {
		log.Fatal(err)
	}
	return buff.Bytes()
}

// RenderQR returns a QR code HTML image with encoded chunk as data
func RenderQR(chunk string) string {
	encodedString := base64.StdEncoding.EncodeToString(encodeQR(chunk))
	return "<img src=\"data:image/png;base64," + encodedString + "\" />"
}

// payloadInChunks cuts a string payload into chunks of at most chunkSize bytes.
func payloadInChunks(longString string, chunkSize int) []string {
	if chunkSize <= 0 {
		log.Fatalf("chunkSize must be positive, got %d", chunkSize)
	}
	var slices []string
	for i := 0; i < len(longString); i += chunkSize {
		end := i + chunkSize
		if end > len(longString) {
			end = len(longString)
		}
		slices = append(slices, longString[i:end])
	}
	return slices
}

// formatChunk wraps a payload chunk with a sequence number so the server can
// reassemble out-of-order frames and detect missing chunks.
func formatChunk(seq int, data string) string {
	return fmt.Sprintf("%s%0*d:%s", chunkPrefix, chunkSeqWidth, seq, data)
}

// parseChunk is the inverse of formatChunk.
func parseChunk(s string) (seq int, data string, ok bool) {
	if !strings.HasPrefix(s, chunkPrefix) {
		return 0, "", false
	}
	rest := s[len(chunkPrefix):]
	i := strings.IndexByte(rest, ':')
	if i <= 0 {
		return 0, "", false
	}
	seq, err := strconv.Atoi(rest[:i])
	if err != nil || seq < 0 {
		return 0, "", false
	}
	return seq, rest[i+1:], true
}

// formatMissing renders 0-based missing seqs as the 1-based comma separated
// list the client --resume flag expects.
func formatMissing(missing []int) string {
	parts := make([]string, len(missing))
	for i, m := range missing {
		parts[i] = strconv.Itoa(m + 1)
	}
	return strings.Join(parts, ",")
}

// DecodeQRCode returns the payload string found in img, or "" if none.
func DecodeQRCode(img image.Image) string {
	b := img.Bounds()
	rgba := image.NewRGBA(b)
	draw.Draw(rgba, b, img, b.Min, draw.Src)
	qrCodes, err := goqr.Recognize(rgba)
	if err != nil {
		return ""
	}
	var payload string
	for _, qrCode := range qrCodes {
		payload = payload + string(qrCode.Payload)
	}
	return payload
}

type retrievalResult struct {
	complete bool
	received int
	total    int
	missing  []int
	payload  []byte
}

// reassembles the payload from the framed chunks collected from the video.
func reassemble(chunks map[int]string) *retrievalResult {
	total := 0
	for seq := range chunks {
		if seq >= total {
			total = seq + 1
		}
	}
	var missing []int
	for seq := 0; seq < total; seq++ {
		if _, ok := chunks[seq]; !ok {
			missing = append(missing, seq)
		}
	}
	fmt.Printf("[*] Received %d/%d chunks", len(chunks), total)
	if len(missing) > 0 {
		fmt.Println(" - MISSING:", formatMissing(missing))
		return &retrievalResult{received: len(chunks), total: total, missing: missing}
	}
	fmt.Println()

	var buf strings.Builder
	for seq := 0; seq < total; seq++ {
		buf.WriteString(chunks[seq])
	}
	decoded, err := base64.StdEncoding.DecodeString(buf.String())
	if err != nil {
		log.Fatalf("base64 decode failed: %v", err)
	}
	decompressed, err := smaz.Decode(nil, decoded)
	if err != nil {
		log.Fatalf("smaz decode failed: %v", err)
	}
	writePayloadFile(decompressed, retrieved)
	h := blake2b.Sum256(decompressed) // content
	fmt.Println("[*] Payload saved as ", retrieved, "\nPayload hash", hex.EncodeToString(h[:]))
	return &retrievalResult{complete: true, received: total, total: total, payload: decompressed}
}

/*
	retrievePayload is the main function that will take the uploaded video,

will extract frames and will call DecodeQRCode() to get the payload.
it will also concatenate all pieces and finally return the full payload.
*/
func retrievePayload() *retrievalResult {
	// Split video into frames using ffmpeg. Ideally it should be a module and not an exec.command call
	files, err := filepath.Glob("./public/*png")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("[***] Cleaning old files and extracting video frames")
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			log.Fatal(err)
		}
	}

	cmd := exec.Command(ffmpegPath(), "-i", videoLocation,
		"-loglevel", "error",
		"-qscale:v", ffmpegQuality,
		"-vf", ffmpegImageScale,
		"-vsync", "vfr",
		"./public/%03d.png")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}

	// Now we need to parse all frames, find if a QR Code is present and extract data from it
	chunks := make(map[int]string)
	matches, _ := filepath.Glob("./public/*png")
	fmt.Println("[***] Extracting data from", len(matches), "frames, skipping duplicates")
	for _, match := range matches {
		f, err := os.Open(match)
		if err != nil {
			continue
		}
		img, _, err := image.Decode(f)
		_ = f.Close()
		if err != nil {
			continue
		}
		raw := DecodeQRCode(img)
		if raw == "" {
			continue
		}
		seq, data, ok := parseChunk(raw)
		if !ok {
			fmt.Println("[!] Skipping QR code in", match, "with unrecognized format")
			continue
		}
		if _, exists := chunks[seq]; !exists {
			chunks[seq] = data
			fmt.Println("[*] Retrieving chunk", seq+1, "from", match)
		}
	}

	if len(chunks) == 0 {
		log.Info("!!! No Payload retrieved from analyzed frames")
		return &retrievalResult{}
	}
	return reassemble(chunks)
}

var lastResult *retrievalResult

func webService() {
	gin.SetMode("release")
	router := gin.New()
	router.MaxMultipartMemory = maxUploadSize
	router.Static("/process", "./public")
	router.GET("/payload", func(c *gin.Context) {
		if _, err := os.Stat(retrieved); err != nil {
			c.String(http.StatusNotFound, "no payload available yet")
			return
		}
		// These headers are needed by some browsers (without them Chrome downloads files as txt)
		c.Header("Content-Disposition", "attachment; filename="+filepath.Base(retrieved))
		c.Header("Content-Type", "application/octet-stream")
		c.File(retrieved)
	})
	// /missing lists the 1-based chunk numbers to re-record, formatted for --resume
	router.GET("/missing", func(c *gin.Context) {
		if lastResult == nil || len(lastResult.missing) == 0 {
			c.String(http.StatusNotFound, "no missing chunks")
			return
		}
		c.String(http.StatusOK, formatMissing(lastResult.missing))
	})
	// upload will get a file and save it in ./public
	// test: curl -F 'file=@./1.jpg' http://localhost:9999/upload
	router.POST("/upload", func(c *gin.Context) {
		file, err := c.FormFile("file")
		if err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("get form err: %s", err.Error()))
			return
		}

		if err := c.SaveUploadedFile(file, videoLocation); err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("upload file err: %s", err.Error()))
			return
		}
		log.Println("\n[*] File received")

		// processing
		result := retrievePayload()
		lastResult = result
		var myLink string
		switch {
		case result.complete:
			myLink = "<b>Payload retrieved.</b> <a href='/payload'>download payload</a>"
		case result.received > 0:
			missing := formatMissing(result.missing)
			myLink = fmt.Sprintf("<b>Partial payload: %d/%d chunks.</b> Missing: <code>%s</code><br/>Re-record those chunks with <code>--client --resume %s</code> and upload again.",
				result.received, result.total, missing, missing)
		default:
			myLink = "<b>No payload retrieved.</b>"
		}
		if _, err = c.Writer.Write([]byte(myLink)); err != nil {
			log.Fatal("Cannot write response")
		}
	})
	log.Info("Serving on port ", serverPort)
	router.Run(":" + serverPort)
}

func writePayloadFile(payload []byte, filename string) {
	err := os.Remove(filename)
	if err != nil {
		fmt.Println("\n[I] No previous payload file found")
	} else {
		fmt.Println("\n[I] Deleted previous payload file")
	}
	// Open a new file for writing only
	file, err := os.OpenFile(
		filename,
		os.O_WRONLY|os.O_TRUNC|os.O_CREATE,
		0600,
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(file)

	// Write bytes to file
	_, err = file.Write(payload)
	if err != nil {
		log.Fatal(err)
	}
}

// selfTest encodes the payload into QR PNGs on disk, decodes them back and
// verifies the reassembled payload matches the original. No camera needed.
func selfTest(data []byte) {
	fmt.Println("[***] Self-test mode: local QR round-trip, no camera needed")
	compressed := smaz.Encode(nil, data)
	encoded := base64.StdEncoding.EncodeToString(compressed)
	chunks := payloadInChunks(encoded, QRCDataMaxBytes)
	fmt.Println("[*] Payload will be in", len(chunks), "chunks")

	dir, err := os.MkdirTemp("", "goqrexfil-selftest-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	for i, chunk := range chunks {
		pngBytes := encodeQR(formatChunk(i, chunk))
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.png", i)), pngBytes, 0644); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Println("[*] Wrote", len(chunks), "QR PNGs to", dir)

	chunkMap := make(map[int]string)
	matches, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	for _, match := range matches {
		f, err := os.Open(match)
		if err != nil {
			log.Fatal(err)
		}
		img, _, err := image.Decode(f)
		_ = f.Close()
		if err != nil {
			log.Fatal(err)
		}
		seq, data, ok := parseChunk(DecodeQRCode(img))
		if !ok {
			log.Fatalf("FAIL: could not decode QR in %s", match)
		}
		chunkMap[seq] = data
	}

	total := len(chunks)
	if len(chunkMap) != total {
		log.Fatalf("FAIL: decoded %d/%d chunks", len(chunkMap), total)
	}
	var buf strings.Builder
	for seq := 0; seq < total; seq++ {
		buf.WriteString(chunkMap[seq])
	}
	decoded, err := base64.StdEncoding.DecodeString(buf.String())
	if err != nil {
		log.Fatalf("FAIL: base64 decode: %v", err)
	}
	out, err := smaz.Decode(nil, decoded)
	if err != nil {
		log.Fatalf("FAIL: smaz decode: %v", err)
	}
	if !bytes.Equal(out, data) {
		log.Fatalf("FAIL: payload mismatch (got %d bytes, want %d)", len(out), len(data))
	}
	h := blake2b.Sum256(out)
	fmt.Println("[*] PASS: round-trip OK, payload hash", hex.EncodeToString(h[:]))
}

// parseResume parses a comma separated list of 1-based chunk numbers into a
// set of 0-based seqs. Returns nil if s is empty.
func parseResume(s string, total int) (map[int]bool, error) {
	if s == "" {
		return nil, nil
	}
	set := make(map[int]bool)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > total {
			return nil, fmt.Errorf("invalid chunk number %q (must be 1-%d)", part, total)
		}
		set[n-1] = true
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("no valid chunk numbers in %q", s)
	}
	return set, nil
}

func main() {
	log.SetFormatter(&log.TextFormatter{})
	isServer := flag.Bool("server", false, "Server mode")
	isClient := flag.Bool("client", false, "Client mode")
	isSelfTest := flag.Bool("selftest", false, "Self-test: encode and decode QRs locally, no camera needed")
	isProcessing := flag.Bool("retrievePayload", false, "Processing existing video only (debug mode)")
	isResume := flag.String("resume", "", "Client: only display these 1-based chunk numbers (comma separated), for re-recording missing chunks")
	flag.Parse()

	if *isSelfTest {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.Fatalf("failed to read stdin: %s", err)
		}
		if len(data) == 0 {
			log.Fatalf("No data read from stdin")
		}
		selfTest(data)
	} else if *isProcessing {
		log.Println("Processing only - DEBUG MODE")
		_ = retrievePayload()
	} else if *isServer {
		// webService mode (retrieving data from video)
		fmt.Println("[*] Server mode: ON")
		webService()
	} else if *isClient {
		// Client mode (allowing video recording of QR codes)
		fmt.Println("[***] Client mode: ON")
		fmt.Println("[*] Loading payload from stdin")
		readText, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.Fatalf("failed to read stdin: %s", err)
		}
		if len(readText) == 0 {
			log.Fatalf("No data read from stdin")
		}
		h := blake2b.Sum256(readText)
		fmt.Println("Plaintext hash", hex.EncodeToString(h[:]))

		// Compress, encode, payload in chunks then display the QrCodes
		compressed := smaz.Encode(nil, readText)
		encoded := base64.StdEncoding.EncodeToString(compressed)
		chunks := payloadInChunks(encoded, QRCDataMaxBytes)
		fmt.Println("\n[*] Payload will be in", len(chunks), "chunks")

		resume, err := parseResume(*isResume, len(chunks))
		if err != nil {
			log.Fatal(err)
		}
		if resume != nil {
			fmt.Println("[*] Resume mode: displaying", len(resume), "chunk(s):", *isResume)
		}

		fmt.Println("[***] Start your video, displaying in >", secsBeforeDisplay, "< seconds ****")
		fmt.Println()
		time.Sleep(secsBeforeDisplay * time.Second)

		// Create UI with basic HTML passed via data URI
		ui, err := lorca.New("data:text/html,"+url.PathEscape(`<html><body><h1>Starting...</h1></body></html>`), "", 675, 675)
		if err != nil {
			log.Fatal("lorca.New():", err)
		}
		defer ui.Close()

		// Iterate on chunks, generate QR code and display it in UI
		shown := 0
		for i, chunk := range chunks {
			if resume != nil && !resume[i] {
				continue
			}
			time.Sleep(msBetweenFrames * time.Millisecond) // need some delays to allow video recording and avoid losing a frame
			ui.Load("data:text/html," + url.PathEscape(`<html><body><center>`+RenderQR(formatChunk(i, chunk))+`</center></body></html>`))
			shown++
		}
		time.Sleep(msBetweenFrames * time.Millisecond)
		ui.Load("data:text/html," + url.PathEscape(fmt.Sprintf(`<html><body><h1>Done (%d chunks)</h1></body></html>`, shown)))
		<-ui.Done()
	} else {
		fmt.Println("Please use client or server mode:")
		fmt.Println("echo \"data to send\" | ./goqrexfil --client\t\tTo use in client mode")
		fmt.Println("echo \"data to send\" | ./goqrexfil --client --resume 3,7,12\tOnly display specific chunks (re-record missing ones)")
		fmt.Println("echo \"data to send\" | ./goqrexfil --selftest\t\tLocal round-trip test, no camera needed")
		fmt.Println("./goqrexfil --server\t\t\t\t\tTo use as a web server to receive video and retrieve payload.")
		fmt.Println()
		os.Exit(1)
	}
}
