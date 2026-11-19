package firmwareticket

import (
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestTicketRoundTrip(t *testing.T) {
	secret := []byte("ticket-secret")
	now := time.Now().UTC()
	expires := now.Add(DefaultTTL)
	signature := Sign("firmware.bin", expires, secret)

	if !Validate("firmware.bin", strconv.FormatInt(expires.Unix(), 10), signature, secret, now) {
		t.Fatal("valid ticket rejected")
	}
	// 死线就是死线：到期时刻起（含相等）不再接受。
	if Validate("firmware.bin", strconv.FormatInt(expires.Unix(), 10), signature, secret, expires) {
		t.Fatal("ticket accepted at its own expiry instant")
	}
	if Validate("firmware.bin", strconv.FormatInt(expires.Unix(), 10), signature, secret, expires.Add(-time.Nanosecond)) {
		t.Fatal("ticket rejected 1ns before expiry")
	}
}

func TestTicketRejectsOtherFirmwareAndTampering(t *testing.T) {
	secret := []byte("ticket-secret")
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	expiresText := strconv.FormatInt(expires.Unix(), 10)
	signature := Sign("firmware.bin", expires, secret)

	if Validate("other.bin", expiresText, signature, secret, now) {
		t.Fatal("ticket accepted for another firmware")
	}
	if Validate("firmware.bin", expiresText, signature+"x", secret, now) {
		t.Fatal("tampered signature accepted")
	}
	if Validate("firmware.bin", expiresText, signature, []byte("another-secret"), now) {
		t.Fatal("ticket accepted with a different secret")
	}
	if Validate("firmware.bin", "not-a-timestamp", signature, secret, now) {
		t.Fatal("non-numeric expiry accepted")
	}
	if Validate("firmware.bin", "", signature, secret, now) {
		t.Fatal("empty expiry accepted")
	}
}

// TestTicketRejectsExpiryBeyondMaxLifetime 固定「票据不能自己延长寿命」的口径：
// 手改 expires 到 24h 之外必须被拒，否则 MaxLifetime 形同虚设。
func TestTicketRejectsExpiryBeyondMaxLifetime(t *testing.T) {
	secret := []byte("ticket-secret")
	now := time.Now().UTC()
	far := now.Add(MaxLifetime + time.Second)
	signature := Sign("firmware.bin", far, secret)
	if Validate("firmware.bin", strconv.FormatInt(far.Unix(), 10), signature, secret, now) {
		t.Fatal("ticket beyond MaxLifetime accepted")
	}
	// 边界内仍然可用（说明拒绝来自上限而不是别的）。
	within := now.Add(MaxLifetime - time.Second)
	signature = Sign("firmware.bin", within, secret)
	if !Validate("firmware.bin", strconv.FormatInt(within.Unix(), 10), signature, secret, now) {
		t.Fatal("ticket within MaxLifetime rejected")
	}
}

// TestURLUsesSigningMoment 覆盖 AC-11 的一半：expires 必须从**签发时刻**起算，
// 而不是某个更早的时刻（上传时间）。
func TestURLUsesSigningMoment(t *testing.T) {
	secret := []byte("ticket-secret")
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	raw := URLFor("http://host", "fw.bin", now, DefaultTTL, secret)

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	if parsed.Path != "/api/v1/firmwares/fw.bin/download" {
		t.Fatalf("path = %q", parsed.Path)
	}
	expires, err := strconv.ParseInt(parsed.Query().Get("expires"), 10, 64)
	if err != nil {
		t.Fatalf("expires is not a Unix timestamp: %q", parsed.Query().Get("expires"))
	}
	if want := now.Add(DefaultTTL).Unix(); expires != want {
		t.Fatalf("expires = %d, want %d (signing moment + DefaultTTL)", expires, want)
	}
	if !Validate("fw.bin", parsed.Query().Get("expires"), parsed.Query().Get("signature"), secret, now) {
		t.Fatal("ticket embedded in URL does not validate")
	}
}
