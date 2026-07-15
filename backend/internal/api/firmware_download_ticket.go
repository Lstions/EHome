package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

func signFirmwareDownload(filename string, expiresAt time.Time, secret []byte) string {
	message := filename + "\n" + strconv.FormatInt(expiresAt.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validateFirmwareDownload(filename, expires, signature string, secret []byte, now time.Time) bool {
	expiresUnix, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || expiresUnix <= now.Unix() || expiresUnix > now.Add(24*time.Hour).Unix() {
		return false
	}
	expected := signFirmwareDownload(filename, time.Unix(expiresUnix, 0), secret)
	return hmac.Equal([]byte(expected), []byte(signature))
}

func firmwareDownloadURL(filename, baseURL string, ttl time.Duration, secret []byte) string {
	expires := time.Now().UTC().Add(ttl)
	return fmt.Sprintf("%s/api/v1/firmwares/%s/download?expires=%d&signature=%s", baseURL, filename, expires.Unix(), signFirmwareDownload(filename, expires, secret))
}
