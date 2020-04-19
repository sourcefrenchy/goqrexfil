package main

import (
	b64 "encoding/base64"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/kjk/smaz"
	"github.com/mattn/go-colorable"
	"github.com/mdp/qrterminal"
)

var clear map[string]func() //create a map for storing clear funcs

func init() {
	clear = make(map[string]func()) //Initialize it
	clear["linux"] = func() {
		cmd := exec.Command("clear") //Linux example, its tested
		cmd.Stdout = os.Stdout
		cmd.Run()
	}
	clear["windows"] = func() {
		cmd := exec.Command("cmd", "/c", "cls") //Windows example, its tested
		cmd.Stdout = os.Stdout
		cmd.Run()
	}
	clear["darwin"] = func() {
		cmd := exec.Command("clear") //Linux example, its tested
		cmd.Stdout = os.Stdout
		cmd.Run()
	}
}

// CallClear exists to clear the command line
func CallClear() {
	value, ok := clear[runtime.GOOS] //runtime.GOOS -> linux, windows, darwin etc.
	if ok {                          //if we defined a clear func for that platform:
		value() //we execute it
	} else { //unsupported platform
		panic("Your platform is unsupported! I can't clear terminal screen :(")
	}
}

// RenderQR as a QR code thanks to Claudio Dangelis
// Copied from https://github.com/claudiodangelis/qrcp/blob/master/qr/qr.go
func RenderQR(chunk string) {
	qrConfig := qrterminal.Config{
		HalfBlocks:     true,
		Level:          qrterminal.L,
		Writer:         os.Stdout,
		BlackWhiteChar: "\u001b[37m\u001b[40m\u2584\u001b[0m",
		BlackChar:      "\u001b[30m\u001b[40m\u2588\u001b[0m",
		WhiteBlackChar: "\u001b[30m\u001b[47m\u2585\u001b[0m",
		WhiteChar:      "\u001b[37m\u001b[47m\u2588\u001b[0m",
	}
	if runtime.GOOS == "windows" {
		qrConfig.HalfBlocks = false
		qrConfig.Writer = colorable.NewColorableStdout()
		qrConfig.BlackChar = qrterminal.BLACK
		qrConfig.WhiteChar = qrterminal.WHITE
	}
	qrterminal.GenerateWithConfig(chunk, qrConfig)
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

func timeStr(sec int) (res string) {
	wks, sec := sec/604800, sec%604800
	ds, sec := sec/86400, sec%86400
	hrs, sec := sec/3600, sec%3600
	mins, sec := sec/60, sec%60
	CommaRequired := false
	if wks != 0 {
		res += fmt.Sprintf("%d wk", wks)
		CommaRequired = true
	}
	if ds != 0 {
		if CommaRequired {
			res += ", "
		}
		res += fmt.Sprintf("%d d", ds)
		CommaRequired = true
	}
	if hrs != 0 {
		if CommaRequired {
			res += ", "
		}
		res += fmt.Sprintf("%d hr", hrs)
		CommaRequired = true
	}
	if mins != 0 {
		if CommaRequired {
			res += ", "
		}
		res += fmt.Sprintf("%d min", mins)
		CommaRequired = true
	}
	if sec != 0 {
		if CommaRequired {
			res += ", "
		}
		res += fmt.Sprintf("%d sec", sec)
	}
	return
}

func main() {
	fmt.Println("-= goqrexfil =-")
	isServer := flag.Bool("server", false, "server mode")
	flag.Parse()

	// Server mode (retrieving data from video)
	if *isServer {
		fmt.Println("[*] Server mode: ON")
		return
	}

	// Client mode (allowing video recording of QR codes)
	fmt.Println("[*] Client mode: ON")
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

	// Compress, encode, chunk in pieces and display
	maxbytes := 500
	compressed := smaz.Encode(nil, readText)
	encoded := b64.StdEncoding.EncodeToString(compressed)
	fmt.Printf("%s", encoded)
	chunks := chunkit(encoded, maxbytes)
	fmt.Printf("[*] Payload is in %d chunks, video recording time estimate: %s\n", len(chunks), timeStr(int(float64(len(chunks))*0.1)))
	fmt.Println("\n\n---=== 5 seconds to use CTRL+C if you want to abort ===---")
	time.Sleep(5 * time.Second)

	for _, chunk := range chunks {
		// log.Println("[D] Generating qr #", i+1)
		time.Sleep(100 * time.Millisecond)
		CallClear()
		RenderQR(chunk)
	}

}
