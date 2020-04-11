package main

import (
	"bufio"
	"bytes"
	b64 "encoding/base64"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"os/exec"
	"runtime"

	qrcode "github.com/skip2/go-qrcode"
)

func generateqr(payload string) string {
	var pic []byte
	pic, _ = qrcode.Encode(payload, qrcode.Medium, 256)
	img, _, err := image.Decode(bytes.NewReader(pic))
	if err != nil {
		log.Fatalln(err)
	}

	out, _ := os.Create("./qr.png")

	if err := png.Encode(out, img); err != nil {
		out.Close()
		log.Fatal(err)
	}
	return "qr.png"
}

func encodepayload(payload string) string {
	fmt.Println("[*] encodepayload", payload)
	sEnc := b64.StdEncoding.EncodeToString([]byte(payload))
	fmt.Println(sEnc)
	return sEnc
}

func openbrowser(filename string) {
	var err error
	fmt.Println("[*] Displaying picture")
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
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			fmt.Println("[*] Client mode: ON")
			encoded := encodepayload(scanner.Text())
			png := generateqr(encoded)
			openbrowser(png)
		}

		if err := scanner.Err(); err != nil {
			log.Println(err)
		}
	} else {
		fmt.Println("Missing data, please send some data via stdin.")
		return
	}
}
