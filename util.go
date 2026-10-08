package main

import (
	"encoding/base32"
	"fmt"
	"strings"
)

// humanCode derives a short, human-comparable verification code from a payload
// hash: the first 5 bytes (40 bits) base32-encoded, grouped in fours, e.g.
// "K7PD-4MQX". The client and server both compute it from the payload hash so a
// user can confirm a successful transfer by eye instead of comparing hex.
func humanCode(hash []byte) string {
	if len(hash) < 5 {
		return ""
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:5])
	// enc is 8 chars; group into 4-4.
	return fmt.Sprintf("%s-%s", enc[:4], enc[4:])
}

// progressBar renders a simple text progress bar, e.g. "[#####-----] 50%".
func progressBar(done, total int, width int) string {
	if total <= 0 {
		return ""
	}
	if width <= 0 {
		width = 10
	}
	frac := float64(done) / float64(total)
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(width) + 0.5)
	return fmt.Sprintf("[%s%s] %3d%%", strings.Repeat("#", filled), strings.Repeat("-", width-filled), int(frac*100+0.5))
}
