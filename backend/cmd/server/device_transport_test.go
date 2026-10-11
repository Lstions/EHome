package main

import (
	"testing"

	"ehome/backend/internal/config"
	"ehome/backend/pkg/protoframe"
)

// device_transport_test.go -- the opt-in gate.
//
// The property that makes this whole change safe to deploy: with
// device.enabled unset, NO listener comes up. That is asserted here rather
// than assumed, because a mutation run showed the gate had no coverage at
// all -- nothing else exercises startDeviceTransport without real certificates.

// TestDisabledStartsNothing -- disabled means nothing at all: no listener, no
// error, no half-built object.
func TestDisabledStartsNothing(t *testing.T) {
	cfg := &config.Config{} // Device.Enabled false, as in a default config
	dt, err := startDeviceTransport(cfg, nil, nil)
	if err != nil {
		t.Fatalf("a disabled device transport returned an error: %v -- every "+
			"existing deployment would fail to start", err)
	}
	if dt != nil {
		t.Fatal("a disabled device transport returned a non-nil object; " +
			"something would then be started or closed that nobody asked for")
	}
}

// TestDisabledIgnoresIncompleteSettings -- an operator who has not opted in
// must not be blocked by cert paths they do not have yet.
func TestDisabledIgnoresIncompleteSettings(t *testing.T) {
	cfg := &config.Config{}
	cfg.Device.Enabled = false
	cfg.Device.Addr = ":8443" // set, but cert files are not
	dt, err := startDeviceTransport(cfg, nil, nil)
	if err != nil {
		t.Fatalf("disabled + partial settings returned %v; opting in is the "+
			"only thing that should require the full set", err)
	}
	if dt != nil {
		t.Fatal("disabled returned a transport")
	}
}

// TestEnabledWithoutCertificatesFailsLoudly -- enabled but unusable must ERROR,
// not silently return "nothing to do".
//
// Silent return would mean "I enabled it" and "the device cannot connect" are
// indistinguishable from the outside -- the exact confusion this whole staged
// rollout exists to avoid.
func TestEnabledWithoutCertificatesFailsLoudly(t *testing.T) {
	cfg := &config.Config{}
	cfg.Device.Enabled = true
	cfg.Device.Addr = ""
	// Validation is normally done in main(); this asserts the transport
	// helper does not paper over a bad config.
	if err := cfg.Device.Validate(); err == nil {
		t.Fatal("Validate accepted an enabled-but-empty device config")
	}

	// And with validation bypassed, attempting to start must still fail
	// rather than return a nil transport (which a caller would treat as
	// "feature off").
	cfg.Device.Addr = ":0"
	cfg.Device.CertFile = "/nonexistent/cert.pem"
	cfg.Device.KeyFile = "/nonexistent/key.pem"
	cfg.Device.ClientCAFile = "/nonexistent/ca.pem"
	dt, err := startDeviceTransport(cfg, func(string, protoframe.Header, []byte) error {
		return nil
	}, nil)
	if err == nil {
		if dt != nil {
			dt.stop()
		}
		t.Fatal("enabled with unreadable certificates returned no error; a " +
			"caller cannot distinguish that from the feature being off")
	}
	if dt != nil {
		t.Fatal("a failed start must not also return a transport")
	}
}
