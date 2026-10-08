package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// zstdCompress compresses data with zstd (replacing the old smaz step).
func zstdCompress(data []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(data, nil), nil
}

// zstdDecompress is the inverse of zstdCompress.
func zstdDecompress(data []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(data, nil)
}

// deriveKey turns a passphrase into a 32-byte AES-256 key via SHA-256.
func deriveKey(passphrase string) []byte {
	sum := sha256.Sum256([]byte(passphrase))
	return sum[:]
}

// aesGcmSeal encrypts plaintext with AES-256-GCM, returning nonce||ciphertext||tag.
func aesGcmSeal(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// aesGcmOpen decrypts a blob produced by aesGcmSeal (nonce||ciphertext||tag).
func aesGcmOpen(key, blob []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, blob[:ns], blob[ns:], nil)
}
