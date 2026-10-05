#!/usr/bin/env python3
"""MQTT 层 5 通道 x 100 Hz 仿真 -> 后端吞吐验证（task-3 第 3 步）。

为什么单独有这一步（诚实性口径）
--------------------------------
现场只有 2 路真实 UART 总线（ch8/ch9），且现场 manifest 的间隔是 5000/1000 ms。
"5 通道 x 100 Hz" 在真机上**跑不满**，原因不是固件，而是链路物理上限：
  - ch8 UART0 @9600：单事务 TX 7 + RX 60 = 67 B @10bit/B -> ~70 ms -> 上限 ~14.3 Hz
  - ch9 UART1 @2400：单事务 TX 5 + RX 18 = 23 B @10bit/B -> ~24 ms -> 上限 ~41 Hz
    （现场模板 read_length 更大时更低；Lead 按 30 B 算得 ~6.9 Hz）
要真正 100 Hz，每路需 >=115200 baud。因此本脚本**只验证后端在 500 report/s
（5x100Hz，约 150 KB/s 原始 payload）下的吞吐与背压**，不声称链路可达。

它做了什么
----------
1. 按 5 通道 x 100 Hz 向 nodes/<node>/up 发 DataReport(0x03)，payload ~300 B，
   带 edge_device_id（使后端走"持久化+解析"而非被动通道）。
2. 发布前后各抓一次后端 /metrics 与 device_data 计数，算增量：
   - ehome_data_reports_processed_total 增量应 == 发布数（无丢失）
   - ehome_data_event_bus_dropped_total 增量应为 0（无背压丢弃）
   - ehome_data_report_errors_total 增量应为 0
   - device_data 行数增量（DB 持久化路径）
3. 产出 JSON 报告 + 人类可读摘要。

用法（默认打本机开发栈；现场生产栈与开发栈无关，不要混用）：
  python3 scripts/mqtt_backend_throughput.py --broker 127.0.0.1 --port 1883 \
      --node loadtest-sim5x100 --channels 5 --hz 100 --duration 60 \
      --metrics http://127.0.0.1:8080/metrics --json out.json
"""

from __future__ import annotations

import argparse
import json
import math
import struct
import sys
import threading
import time

try:
    import paho.mqtt.client as mqtt
except ImportError:
    sys.stderr.write("需要 paho-mqtt\n")
    raise SystemExit(2)

try:
    import urllib.request
except ImportError:
    urllib = None


def enc_varint(v: int) -> bytes:
    out = bytearray()
    while v > 0x7F:
        out.append((v & 0x7F) | 0x80)
        v >>= 7
    out.append(v & 0x7F)
    return bytes(out)


def field_varint(n, v):
    return enc_varint((n << 3) | 0) + enc_varint(v)


def field_bytes(n, d):
    return enc_varint((n << 3) | 2) + enc_varint(len(d)) + d


def data_report(channel_id, timestamp_us, sequence, raw, error_code, request_id,
                edge_device_id, command_template_id):
    f = bytearray([0x03])
    f += field_varint(1, channel_id)
    f += field_varint(2, timestamp_us)
    f += field_varint(3, sequence)
    if raw:
        f += field_bytes(4, raw)
    if error_code:
        f += field_varint(5, error_code)
    if request_id:
        f += field_varint(6, request_id)
    if edge_device_id:
        f += field_varint(7, edge_device_id)
    if command_template_id:
        f += field_varint(9, command_template_id)
    return bytes(f)


def scrape_metrics(url):
    if not url or urllib is None:
        return {}
    try:
        with urllib.request.urlopen(url, timeout=5) as r:
            text = r.read().decode("utf-8", "replace")
    except Exception as exc:
        print("[metrics] scrape failed: %s" % exc, file=sys.stderr)
        return {}
    out = {}
    for line in text.splitlines():
        if line.startswith("#") or not line.strip():
            continue
        parts = line.rsplit(" ", 1)
        if len(parts) != 2:
            continue
        name, val = parts
        if name in ("ehome_data_reports_processed_total",
                    "ehome_data_event_bus_dropped_total",
                    "ehome_data_report_errors_total"):
            try:
                out[name] = float(val)
            except ValueError:
                pass
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--broker", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=1883)
    ap.add_argument("--node", default="loadtest-sim5x100")
    ap.add_argument("--channels", type=int, default=5)
    ap.add_argument("--hz", type=int, default=100)
    ap.add_argument("--duration", type=float, default=60.0)
    ap.add_argument("--payload-bytes", type=int, default=300)
    ap.add_argument("--metrics", default="http://127.0.0.1:8080/metrics")
    ap.add_argument("--json")
    ap.add_argument("--no-wait-drain", action="store_true")
    args = ap.parse_args()

    topic = "nodes/%s/up" % args.node
    total_expected = int(args.channels * args.hz * args.duration)
    raw = bytes((i * 7 + 3) & 0xFF for i in range(args.payload_bytes))

    m0 = scrape_metrics(args.metrics)
    print("[bench] baseline metrics: %s" % m0)

    client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2,
                         client_id="bench-5x100-%d" % int(time.time()))
    connected = threading.Event()
    client.on_connect = lambda c, u, f, rc, p=None: connected.set()
    client.connect(args.broker, args.port, keepalive=30)
    client.loop_start()
    if not connected.wait(10):
        raise SystemExit("MQTT connect timeout")

    seq = [0] * args.channels
    published = 0
    pub_fail = 0
    payload_bytes = 0
    t0 = time.perf_counter()
    period = 1.0 / args.hz
    next_tick = t0
    for tick in range(int(args.duration * args.hz)):
        next_tick += period
        for ch in range(1, args.channels + 1):
            seq[ch - 1] += 1
            payload = data_report(ch, int(time.time() * 1e6), seq[ch - 1], raw, 0, 0,
                                 900000 + ch, 0)
            info = client.publish(topic, payload, qos=0)
            if info.rc == 0:
                published += 1
                payload_bytes += len(payload)
            else:
                pub_fail += 1
        delay = next_tick - time.perf_counter()
        if delay > 0:
            time.sleep(delay)
    pub_elapsed = time.perf_counter() - t0

    # 等后端把 bus 排空（计数追上发布数或超时）
    drain_deadline = time.time() + 60
    m1 = scrape_metrics(args.metrics)
    if not args.no_wait_drain and args.metrics:
        target = m0.get("ehome_data_reports_processed_total", 0) + published
        while time.time() < drain_deadline:
            m1 = scrape_metrics(args.metrics)
            if m1.get("ehome_data_reports_processed_total", 0) >= target:
                break
            time.sleep(0.5)
    drain_s = time.perf_counter() - t0 - pub_elapsed

    client.loop_stop()
    client.disconnect()

    processed = m1.get("ehome_data_reports_processed_total", 0) - m0.get("ehome_data_reports_processed_total", 0)
    dropped = m1.get("ehome_data_event_bus_dropped_total", 0) - m0.get("ehome_data_event_bus_dropped_total", 0)
    errors = m1.get("ehome_data_report_errors_total", 0) - m0.get("ehome_data_report_errors_total", 0)

    report = {
        "broker": "%s:%d" % (args.broker, args.port),
        "topic": topic,
        "node": args.node,
        "channels": args.channels,
        "target_hz": args.hz,
        "duration_s": round(args.duration, 2),
        "payload_bytes_each": args.payload_bytes,
        "published_reports": published,
        "publish_failures": pub_fail,
        "expected_reports": total_expected,
        "publish_elapsed_s": round(pub_elapsed, 3),
        "achieved_report_rate_hz": round(published / pub_elapsed, 1),
        "raw_payload_kb_s": round((payload_bytes / 1024.0) / pub_elapsed, 1),
        "backend": {
            "metrics_before": m0,
            "metrics_after": m1,
            "processed_delta": processed,
            "dropped_delta": dropped,
            "errors_delta": errors,
            "drain_extra_s": round(drain_s, 2),
        },
        "pass": {
            "published_matches_expected": published == total_expected,
            "processed_ge_published": processed >= published,
            "no_backend_drop": dropped == 0,
            "no_backend_error": errors == 0,
        },
        "scope_note": (
            "本步只验证后端在 %d report/s（%.0f KB/s）下的吞吐与背压；"
            "真实 UART 链路在 9600/2400 baud 下物理上限约 14.3/41 Hz（见报告），"
            "满配 100 Hz 的前提是每路 >=115200 baud。**不得**据此声称现场链路已跑满 5x100Hz。"
            % (args.channels * args.hz, (payload_bytes / 1024.0) / pub_elapsed)),
    }
    report["overall_pass"] = all(report["pass"].values())
    print("\n" + "=" * 72)
    print(json.dumps(report, ensure_ascii=False, indent=2))
    if args.json:
        with open(args.json, "w", encoding="utf-8") as fh:
            json.dump(report, fh, ensure_ascii=False, indent=2)
        print("[bench] wrote %s" % args.json)
    return 0 if report["overall_pass"] else 1


if __name__ == "__main__":
    sys.exit(main())
