package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
)

func productWrite(path string, raw []byte) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return errPreparation
	}
	_, e = f.Write(raw)
	s := f.Sync()
	c := f.Close()
	if e != nil || s != nil || c != nil {
		return errPreparation
	}
	return nil
}
func productJSON(path string, v any) (string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	return urlSHA(raw), productWrite(path, raw)
}

func productRandom(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("random_unavailable")
	}
	return hex.EncodeToString(b)
}
