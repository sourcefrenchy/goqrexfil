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
	zxing "github.com/makiuchi-d/gozxing"
	zxingqr "github.com/makiuchi-d/gozxing/qrcode"
	log "github.com/sirupsen/logrus"
	xdraw "golang.org/x/image/draw"
)

var zxingReader = zxingqr.NewQRCodeReader()

// decodeWithZXing tries to read a single QR code from img using gozxing.
func decodeWithZXing(img image.Image) string {
	bitmap, err := zxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return ""
	}
	res, err := zxingReader.Decode(bitmap, nil)
	if err != nil {
		return ""
	}
	return res.GetText()
}

// decodeWithGoQR tries to read a QR code from img using the legacy goqr reader.
func decodeWithGoQR(img image.Image) string {
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

// scaleImage returns a copy of img scaled by factor (using high-quality
// bi-linear interpolation). factor > 1 upscales, < 1 downscales.
func scaleImage(img image.Image, factor float64) image.Image {
	b := img.Bounds()
	w := int(float64(b.Dx())*factor + 0.5)
	h := int(float64(b.Dy())*factor + 0.5)
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// DecodeQRCode returns the payload string of the first QR found in img, or "" if
// none. It tries a ladder of decoders and scales: gozxing at native, 2x, and 0.5x,
// then the legacy goqr reader at native. The first success wins.
func DecodeQRCode(img image.Image) string {
	if s := decodeWithZXing(img); s != "" {
		return s
	}
	if s := decodeWithZXing(scaleImage(img, 2)); s != "" {
		return s
	}
	if s := decodeWithZXing(scaleImage(img, 0.5)); s != "" {
		return s
	}
	return decodeWithGoQR(img)
}

// decodeAll returns every QR payload found in img (a grid of codes). It uses a
// find-blank-repeat loop: decode one code, blank its bounding box, repeat, until
// nothing more is found or maxCodes is reached.
func decodeAll(img image.Image, maxCodes int) []string {
	if maxCodes <= 0 {
		maxCodes = 16
	}
	var out []string
	seen := make(map[string]bool)
	work := img
	for i := 0; i < maxCodes; i++ {
		text := decodeWithZXing(work)
		if text == "" {
			break
		}
		if seen[text] {
			// Same code found again (shouldn't happen after blanking); stop to avoid a loop.
			break
		}
		seen[text] = true
		out = append(out, text)
		work = blankRegion(work, resultPoints(img, text))
	}
	return out
}

// resultPoints returns the bounding box of the first QR found in img that decodes
// to text (best-effort; used to blank it). Returns the image bounds if unknown.
func resultPoints(img image.Image, text string) image.Rectangle {
	bitmap, err := zxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return img.Bounds()
	}
	res, err := zxingReader.Decode(bitmap, nil)
	if err != nil || res.GetText() != text {
		return img.Bounds()
	}
	minX, minY, maxX, maxY := 1<<30, 1<<30, -1, -1
	for _, p := range res.GetResultPoints() {
		x, y := int(p.GetX()), int(p.GetY())
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}
	if minX > maxX || minY > maxY {
		return img.Bounds()
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

// blankRegion returns a copy of img with rect filled white (plus a margin), so a
// subsequent decode skips that code.
func blankRegion(img image.Image, rect image.Rectangle) image.Image {
	b := img.Bounds()
	blank := image.NewRGBA(b)
	draw.Draw(blank, b, img, b.Min, draw.Src)
	m := 12 // margin in pixels to fully cover the quiet zone
	r := rect
	if r.Min.X-m > b.Min.X {
		r.Min.X -= m
	} else {
		r.Min.X = b.Min.X
	}
	if r.Min.Y-m > b.Min.Y {
		r.Min.Y -= m
	} else {
		r.Min.Y = b.Min.Y
	}
	if r.Max.X+m < b.Max.X {
		r.Max.X += m
	} else {
		r.Max.X = b.Max.X
	}
	if r.Max.Y+m < b.Max.Y {
		r.Max.Y += m
	} else {
		r.Max.Y = b.Max.Y
	}
	draw.Draw(blank, r, image.White, image.Point{}, draw.Over)
	return blank
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

// composeGridPNG renders a grid of QR codes (cols across, rows down) as a single
// crisp PNG. Each cell is the native QR (1 module = pxPerModule pixels, nearest
// neighbor for crispness) with a quiet zone; cells are separated by a gap. Used by
// the end-to-end video tests to synthesize grid frames.
func composeGridPNG(datas []string, cols, pxPerModule, quiet, gap int) []byte {
	// Encode all cells natively first to learn the module size (uniform for a
	// fixed symbol size).
	natives := make([]image.Image, len(datas))
	var n int
	for i, d := range datas {
		c, err := qr.Encode(d, qr.H, qr.Unicode)
		if err != nil {
			log.Fatal(err)
		}
		natives[i] = c
		n = c.Bounds().Max.X
	}
	cell := n * pxPerModule
	cellW := cell + 2*quiet
	rows := (len(datas) + cols - 1) / cols
	totalW := cols*cellW + (cols-1)*gap
	totalH := rows*cellW + (rows-1)*gap
	img := image.NewRGBA(image.Rect(0, 0, totalW, totalH))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Over)
	for i, d := range datas {
		_ = d
		cx := i % cols
		cy := i / cols
		ox := cx*(cellW+gap) + quiet
		oy := cy*(cellW+gap) + quiet
		// Upscale the native QR to cell x cell, nearest neighbor (crisp).
		scaled := image.NewRGBA(image.Rect(0, 0, cell, cell))
		xdraw.NearestNeighbor.Scale(scaled, scaled.Bounds(), natives[i], natives[i].Bounds(), draw.Over, nil)
		draw.Draw(img, image.Rect(ox, oy, ox+cell, oy+cell), scaled, image.Point{}, draw.Over)
	}
	var buff bytes.Buffer
	if err := png.Encode(&buff, img); err != nil {
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

// qrMatrix returns the module matrix for data, including a quietZone light border
// on all sides. The returned matrix is (n+2*quietZone) wide and tall.
func qrMatrix(data string, quietZone int) ([][]bool, int, error) {
	code, err := qr.Encode(data, qr.H, qr.Unicode)
	if err != nil {
		return nil, 0, err
	}
	n := code.Bounds().Max.X // native module count (always odd)
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
	return m, N, nil
}

// renderQRTerminal renders data as a QR code using Unicode half-block characters,
// pairing two module rows into one text line. quietZone is the light border in
// modules (4 = QR spec). No scaling is applied, so modules stay crisp.
func renderQRTerminal(data string, quietZone int) (string, error) {
	m, N, err := qrMatrix(data, quietZone)
	if err != nil {
		return "", err
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

// renderGridTerminal renders a grid of QR codes (cols across, rows down) as
// terminal text. Each cell is a half-block QR with a quietZone border; cells are
// separated by gap columns (quiet zones) and gap/2 rows. All cells must be the
// same size (they are, for a fixed symbol size).
func renderGridTerminal(datas []string, cols, quietZone, gap int) (string, error) {
	if cols < 1 {
		cols = 1
	}
	matrices := make([][][]bool, len(datas))
	var cell int
	for i, d := range datas {
		m, N, err := qrMatrix(d, quietZone)
		if err != nil {
			return "", err
		}
		matrices[i] = m
		cell = N
	}
	rows := (len(datas) + cols - 1) / cols
	// Grid dimensions in modules.
	gw := cols*cell + (cols-1)*gap
	gh := rows*cell + (rows-1)*gap
	// Pad to an even height so half-block pairing is clean.
	if gh%2 != 0 {
		gh++
	}

	var sb strings.Builder
	for y := 0; y < gh; y += 2 {
		for x := 0; x < gw; x++ {
			top := darkAt(matrices, cols, cell, gap, x, y)
			bot := false
			if y+1 < gh {
				bot = darkAt(matrices, cols, cell, gap, x, y+1)
			}
			switch {
			case top && bot:
				sb.WriteRune('█')
			case top:
				sb.WriteRune('▀')
			case bot:
				sb.WriteRune('▄')
			default:
				sb.WriteRune(' ')
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// darkAt reports whether module (x,y) of the composed grid is dark. matrices is
// the flat list of cells (row-major), each a [][]bool of size cell x cell.
func darkAt(matrices [][][]bool, cols, cell, gap, x, y int) bool {
	cx := x / (cell + gap)
	cy := y / (cell + gap)
	if cx >= cols {
		return false
	}
	ix := x - cx*(cell+gap)
	iy := y - cy*(cell+gap)
	if ix >= cell || iy >= cell {
		return false // in the gap
	}
	idx := cy*cols + cx
	if idx >= len(matrices) {
		return false
	}
	return matrices[idx][iy][ix]
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

// displayGridTerminal clears the screen and renders a grid of QR codes.
func displayGridTerminal(datas []string, cols, quietZone, gap int, caption string) error {
	block, err := renderGridTerminal(datas, cols, quietZone, gap)
	if err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("\x1b[2J\x1b[H")
	if caption != "" {
		sb.WriteString(caption)
		sb.WriteString("\n\n")
	}
	sb.WriteString(block)
	_, err = os.Stdout.WriteString(sb.String())
	return err
}

// qrTerminalWidth returns the rendered width (columns) of a single QR for data.
func qrTerminalWidth(data string, quietZone int) (int, error) {
	code, err := qr.Encode(data, qr.H, qr.Unicode)
	if err != nil {
		return 0, err
	}
	return code.Bounds().Max.X + 2*quietZone, nil
}

// gridTerminalSize returns the rendered (cols, rows) of a grid of QR codes.
func gridTerminalSize(datas []string, cols, quietZone, gap int) (int, int, error) {
	if cols < 1 {
		cols = 1
	}
	if len(datas) == 0 {
		return 0, 0, nil
	}
	_, cell, err := qrMatrix(datas[0], quietZone)
	if err != nil {
		return 0, 0, err
	}
	rows := (len(datas) + cols - 1) / cols
	gw := cols*cell + (cols-1)*gap
	gh := rows*cell + (rows-1)*gap
	if gh%2 != 0 {
		gh++
	}
	return gw, gh / 2, nil
}
