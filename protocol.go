package main

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/kjk/smaz"
	"github.com/xssnick/raptorq"
)

const (
	protocolPrefix = "GQ2:" // GQ2:<compressedLen>:<symbolID>:<base64(symbol)>
	symbolSize     = 240    // raw bytes per RaptorQ symbol -> ~320 base64 chars per QR
)

// encodePayload compresses the payload and turns it into a stream of RaptorQ
// symbols, each framed for a QR code. It returns the framed QR payloads (in
// symbol-id order) and k, the number of base (source) symbols.
func encodePayload(payload []byte, redundancyPct int) ([]string, int, error) {
	compressed := smaz.Encode(nil, payload)
	rq := raptorq.NewRaptorQ(symbolSize)
	enc, err := rq.CreateEncoder(compressed)
	if err != nil {
		return nil, 0, err
	}
	k := int(enc.BaseSymbolsNum())
	m := k + k*redundancyPct/100
	if m < k+1 {
		m = k + 1
	}
	framed := make([]string, 0, m)
	for id := 0; id < m; id++ {
		sym := enc.GenSymbol(uint32(id))
		framed = append(framed, formatSymbol(uint32(len(compressed)), uint32(id), sym))
	}
	return framed, k, nil
}

// formatSymbol wraps a RaptorQ symbol with the header the decoder needs:
// GQ2:<compressedLen>:<symbolID>:<base64(symbol)>. The ':' separators cannot
// appear in base64, so framing is unambiguous.
func formatSymbol(compressedLen, id uint32, symbol []byte) string {
	return fmt.Sprintf("%s%d:%d:%s", protocolPrefix, compressedLen, id, base64.StdEncoding.EncodeToString(symbol))
}

// parseSymbol is the inverse of formatSymbol.
func parseSymbol(s string) (compressedLen, id uint32, symbol []byte, ok bool) {
	if !strings.HasPrefix(s, protocolPrefix) {
		return 0, 0, nil, false
	}
	rest := s[len(protocolPrefix):]
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 {
		return 0, 0, nil, false
	}
	cl, err1 := strconv.ParseUint(parts[0], 10, 32)
	idv, err2 := strconv.ParseUint(parts[1], 10, 32)
	if err1 != nil || err2 != nil {
		return 0, 0, nil, false
	}
	sym, err3 := base64.StdEncoding.DecodeString(parts[2])
	if err3 != nil {
		return 0, 0, nil, false
	}
	return uint32(cl), uint32(idv), sym, true
}

// decoder accumulates RaptorQ symbols from video frames and reports when the
// payload becomes reconstructable. It is lazily initialized from the first
// symbol seen.
type decoder struct {
	dec        *raptorq.Decoder
	compressed uint32
	symbolSz   uint32
	received   int
	seen       map[uint32]bool
}

func (d *decoder) init(compressedLen, symbolSz uint32) error {
	if d.dec != nil {
		return nil
	}
	rq := raptorq.NewRaptorQ(symbolSz)
	dec, err := rq.CreateDecoder(compressedLen)
	if err != nil {
		return err
	}
	d.dec = dec
	d.compressed = compressedLen
	d.symbolSz = symbolSz
	d.seen = make(map[uint32]bool)
	return nil
}

// add feeds a symbol. It returns true once the payload is decodable. Duplicate
// symbol ids (repeated frames) are ignored.
func (d *decoder) add(id uint32, symbol []byte, compressedLen uint32) (bool, error) {
	if d.dec == nil {
		if err := d.init(compressedLen, uint32(len(symbol))); err != nil {
			return false, err
		}
	}
	if d.seen[id] {
		return false, nil
	}
	d.seen[id] = true
	if _, err := d.dec.AddSymbol(id, symbol); err != nil {
		return false, err
	}
	d.received++
	// Probing Decode() is expensive; only try once we plausibly have enough.
	if d.received < d.baseSymbols() {
		return false, nil
	}
	ok, _, err := d.dec.Decode()
	return ok, err
}

// baseSymbols estimates k = ceil(compressed/symbolSize) for progress display.
func (d *decoder) baseSymbols() int {
	if d.compressed == 0 || d.symbolSz == 0 {
		return 0
	}
	return int((d.compressed + d.symbolSz - 1) / d.symbolSz)
}

// received reports how many distinct symbols have been fed.
func (d *decoder) receivedCount() int { return d.received }

// ready reports whether the decoder has been initialized.
func (d *decoder) ready() bool { return d.dec != nil }

// finish reconstructs and decompresses the payload.
func (d *decoder) finish() ([]byte, error) {
	if d.dec == nil {
		return nil, fmt.Errorf("no symbols received")
	}
	ok, compressed, err := d.dec.Decode()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("not enough symbols to decode (received %d, need ~%d)", d.received, d.baseSymbols())
	}
	return smaz.Decode(nil, compressed)
}
