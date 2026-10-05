#!/usr/bin/env python3
"""实机 UART soak 取证：并行抓「S3 控制台」+「生产 MQTT 上行」，产出 JSON 报告。

设计要点（为什么不是一个脚本读串口就完事）：
  1. 现场设备连的是**生产栈** broker 192.168.20.6:1883（本机开发栈与现场无关），
     所以上行证据必须从生产 broker 订阅。
  2. S3 的控制台走原生 USB-JTAG，复位会重枚举（ttyACM 号会变），
     因此这里按 by-id 传参并在读失败时重开端口（复用 serial_boot_capture.py 的做法，
     但**不主动复位**——本脚本是纯观测，避免干扰被测对象）。
  3. report_drop / largest / PANIC 只能从设备控制台拿到；p99 / 每通道速率只能从
     MQTT 上行拿到。两者时间戳不同源，因此各自记录、报告中分别标注口径。

口径（报告里必须带）：
  - report_drop 取自控制台 'RX_TASK: Stats: ... report_drop=N' 的**最后一次**采样；
  - largest 取自 MemReport（0x20 或 0x21，按实抓到的号判断）；
  - p99 用 min(recv_ts - device_ts) 去常量时钟偏移，是「抖动+排队」而非绝对传播时延；
  - 通道速率用 DataReport 计数 / soak 时长。

用法：
  python3 scripts/uart_real_soak.py \
    --node 30EDA0A9A808 --broker 192.168.20.6 --port 1883 \
    --console /dev/serial/by-id/usb-Espressif_USB_JTAG_serial_debug_unit_30:ED:A0:A9:A8:08-if00 \
    --duration 300 --outdir docs/验证/证据/2026-10-05-实机压测
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import sys
import threading
import time

import serial

try:
    import paho.mqtt.client as mqtt
except ImportError:
    sys.stderr.write("需要 paho-mqtt：用 IDF python env 或 pip install paho-mqtt\n")
    raise SystemExit(2)

MSG_HELLO = 0x01
MSG_STATUS_RPT = 0x02
MSG_DATA_RPT = 0x03
MSG_CONFIG_RSLT = 0x05
MSG_MEM_RPT_V20 = 0x20
MSG_MEM_RPT_V21 = 0x21
MSG_LOG_STREAM = 0x1D

MEM_FIELDS = ["free_bytes", "largest_bytes", "min_ever_bytes",
              "min_task_stack_high_water_bytes", "reserve_floor_bytes"]


# ---------------------------------------------------------------- TLV decode
def dec_varint(buf, pos):
    value = 0
    shift = 0
    while True:
        if pos >= len(buf):
            raise ValueError("truncated varint")
        b = buf[pos]
        pos += 1
        value |= (b & 0x7F) << shift
        if not (b & 0x80):
            return value, pos
        shift += 7


def decode_fields(payload):
    pos = 0
    while pos < len(payload):
        tag, pos = dec_varint(payload, pos)
        fnum, wire = tag >> 3, tag & 7
        if wire == 0:
            val, pos = dec_varint(payload, pos)
        elif wire == 2:
            ln, pos = dec_varint(payload, pos)
            val = payload[pos:pos + ln]
            pos += ln
        else:
            return
        yield fnum, wire, val


def percentile(values, pct):
    if not values:
        return float("nan")
    ordered = sorted(values)
    rank = (pct / 100.0) * (len(ordered) - 1)
    lo = int(math.floor(rank))
    hi = int(math.ceil(rank))
    if lo == hi:
        return ordered[lo]
    return ordered[lo] + (ordered[hi] - ordered[lo]) * (rank - lo)


# ---------------------------------------------------------------- console
CONSOLE_PATTERNS = {
    "report_drop": re.compile(r"report_drop=(\d+)"),
    "rx_timeout": re.compile(r"RX timeout slot(\d) type=(\d+) reqID=(\d+)"),
    "cmd_u0_txn": re.compile(r"CMD_U0: Stats: txn=(\d+) err=(\d+)"),
    "cmd_u1_txn": re.compile(r"CMD_U1: Stats: txn=(\d+) err=(\d+)"),
    "sched_stats": re.compile(r"SCHEDULER: Stats: samples=(\d+) full=(\d+) min_free=(\d+)"),
    "panic": re.compile(r"(Guru Meditation|panic|abort\(\)|assert failed|Backtrace:|WDT|task_wdt)", re.I),
    "heap": re.compile(r"\[heap\]\s+(\S+)\s+(\S+)\s+free=(\d+) largest=(\d+)"),
    "bootheap": re.compile(r"\[bootheap\]\s+(\S+)\s+free=(\d+) largest=(\d+) min_ever=(\d+)"),
}


class ConsoleReader(threading.Thread):
    """被动读控制台，端口读失败时按 by-id 重开（不复位）。"""

    def __init__(self, port, out_path, stop_evt):
        super().__init__(daemon=True)
        self.port = port
        self.out_path = out_path
        self.stop_evt = stop_evt
        self.lines = []
        self.panics = []
        self.raw = bytearray()
        self.reopens = 0
        self.lock = threading.Lock()

    def _open(self):
        for _ in range(40):
            if self.stop_evt.is_set():
                return None
            try:
                s = serial.Serial(self.port, 115200, timeout=0.2)
                time.sleep(0.2)
                return s
            except Exception:
                time.sleep(0.25)
        return None

    def run(self):
        s = self._open()
        if s is None:
            return
        buf = bytearray()
        while not self.stop_evt.is_set():
            try:
                d = s.read(8192)
            except Exception:
                try:
                    s.close()
                except Exception:
                    pass
                s = self._open()
                self.reopens += 1
                if s is None:
                    return
                continue
            if not d:
                continue
            with self.lock:
                self.raw += d
            buf += d
            while b"\n" in buf:
                idx = buf.index(b"\n")
                line = bytes(buf[:idx]).decode("utf-8", "replace").rstrip("\r")
                del buf[:idx + 1]
                self._on_line(line)
        try:
            s.close()
        except Exception:
            pass
        with open(self.out_path, "wb") as fh:
            fh.write(bytes(self.raw))

    def _on_line(self, line):
        with self.lock:
            self.lines.append((round(time.time(), 3), line))
            if CONSOLE_PATTERNS["panic"].search(line):
                self.panics.append(line)

    def snapshot(self):
        with self.lock:
            return list(self.lines), list(self.panics), self.reopens


# ---------------------------------------------------------------- MQTT
class UpCollector:
    def __init__(self, node, broker, port):
        self.node = node
        self.broker = broker
        self.port = port
        self.connected = False
        self.disconnects = 0
        self.connected_at = None
        self.stopping = False
        self.lock = threading.Lock()
        self.records = []       # (recv_us, mtype, payload)
        self.mem_reports = []
        self.data_reports = []  # (recv_us, channel, ts, seq, raw_len, error_code)
        self.log_lines = []
        self.config_results = []
        self.hello_ack = 0
        self.status = 0
        self.mem_msg_type = None
        self.client = None

    def on_connect(self, client, userdata, flags, reason_code, properties=None):
        rc = getattr(reason_code, "value", reason_code)
        if rc != 0:
            return
        self.connected = True
        self.connected_at = time.time()
        client.subscribe(f"nodes/{self.node}/up", qos=0)
        print(f"[mqtt] connected {self.broker}:{self.port}, subscribed nodes/{self.node}/up", flush=True)

    def on_disconnect(self, *a):
        if self.connected and not self.stopping:
            self.disconnects += 1
            print("[mqtt] !! disconnected", flush=True)

    def on_message(self, client, userdata, msg):
        payload = msg.payload
        if not payload:
            return
        mtype = payload[0]
        body = payload[1:]
        recv_us = time.perf_counter() * 1e6
        with self.lock:
            self.records.append((recv_us, mtype, bytes(payload)))
            if mtype == MSG_DATA_RPT:
                self._data(body, recv_us)
            elif mtype in (MSG_MEM_RPT_V20, MSG_MEM_RPT_V21):
                self.mem_msg_type = mtype
                self._mem(body, recv_us)
            elif mtype == MSG_LOG_STREAM:
                self._log(body)
            elif mtype == MSG_CONFIG_RSLT:
                self._cfg(body)
            elif mtype == 0x12:
                self.hello_ack += 1
            elif mtype == MSG_STATUS_RPT:
                self.status += 1

    def _data(self, body, recv_us):
        ch = ts = seq = 0
        raw_len = 0
        err = 0
        for fnum, wire, val in decode_fields(body):
            if fnum == 1:
                ch = val
            elif fnum == 2:
                ts = val
            elif fnum == 3:
                seq = val
            elif fnum == 4:
                raw_len = len(val)
            elif fnum == 5:
                err = val
        self.data_reports.append((recv_us, ch, ts, seq, raw_len, err))

    def _mem(self, body, recv_us):
        vals = {}
        for fnum, wire, val in decode_fields(body):
            if 1 <= fnum <= 5 and wire == 0:
                vals[MEM_FIELDS[fnum - 1]] = val
        self.mem_reports.append((recv_us, vals))

    def _log(self, body):
        text = None
        for fnum, wire, val in decode_fields(body):
            if wire == 2 and fnum in (2, 3):
                try:
                    text = bytes(val).decode("utf-8", "replace")
                except Exception:
                    text = None
        if text:
            self.log_lines.append(text)

    def _cfg(self, body):
        rec = {}
        for fnum, wire, val in decode_fields(body):
            if fnum == 1 and wire == 2:
                rec["manifest_id"] = bytes(val).decode(errors="replace")
            elif fnum == 2 and wire == 0:
                rec["success"] = bool(val)
            elif fnum == 4 and wire == 2:
                rec["sync_id"] = bytes(val).decode(errors="replace")
        self.config_results.append(rec)

    def run(self, stop_evt):
        self.client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2,
                                  client_id=f"loadtest-soak-{int(time.time())}")
        self.client.on_connect = self.on_connect
        self.client.on_disconnect = self.on_disconnect
        self.client.on_message = self.on_message
        self.client.connect(self.broker, self.port, keepalive=30)
        self.client.loop_start()
        deadline = time.time() + 15
        while not self.connected and time.time() < deadline and not stop_evt.is_set():
            time.sleep(0.1)
        stop_evt.wait()
        self.stopping = True
        try:
            self.client.disconnect()
            self.client.loop_stop()
        except Exception:
            pass


# ---------------------------------------------------------------- main
def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--node", required=True)
    ap.add_argument("--broker", default="192.168.20.6")
    ap.add_argument("--port", type=int, default=1883)
    ap.add_argument("--console", required=True, help="S3 控制台 by-id 路径")
    ap.add_argument("--duration", type=float, default=300.0)
    ap.add_argument("--outdir", required=True)
    ap.add_argument("--tag", default="step1-baseline")
    ap.add_argument("--no-console", action="store_true")
    args = ap.parse_args()

    os.makedirs(args.outdir, exist_ok=True)
    stop_evt = threading.Event()

    console = None
    if not args.no_console:
        console = ConsoleReader(args.console,
                                os.path.join(args.outdir, f"{args.tag}-console.log"),
                                stop_evt)
        console.start()
    collector = UpCollector(args.node, args.broker, args.port)
    mqtt_thread = threading.Thread(target=collector.run, args=(stop_evt,), daemon=True)
    mqtt_thread.start()

    t0 = time.time()
    print(f"[soak] {args.tag}: node={args.node} duration={args.duration}s ...", flush=True)
    try:
        while time.time() - t0 < args.duration:
            time.sleep(5)
            el = time.time() - t0
            print(f"[soak] t={el:6.1f}s data={len(collector.data_reports)} "
                  f"mem={len(collector.mem_reports)} disc={collector.disconnects}", flush=True)
    except KeyboardInterrupt:
        pass
    stop_evt.set()
    mqtt_thread.join(timeout=10)
    if console:
        console.join(timeout=5)

    elapsed = time.time() - t0
    report = analyze(args, collector, console, elapsed)
    jpath = os.path.join(args.outdir, f"{args.tag}-report.json")
    with open(jpath, "w", encoding="utf-8") as fh:
        json.dump(report, fh, ensure_ascii=False, indent=2)
    print("\n" + "=" * 72)
    print(json.dumps(report, ensure_ascii=False, indent=2))
    print(f"[soak] wrote {jpath}")
    return 0


def analyze(args, collector, console, elapsed):
    data = collector.data_reports
    per_ch = {}
    for recv_us, ch, ts, seq, raw_len, err in data:
        st = per_ch.setdefault(ch, {"count": 0, "errors": 0, "payload_bytes": 0,
                                    "seq_gaps": 0, "missing": 0, "last_seq": None,
                                    "samples": []})
        st["count"] += 1
        if err:
            st["errors"] += 1
        st["payload_bytes"] += raw_len
        if st["last_seq"] is not None:
            gap = (seq - st["last_seq"]) & 0xFFFFFFFF
            if gap > 1:
                st["seq_gaps"] += 1
                st["missing"] += gap - 1
        st["last_seq"] = seq
        st["samples"].append((recv_us, ts, raw_len, err))

    deltas = [(r - ts) for (r, ch, ts, seq, rl, e) in data]
    offset = min(deltas) if deltas else 0.0
    lat = [(d - offset) / 1000.0 for d in deltas]

    per_ch_out = {}
    for ch, st in sorted(per_ch.items()):
        per_ch_out[str(ch)] = {
            "count": st["count"],
            "observed_hz": round(st["count"] / elapsed, 3),
            "error_reports": st["errors"],
            "seq_gaps": st["seq_gaps"],
            "missing_reports": st["missing"],
            "payload_bytes": st["payload_bytes"],
            "avg_payload_len": round(st["payload_bytes"] / st["count"], 1) if st["count"] else 0,
        }

    mem = collector.mem_reports[-1][1] if collector.mem_reports else None

    console_lines, panics, reopens = (console.snapshot() if console else ([], [], 0))
    drop_samples = []
    u0 = u1 = None
    sched = None
    for _, line in console_lines:
        m = CONSOLE_PATTERNS["report_drop"].search(line)
        if m:
            drop_samples.append(int(m.group(1)))
        m = CONSOLE_PATTERNS["cmd_u0_txn"].search(line)
        if m:
            u0 = {"txn": int(m.group(1)), "err": int(m.group(2))}
        m = CONSOLE_PATTERNS["cmd_u1_txn"].search(line)
        if m:
            u1 = {"txn": int(m.group(1)), "err": int(m.group(2))}
        m = CONSOLE_PATTERNS["sched_stats"].search(line)
        if m:
            sched = {"samples": int(m.group(1)), "full": int(m.group(2)),
                     "min_free": int(m.group(3))}

    return {
        "tag": args.tag,
        "node": args.node,
        "broker": f"{args.broker}:{args.port}",
        "duration_s": round(elapsed, 2),
        "console": {
            "port": args.console if not args.no_console else None,
            "reopens": reopens,
            "lines": len(console_lines),
            "report_drop_samples": drop_samples[-10:],
            "report_drop_last": drop_samples[-1] if drop_samples else None,
            "report_drop_max": max(drop_samples) if drop_samples else None,
            "cmd_u0": u0,
            "cmd_u1": u1,
            "scheduler": sched,
            "panic_or_wdt_lines": panics[:20],
            "panic_count": len(panics),
        },
        "mqtt": {
            "connected": collector.connected,
            "disconnects": collector.disconnects,
            "total_messages": len(collector.records),
            "data_reports": len(data),
            "status_reports": collector.status,
            "hello_acks": collector.hello_ack,
            "log_stream_lines": len(collector.log_lines),
            "mem_report_msg_type": (hex(collector.mem_msg_type)
                                    if collector.mem_msg_type else None),
            "mem_report_count": len(collector.mem_reports),
            "config_results": collector.config_results,
        },
        "mem_report_last": mem,
        "mem_report_note": (
            "S3(s3p) 的 free/largest 来自 heap_caps_get_largest_free_block(MALLOC_CAP_8BIT)，"
            "在 SPIRAM_USE_MALLOC 下会取到 PSRAM 的 8MB 连续块 —— 这是『PSRAM 总量』，"
            "**不是内部 RAM 水位**，不能作为 16KiB floor 达标证据（见 Lead 缺陷报告）。"
            if mem and mem.get("largest_bytes", 0) > 4 * 1024 * 1024 else
            "largest 为内部 RAM 量级，可作为 floor 判据参考"),
        "per_channel": per_ch_out,
        "latency_ms": {
            "p50": round(percentile(lat, 50), 2) if lat else None,
            "p99": round(percentile(lat, 99), 2) if lat else None,
            "p99_9": round(percentile(lat, 99.9), 2) if lat else None,
            "max": round(max(lat), 2) if lat else None,
            "samples": len(lat),
            "convention": "相对最小观测偏移的抖动+排队，不是绝对传播时延",
        },
    }


if __name__ == "__main__":
    sys.exit(main())
