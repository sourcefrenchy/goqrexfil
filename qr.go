package main

import (
	"bufio"
	b64 "encoding/base64"
	"flag"
	"fmt"
	"log"
	"os"
)

func encodepayload(payload string) {
	fmt.Println("[*] encodepayload", payload)
	sEnc := b64.StdEncoding.EncodeToString([]byte(payload))
	fmt.Println(sEnc)
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
			encodepayload(scanner.Text())
		}

		if err := scanner.Err(); err != nil {
			log.Println(err)
		}
	} else {
		fmt.Println("Missing data, please send some data via stdin.")
		return
	}
}
