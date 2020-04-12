package main

import (
	"bytes"
	b64 "encoding/base64"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

func generateqr(payload string, index int) string {
	// https://github.com/skip2/go-qrcode
	// The maximum capacity of a QR Code varies according to the content encoded
	// and the error recovery level. The maximum capacity is 2,953 bytes, 4,296 alphanumeric
	// characters, 7,089 numeric digits, or a combination of these.
	var pic []byte
	pic, _ = qrcode.Encode(payload, qrcode.Highest, 400)
	img, _, err := image.Decode(bytes.NewReader(pic))
	if err != nil {
		log.Fatalln(err)
	}

	index++ // Let start index=1
	var filename = strconv.Itoa(index) + ".jpg"
	out, _ := os.Create(filename)

	if err := jpeg.Encode(out, img, &jpeg.Options{Quality: 80}); err != nil {
		out.Close()
		log.Fatal(err)
	}
	return filename
}

func chunkit(longString string, chunkSize int) []string {
	slices := []string{}
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
	// for _, str := range slices {
	// 	fmt.Printf("(%s...) len: %d\n", str[0:5], len(str))
	// }
	return slices
}

func encodepayload(payload string) string {
	fmt.Println("[*] encodepayload")
	sEnc := b64.StdEncoding.EncodeToString([]byte(payload))
	return sEnc
}

func openbrowser(filename string) {
	var err error
	fmt.Println("[*] Displaying picture", filename)
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", filename).Start()
	case "windows":
		err = exec.Command("rundll32", "filename.dll,FileProtocolHandler", filename).Start()
	case "darwin":
		err = exec.Command("open", filename).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	fmt.Println("-= goqrexfil =-")
	isServer := flag.Bool("server", false, "server mode")
	flag.Parse()

	if *isServer {
		fmt.Println("[*] Server mode: ON")
		return
	}

	writeText, err := os.Open(os.DevNull)
	if err != nil {
		log.Fatalf("failed to open a null device: %s", err)
	}
	defer writeText.Close()
	io.WriteString(writeText, "Write Text")

	readText, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("failed to read stdin: %s", err)
	}

	if len(readText) == 0 {
		log.Fatalf("No data read from stdin")
	}

	fmt.Println("[*] Client mode: ON")
	encoded := encodepayload(bytes.NewBuffer(readText).String())
	chunks := chunkit(encoded, 250)
	fmt.Printf("[*] Payload is in %d chunks\n", len(chunks))
	for i, chunk := range chunks {
		openbrowser(generateqr(chunk, i))
		time.Sleep(1 * time.Second)
	}
}
