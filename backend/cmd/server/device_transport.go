package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"ehome/backend/internal/config"
	"ehome/backend/internal/transport"
	"ehome/backend/pkg/logger"
)

// device_transport.go -- brings up the 3.0 device-facing listener.
//
// # Why this is a separate file, and why it is opt-in
//
// Design §7.3 stages MQTT retirement as P0..P4, and P0 is "the backend listens
// on BOTH MQTT and TCP". This file is that step. It is off unless
// device.enabled is true in config, so deploying this build changes NOTHING
// until an operator opts in -- which is what makes the rollout reversible.
//
// # The two failures worth designing against
//
//  1. A half-configured listener that silently does not start. Then "I enabled
//     it" and "the device cannot connect" look identical from the outside.
//     => config validation refuses partial settings, and this function returns
//     a hard error rather than logging and continuing.
//
//  2. A listener that comes up but is unreachable from the routing layer.
//     Then devices connect, send Hello, and get no HelloAck -- which looks
//     like a firmware bug. => the Registry created here is the SAME object
//     handed to the downlink bridge below; there is no second instance.

// deviceTransport bundles what the rest of main() needs.
type deviceTransport struct {
	server   *transport.Server
	stopFunc context.CancelFunc
}

// startDeviceTransport loads certificates, starts the listener and returns a
// bridge that prefers it. Returns (nil, nil) when the feature is disabled.
func startDeviceTransport(cfg *config.Config, onFrame transport.FrameCallback, reg *transport.Registry) (*deviceTransport, error) {
	if !cfg.Device.Enabled {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.Device.CertFile, cfg.Device.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("device transport: load server keypair: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.Device.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("device transport: read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		// An empty pool would make every device certificate fail verification,
		// i.e. the listener would accept nobody -- a silent total outage that
		// looks like a certificate problem on the device side.
		return nil, fmt.Errorf("device transport: client CA file %q contains no usable certificates",
			cfg.Device.ClientCAFile)
	}

	srv, err := transport.New(transport.Config{
		Addr:             cfg.Device.Addr,
		Registry:         reg,
		Cert:             cert,
		ClientCAs:        pool,
		ReadTimeout:      secondsOrDefault(cfg.Device.ReadTimeoutSec, 0),
		WriteTimeout:     secondsOrDefault(cfg.Device.WriteTimeoutSec, 10*time.Second),
		HandshakeTimeout: 10 * time.Second,
		Logger:           logger.Current(),
		OnFrame:          onFrame,
	})
	if err != nil {
		return nil, fmt.Errorf("device transport: %w", err)
	}
	if err := srv.Listen(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := srv.Serve(ctx); err != nil {
			logger.Errorf("Device transport stopped: %v", err)
		}
	}()

	logger.Infof("Device transport (3.0 TCP+TLS, mTLS) listening on %s", srv.Addr())

	return &deviceTransport{
		server:   srv,
		stopFunc: cancel,
	}, nil
}

// stop shuts the listener down and cancels Serve.
func (d *deviceTransport) stop() {
	if d == nil {
		return
	}
	if d.stopFunc != nil {
		d.stopFunc()
	}
	if d.server != nil {
		if err := d.server.Close(); err != nil {
			logger.Warnf("Device transport close: %v", err)
		} else {
			logger.Infof("Device transport stopped")
		}
	}
}

func secondsOrDefault(sec int, def time.Duration) time.Duration {
	if sec <= 0 {
		return def
	}
	return time.Duration(sec) * time.Second
}
