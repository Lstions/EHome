package api

import (
	"strconv"
	"testing"
	"time"
)

func TestFirmwareDownloadTicketRejectsExpiredAndTamperedValues(t *testing.T) {
	secret := []byte("ticket-secret")
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	signature := signFirmwareDownload("firmware.bin", expires, secret)
	if !validateFirmwareDownload("firmware.bin", expires.Format("150405"), signature, secret, now) {
		// Format("150405") is intentionally not a Unix timestamp.
	} else {
		t.Fatal("non-Unix expiry accepted")
	}
	expiresText := "" // set below to keep exact Unix value used for signature
	expiresText = fmtInt(expires.Unix())
	if !validateFirmwareDownload("firmware.bin", expiresText, signature, secret, now) {
		t.Fatal("valid ticket rejected")
	}
	if validateFirmwareDownload("other.bin", expiresText, signature, secret, now) {
		t.Fatal("ticket accepted for another firmware")
	}
	if validateFirmwareDownload("firmware.bin", fmtInt(now.Add(-time.Second).Unix()), signature, secret, now) {
		t.Fatal("expired ticket accepted")
	}
}

func fmtInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
