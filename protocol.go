package main

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/xssnick/raptorq"
)

const (
	protocolPrefix = "GQ3:" // GQ3:<mode>:<blobLen>:<symbolID>:<base64(symbol)>
	symbolSize     = 240    // raw bytes per RaptorQ symbol -> ~320 base64 chars per QR

	modeCompressed = "z"  // blob is zstd-compressed
	modeEncrypted  = "ze" // blob is zstd-compressed then AES-GCM encrypted
)

// encodePayload compresses (and optionally encrypts) the payload and turns it into
// a stream of RaptorQ symbols, each framed for a QR code. It returns the framed
// QR payloads (in symbol-id order), k (the number of base symbols), and the mode.
func encodePayload(payload []byte, redundancyPct int, key string) ([]string, int, string, error) {
	compressed, err := zstdCompress(payload)
	if err != nil {
		return nil, 0, "", err
	}
	blob := compressed
	mode := modeCompressed
	if key != "" {
		blob, err = aesGcmSeal(deriveKey(key), compressed)
		if err != nil {
			return nil, 0, "", err
		}
		mode = modeEncrypted
	}

	rq := raptorq.NewRaptorQ(symbolSize)
	enc, err := rq.CreateEncoder(blob)
	if err != nil {
		return nil, 0, "", err
	}
	k := int(enc.BaseSymbolsNum())
	m := k + k*redundancyPct/100
	if m < k+1 {
		m = k + 1
	}
	framed := make([]string, 0, m)
	for id := 0; id < m; id++ {
		sym := enc.GenSymbol(uint32(id))
		framed = append(framed, formatSymbol(mode, uint32(len(blob)), uint32(id), sym))
	}
	return framed, k, mode, nil
}

// formatSymbol wraps a RaptorQ symbol with the header the decoder needs:
// GQ3:<mode>:<blobLen>:<symbolID>:<base64(symbol)>. The ':' separators cannot
// appear in base64, so framing is unambiguous.
func formatSymbol(mode string, blobLen, id uint32, symbol []byte) string {
	return fmt.Sprintf("%s%s:%d:%d:%s", protocolPrefix, mode, blobLen, id, base64.StdEncoding.EncodeToString(symbol))
}

// parseSymbol is the inverse of formatSymbol.
func parseSymbol(s string) (mode string, blobLen, id uint32, symbol []byte, ok bool) {
	if !strings.HasPrefix(s, protocolPrefix) {
		return "", 0, 0, nil, false
	}
	rest := s[len(protocolPrefix):]
	parts := strings.SplitN(rest, ":", 4)
	if len(parts) != 4 {
		return "", 0, 0, nil, false
	}
	if parts[0] != modeCompressed && parts[0] != modeEncrypted {
		return "", 0, 0, nil, false
	}
	bl, err1 := strconv.ParseUint(parts[1], 10, 32)
	idv, err2 := strconv.ParseUint(parts[2], 10, 32)
	if err1 != nil || err2 != nil {
		return "", 0, 0, nil, false
	}
	sym, err3 := base64.StdEncoding.DecodeString(parts[3])
	if err3 != nil {
		return "", 0, 0, nil, false
	}
	return parts[0], uint32(bl), uint32(idv), sym, true
}

// decoder accumulates RaptorQ symbols from video frames and reports when the
// payload becomes reconstructable. It is lazily initialized from the first
// symbol seen. Not concurrency-safe; guard externally if shared.
type decoder struct {
	dec      *raptorq.Decoder
	mode     string
	blobLen  uint32
	symbolSz uint32
	key      []byte
	received int
	seen     map[uint32]bool
}

// newDecoder creates a decoder that will decrypt with key (nil = no encryption).
func newDecoder(key string) *decoder {
	var k []byte
	if key != "" {
		k = deriveKey(key)
	}
	return &decoder{key: k}
}

func (d *decoder) init(mode string, blobLen, symbolSz uint32) error {
	if d.dec != nil {
		return nil
	}
	rq := raptorq.NewRaptorQ(symbolSz)
	dec, err := rq.CreateDecoder(blobLen)
	if err != nil {
		return err
	}
	d.dec = dec
	d.mode = mode
	d.blobLen = blobLen
	d.symbolSz = symbolSz
	d.seen = make(map[uint32]bool)
	return nil
}

// add feeds a symbol. It returns true once the payload is decodable. Duplicate
// symbol ids (repeated frames) are ignored.
func (d *decoder) add(mode string, blobLen, id uint32, symbol []byte) (bool, error) {
	if d.dec == nil {
		if err := d.init(mode, blobLen, uint32(len(symbol))); err != nil {
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

// baseSymbols estimates k = ceil(blobLen/symbolSize) for progress display.
func (d *decoder) baseSymbols() int {
	if d.blobLen == 0 || d.symbolSz == 0 {
		return 0
	}
	return int((d.blobLen + d.symbolSz - 1) / d.symbolSz)
}

// receivedCount reports how many distinct symbols have been fed.
func (d *decoder) receivedCount() int { return d.received }

// ready reports whether the decoder has been initialized.
func (d *decoder) ready() bool { return d.dec != nil }

// finish reconstructs the RaptorQ blob and reverses encryption + compression.
func (d *decoder) finish() ([]byte, error) {
	if d.dec == nil {
		return nil, fmt.Errorf("no symbols received")
	}
	ok, blob, err := d.dec.Decode()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("not enough symbols to decode (received %d, need ~%d)", d.received, d.baseSymbols())
	}
	if d.mode == modeEncrypted {
		blob, err = aesGcmOpen(d.key, blob)
		if err != nil {
			return nil, fmt.Errorf("decrypt: %w", err)
		}
	}
	return zstdDecompress(blob)
}
