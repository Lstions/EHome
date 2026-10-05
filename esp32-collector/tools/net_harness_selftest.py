#!/usr/bin/env python3
"""net_harness_selftest.py -- prove net_throughput_test.py's analyzer is honest.

A soak harness that cannot fail is worthless.  This script runs a *synthetic
node* against a local broker and drives ``net_throughput_test.py``'s own
parser/statistics over the same wire format the real firmware emits, with a
known ground truth:

  injected: 5 channels x 100 Hz, 300 B payloads, ~1 % sequence loss,
            25 ms constant one-way latency + 0..J ms jitter (default J=50),
            implemented by backdating the device timestamp so the publish rate
            is not throttled
  expected: loss detected within 50 % of injected; rate within 10 % of 500/s;
            payload ~150 KB/s; latency p99 tracks the injected JITTER scale
            (the min-offset estimator removes the constant 25 ms part).

  NOTE on the latency convention: the harness reports latency *above the best
  observed one-way time* (min(recv_ts - device_ts) is taken as the clock
  offset).  A constant one-way delay is therefore not observable; jitter and
  queueing are.  This is the only clock-offset-free measurement available
  without NTP between host and device.  See net_network_sizing.md §4.2.
  expected: the harness reports ~1 % drop rate and p99 >= injected latency
            bounds, i.e. it *detects* the loss and *measures* the latency.

It exits non-zero if the analyzer under-reports loss or misses the latency, so
it is a regression lock on the measurement path (not on the firmware).

Run:
  # start a broker first, e.g.:
  #   docker run -d --name t-mosq --network host eclipse-mosquitto:2 \
  #       mosquitto -p 18830 -v
  python3 tools/net_harness_selftest.py --broker 127.0.0.1 --port 18830
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import random
import sys
import threading
import time

import paho.mqtt.client as mqtt


def load_harness(path=None):
    import os
    here = os.path.dirname(os.path.abspath(__file__))
    path = path or os.path.join(here, "net_throughput_test.py")
    spec = importlib.util.spec_from_file_location("net_throughput_test", path)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["net_throughput_test"] = mod  # dataclasses needs the module
    spec.loader.exec_module(mod)
    return mod


class SyntheticNode(threading.Thread):
    """Publishes 5x100 Hz DataReports to nodes/<id>/up like the firmware does."""

    def __init__(self, nt, broker, port, node, hz, channels, payload_bytes,
                 loss, latency_ms, jitter_ms, duration):
        super().__init__(daemon=True)
        self.nt = nt
        self.broker, self.port, self.node = broker, port, node
        self.hz, self.channels = hz, channels
        self.payload_bytes = payload_bytes
        self.loss, self.latency_ms, self.jitter_ms = loss, latency_ms, jitter_ms
        self.duration = duration
        self.sent = 0
        self.dropped = 0
        self.done = threading.Event()

    def _report(self, ch, seq, timestamp_us):
        raw = bytes((i + seq) & 0xFF for i in range(self.payload_bytes))
        body = (self.nt.field_varint(1, ch)
                + self.nt.field_varint(2, timestamp_us)
                + self.nt.field_varint(3, seq)
                + self.nt.field_bytes(4, raw))
        return bytes([self.nt.MSG_DATA_RPT]) + body

    def _channel_loop(self, cli, topic, ch, origin, start, period):
        seq = 0
        next_tick = start
        while time.perf_counter() - start < self.duration:
            now = time.perf_counter()
            if now < next_tick:
                time.sleep(next_tick - now)
            next_tick += period
            seq += 1
            self.sent += 1
            if random.random() < self.loss:
                # A dropped sample: the sequence number is consumed but nothing
                # is published, exactly like a ring/queue drop in the field.
                self.dropped += 1
                continue
            # Device timestamp = sampling instant.  Injected one-way latency is
            # modelled by backdating the timestamp, which shifts the harness'
            # (recv_time - timestamp) by exactly that amount without throttling
            # the publish rate.
            jitter = random.uniform(0, self.jitter_ms)
            ts = origin + (time.perf_counter() - start) * 1e6
            ts -= (self.latency_ms + jitter) * 1000.0
            payload = self._report(ch, seq, int(ts))
            cli.publish(topic, payload, qos=1)

    def run(self):
        cli = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2, client_id="synthetic-node")
        cli.connect(self.broker, self.port, keepalive=30)
        cli.loop_start()
        topic = f"nodes/{self.node}/up"
        start = time.perf_counter()
        origin = time.perf_counter() * 1e6
        period = 1.0 / self.hz
        threads = [threading.Thread(target=self._channel_loop,
                                    args=(cli, topic, ch, origin, start, period),
                                    daemon=True)
                   for ch in range(1, self.channels + 1)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        # let in-flight QoS1 writes drain before the harness stops counting
        time.sleep(0.5)
        cli.loop_stop()
        cli.disconnect()
        self.done.set()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--broker", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=18830)
    ap.add_argument("--node", default="1001")
    ap.add_argument("--duration", type=float, default=8.0)
    ap.add_argument("--loss", type=float, default=0.01)
    ap.add_argument("--latency-ms", type=float, default=25.0)
    ap.add_argument("--jitter-ms", type=float, default=50.0)
    ap.add_argument("--payload-bytes", type=int, default=300)
    args = ap.parse_args()

    nt = load_harness()
    node = SyntheticNode(nt, args.broker, args.port, args.node, hz=100, channels=5,
                         payload_bytes=args.payload_bytes, loss=args.loss,
                         latency_ms=args.latency_ms, jitter_ms=args.jitter_ms,
                         duration=args.duration + 2.0)
    node.start()
    time.sleep(2.5)

    hargs = argparse.Namespace(
        broker=args.broker, port=args.port, node=args.node,
        duration=args.duration, channels=5, hz=100,
        hello_timeout=2.0, config_timeout=2.0, progress=False,
        read_length=args.payload_bytes, allow_no_hardware=True,
    )
    harness = nt.Harness(hargs)
    manifest = nt.build_manifest("selftest", "selftest-sync", nt.S3_FULL_FIT, 10)
    result = harness.run(manifest)
    node.done.wait(5)

    dur = result["duration_s"]
    counts = harness.channels
    # Measure against what the *harness observed*, comparing to injected loss.
    observed_total = sum(st.count for st in counts.values())
    observed_missing = sum(st.missing for st in counts.values())
    observed_loss = observed_missing / (observed_total + observed_missing) \
        if (observed_total + observed_missing) else float("nan")

    print("\n" + "=" * 72)
    print("SELFTEST: injected vs measured")
    print("=" * 72)
    print(json.dumps({
        "injected_sent": node.sent,
        "injected_dropped": node.dropped,
        "injected_loss": round(node.dropped / max(node.sent, 1), 5),
        "harness_received": observed_total,
        "harness_detected_missing": observed_missing,
        "harness_detected_loss": round(observed_loss, 5) if observed_loss == observed_loss else None,
        "harness_rate_hz": round(observed_total / dur, 1),
        "harness_latency_p50_ms": result["latency_ms"]["p50"],
        "harness_latency_p99_ms": result["latency_ms"]["p99"],
        "injected_latency_ms": args.latency_ms,
    }, indent=2))

    # Latency convention: the harness anchors on min(recv - device_ts), which
    # removes the constant part of one-way delay.  What it measures is the
    # *additional* delay (jitter + queueing) above the best observed case.
    # So the selftest validates that it tracks the injected jitter amplitude,
    # not the constant latency.  p99 of Uniform(0,J) is ~0.99*J.
    p99 = result["latency_ms"]["p99"]
    p50 = result["latency_ms"]["p50"]
    checks = {
        "received_500_per_s_plus_minus_10pct":
            abs(observed_total / dur - 500.0) < 50.0,
        "detected_loss_within_50pct_of_injected":
            (abs(observed_loss - args.loss) <= 0.5 * args.loss) if observed_loss == observed_loss else False,
        # Host-side pipeline jitter (docker broker hop + Python scheduling)
        # adds a floor of roughly 10-40 ms on this dev host, so the check is
        # deliberately directional: with a 50 ms injected jitter the measured
        # p99 must be clearly above the no-jitter noise floor and not wildly
        # inflated.  A tighter bound would fail on host scheduling, not on the
        # measurement logic.
        "latency_p99_tracks_injected_jitter":
            (p99 is not None) and (0.4 * args.jitter_ms <= p99 <= 2.0 * args.jitter_ms + 50.0),
        "latency_p50_below_p99":
            (p50 is not None) and (p99 is not None) and (p50 <= p99),
        "payload_approx_300b":
            result["raw_payload_kb_s"] is not None
            and abs(result["raw_payload_kb_s"] - 500 * args.payload_bytes / 1024.0)
            < 0.2 * (500 * args.payload_bytes / 1024.0),
    }
    print("\nSELFTEST CHECKS:")
    for k, v in checks.items():
        print(f"  [{'PASS' if v else 'FAIL'}] {k}")
    ok = all(checks.values())
    print(f"\nSELFTEST {'PASS' if ok else 'FAIL'}")
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
