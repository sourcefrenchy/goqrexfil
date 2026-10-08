package main

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"os"
	"strings"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	goqr "github.com/liyue201/goqr"
	log "github.com/sirupsen/logrus"
)

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

// encodeQR renders data as a PNG QR code image (error correction H). Used by the
// self-test and the end-to-end video tests.
func encodeQR(data string) []byte {
	qrCode, err := qr.Encode(data, qr.H, qr.Unicode)
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

// isDark reports whether module (x,y) of a native-size QR image is dark.
func isDark(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	luma := (r*299 + g*587 + b*114) / 1000
	return luma < 0x8000
}

// renderQRTerminal renders data as a QR code using Unicode half-block characters,
// pairing two module rows into one text line. quietZone is the light border in
// modules (4 = QR spec). No scaling is applied, so modules stay crisp.
func renderQRTerminal(data string, quietZone int) (string, error) {
	code, err := qr.Encode(data, qr.H, qr.Unicode)
	if err != nil {
		return "", err
	}
	n := code.Bounds().Max.X // native module count (always odd)

	// Build the full matrix including the quiet zone (light border).
	N := n + 2*quietZone
	m := make([][]bool, N)
	for y := 0; y < N; y++ {
		m[y] = make([]bool, N)
		for x := 0; x < N; x++ {
			if x >= quietZone && x < quietZone+n && y >= quietZone && y < quietZone+n {
				m[y][x] = isDark(code, x-quietZone, y-quietZone)
			}
		}
	}

	var sb strings.Builder
	for y := 0; y < N; y += 2 {
		for x := 0; x < N; x++ {
			top := m[y][x]
			bot := y+1 < N && m[y+1][x]
			switch {
			case top && bot:
				sb.WriteRune('█') // U+2588 full block
			case top:
				sb.WriteRune('▀') // U+2580 upper half block
			case bot:
				sb.WriteRune('▄') // U+2584 lower half block
			default:
				sb.WriteRune(' ')
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// displayQRTerminal clears the screen and renders data as a QR code, in a single
// write so the frame is atomic. caption (if non-empty) is shown above the code.
func displayQRTerminal(data, caption string, quietZone int) error {
	block, err := renderQRTerminal(data, quietZone)
	if err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("\x1b[2J\x1b[H") // clear screen + home
	if caption != "" {
		sb.WriteString(caption)
		sb.WriteString("\n\n")
	}
	sb.WriteString(block)
	_, err = os.Stdout.WriteString(sb.String())
	return err
}

// qrTerminalWidth returns the rendered width (columns) of a QR for data, so the
// caller can warn if the terminal is too narrow.
func qrTerminalWidth(data string, quietZone int) (int, error) {
	code, err := qr.Encode(data, qr.H, qr.Unicode)
	if err != nil {
		return 0, err
	}
	return code.Bounds().Max.X + 2*quietZone, nil
}
