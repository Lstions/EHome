package config

import (
	"strings"
	"testing"
)

// device_test.go -- the 3.0 device-listener configuration.
//
// The behaviour that matters: an operator who has NOT opted in must never be
// blocked by 3.0 settings, and an operator who HAS opted in must not end up
// with a listener that silently fails to start. "I enabled it" and "the device
// cannot connect" must not look identical.

// TestDeviceDisabledByDefault -- deploying this build must not change
// behaviour. That is the property that makes the rollout safe.
func TestDeviceDisabledByDefault(t *testing.T) {
	if defaultConfig().Device.Enabled {
		t.Fatal("the 3.0 device listener is enabled by default; deploying this " +
			"build would start listening on a new port without anyone asking")
	}
}

// TestDisabledDeviceConfigIsNotValidated -- someone who has not opted in has
// no cert paths yet, and must not be blocked by them.
func TestDisabledDeviceConfigIsNotValidated(t *testing.T) {
	empty := DeviceConfig{} // nothing set at all
	if err := empty.Validate(); err != nil {
		t.Fatalf("a DISABLED device config was rejected: %v -- an operator who "+
			"has not opted in must not need 3.0 settings", err)
	}
}

// TestEnabledDeviceConfigRequiresEverything -- each required field, one at a
// time, so the error names the actual missing piece.
func TestEnabledDeviceConfigRequiresEverything(t *testing.T) {
	full := DeviceConfig{
		Enabled:      true,
		Addr:         ":8443",
		CertFile:     "/etc/ehome/server.crt",
		KeyFile:      "/etc/ehome/server.key",
		ClientCAFile: "/etc/ehome/device-ca.crt",
	}
	if err := full.Validate(); err != nil {
		t.Fatalf("a complete device config was rejected: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*DeviceConfig)
		wantSub string
	}{
		{"no addr", func(d *DeviceConfig) { d.Addr = "" }, "device.addr"},
		{"no cert", func(d *DeviceConfig) { d.CertFile = "" }, "cert_file"},
		{"no key", func(d *DeviceConfig) { d.KeyFile = "" }, "key_file"},
		{"no client CA", func(d *DeviceConfig) { d.ClientCAFile = "" }, "client_ca_file"},
	}
	for _, c := range cases {
		d := full
		c.mutate(&d)
		err := d.Validate()
		if err == nil {
			t.Errorf("%s: enabled config accepted -- the listener would either "+
				"fail at startup with no explanation or, worse, run without mTLS", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("%s: error %q does not name %q", c.name, err, c.wantSub)
		}
	}
}

// TestMissingClientCANamesTheReason -- the client CA is the field most likely
// to be forgotten, and its absence is the one that silently DISABLES mTLS.
// The message must say what is at stake, not just "required".
func TestMissingClientCANamesTheReason(t *testing.T) {
	d := DeviceConfig{Enabled: true, Addr: ":8443",
		CertFile: "c", KeyFile: "k"}
	err := d.Validate()
	if err == nil {
		t.Fatal("accepted a device listener with no client CA")
	}
	if !strings.Contains(err.Error(), "mTLS") {
		t.Errorf("error %q does not mention mTLS; an operator reading it would "+
			"not realise the listener would accept unauthenticated clients", err)
	}
}
