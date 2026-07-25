#!/usr/bin/env python3
"""C6 UART0 external-device and receive-path validation.

This program deliberately does *not* flash the C6, publish a manifest, or
write any configuration.  It treats ``/dev/ttyUSB0`` as the external device
connected to the C6 UART0 pins and ``/dev/ttyACM0`` as a read-only C6 console.

The C6 must already have a UART0 channel configured if a request/response
exchange is expected.  The program remains useful with a passive channel: it
can inject deterministic bytes, act as an echo/fixed-response peripheral,
record the observed C6 TX stream, and report UART error/overflow/timeout lines
from the console.  It never labels a run as lossless merely because the host
write() call accepted bytes; a transport-level receive count requires a C6
DataReport/metrics source outside the serial console.

Examples:

  # Observe an already configured UART0 channel and echo requests back.
  python3 scripts/c6_uart0_serial_validation.py --mode smoke --echo

  # Send 1 Mbps deterministic input for 30 seconds, with 50 ms frame gaps.
  python3 scripts/c6_uart0_serial_validation.py --mode continuous \
      --baud 1000000 --duration 30 --idle-ms 50 --require-traffic

  # Run all serial-only cases and save machine-readable evidence.
  python3 scripts/c6_uart0_serial_validation.py --mode all \
      --baud 1000000 --duration 30 --echo --json /tmp/c6-uart0.json
"""

from __future__ import annotations

import argparse
import binascii
import json
import os
import struct
import sys
import threading
import time
import re
from dataclasses import asdict, dataclass, field
from typing import Optional

try:
    import serial
except ImportError as exc:  # pragma: no cover - exercised on target host
    raise SystemExit("pyserial is required: python3 -m pip install pyserial") from exc


DEFAULT_USB = "/dev/ttyUSB0"
DEFAULT_ACM = "/dev/ttyACM0"
DEFAULT_CONSOLE_BAUD = 115200
DEFAULT_BAUD = 1_000_000
FRAME_MAGIC = b"ECV1"


@dataclass
class ConsoleStats:
    bytes: int = 0
    lines: int = 0
    rx_overflow: int = 0
    uart_error: int = 0
    rx_timeout: int = 0
    event_lines: int = 0
    report_lines: int = 0
    rx_hits: int = 0
    rx_events: int = 0
    text_tail: list[str] = field(default_factory=list)


@dataclass
class CaseResult:
    name: str
    status: str
    duration_s: float
    tx_bytes: int = 0
    tx_frames: int = 0
    observed_usb_bytes: int = 0
    observed_usb_chunks: int = 0
    crc32: str = ""
    console: Optional[dict] = None
    note: str = ""


class C6SerialProbe:
    """Own both ports and keep console reading independent from USB writes."""

    def __init__(self, usb_port: str, acm_port: str, baud: int,
                 console_baud: int, echo: bool, response: bytes,
                 read_timeout: float = 0.05):
        self.usb_port_name = usb_port
        self.acm_port_name = acm_port
        self.baud = baud
        self.console_baud = console_baud
        self.echo = echo
        self.response = response
        self.read_timeout = read_timeout
        self.usb: Optional[serial.Serial] = None
        self.acm: Optional[serial.Serial] = None
        self.stop = threading.Event()
        self.console_thread: Optional[threading.Thread] = None
        self.peripheral_thread: Optional[threading.Thread] = None
        self.console_stats = ConsoleStats()
        self.usb_observed_bytes = 0
        self.usb_observed_chunks = 0
        self._lock = threading.Lock()

    def open(self) -> None:
        # Do not use exclusive=True: some C6 USB/JTAG drivers expose a shared
        # console endpoint, and opening it read-only is the least invasive
        # observation possible.
        self.usb = serial.Serial(
            self.usb_port_name,
            baudrate=self.baud,
            timeout=self.read_timeout,
            write_timeout=5.0,
        )
        self.acm = serial.Serial(
            self.acm_port_name,
            baudrate=self.console_baud,
            timeout=self.read_timeout,
            write_timeout=1.0,
        )
        self.usb.reset_input_buffer()
        self.usb.reset_output_buffer()
        self.acm.reset_input_buffer()
        self.console_thread = threading.Thread(
            target=self._console_loop, name="c6-console", daemon=True
        )
        self.peripheral_thread = threading.Thread(
            target=self._peripheral_loop, name="uart0-peripheral", daemon=True
        )
        self.console_thread.start()
        self.peripheral_thread.start()

    def close(self) -> None:
        self.stop.set()
        for thread in (self.console_thread, self.peripheral_thread):
            if thread:
                thread.join(timeout=1.0)
        for port in (self.usb, self.acm):
            if port and port.is_open:
                port.close()

    def _console_loop(self) -> None:
        assert self.acm is not None
        pending = bytearray()
        while not self.stop.is_set():
            try:
                chunk = self.acm.read(1024)
            except serial.SerialException:
                break
            if not chunk:
                continue
            self.console_stats.bytes += len(chunk)
            pending.extend(chunk)
            while b"\n" in pending:
                line, _, remainder = pending.partition(b"\n")
                pending = bytearray(remainder)
                text = line.decode("utf-8", errors="replace").strip()
                if not text:
                    continue
                self._record_console_line(text)
        if pending:
            self._record_console_line(pending.decode("utf-8", errors="replace"))

    def _record_console_line(self, text: str) -> None:
        stats = self.console_stats
        stats.lines += 1
        lower = text.lower()
        if "rx overflow" in lower or "fifo_ovf" in lower or "buffer_full" in lower:
            stats.rx_overflow += 1
        if "uart" in lower and ("event=" in lower or "parity" in lower or
                                 "frame_err" in lower or "break" in lower):
            stats.uart_error += 1
        if "rx timeout" in lower:
            stats.rx_timeout += 1
        if "uart_data" in lower or "rx_task" in lower:
            stats.event_lines += 1
        hit_match = re.search(r"\bhits=(\d+)", lower)
        if hit_match:
            stats.rx_hits += int(hit_match.group(1))
        event_match = re.search(r"\bevents=(\d+)", lower)
        if event_match:
            stats.rx_events += int(event_match.group(1))
        if "datareport" in lower or "report" in lower:
            stats.report_lines += 1
        stats.text_tail.append(text)
        del stats.text_tail[:-20]

    def _peripheral_loop(self) -> None:
        """Act as a deliberately simple UART0 device.

        Requests are not parsed because this is a bus transport test, not a
        device protocol test.  Echoing the bytes (or returning a fixed frame)
        gives an already configured C6 pending command a deterministic reply.
        """
        assert self.usb is not None
        while not self.stop.is_set():
            try:
                chunk = self.usb.read(4096)
            except serial.SerialException:
                break
            if not chunk:
                continue
            with self._lock:
                self.usb_observed_bytes += len(chunk)
                self.usb_observed_chunks += 1
            if not self.echo and not self.response:
                continue
            reply = chunk if self.echo else self.response
            try:
                self.usb.write(reply)
                self.usb.flush()
            except serial.SerialException:
                break

    @staticmethod
    def make_frame(sequence: int, payload_size: int) -> bytes:
        payload = bytes((sequence + i) & 0xFF for i in range(payload_size))
        header = FRAME_MAGIC + struct.pack(">I H", sequence, payload_size)
        crc = binascii.crc32(header + payload) & 0xFFFFFFFF
        return header + payload + struct.pack(">I", crc)

    def send_frames(self, count: int, payload_size: int,
                    inter_frame_idle_ms: float, continuous: bool) -> tuple[int, int, int]:
        assert self.usb is not None
        tx_bytes = 0
        tx_frames = 0
        crc = 0
        for sequence in range(count):
            frame = self.make_frame(sequence, payload_size)
            self.usb.write(frame)
            tx_bytes += len(frame)
            tx_frames += 1
            crc = binascii.crc32(frame, crc) & 0xFFFFFFFF
            if not continuous and inter_frame_idle_ms > 0:
                self.usb.flush()
                time.sleep(inter_frame_idle_ms / 1000.0)
        self.usb.flush()
        return tx_bytes, tx_frames, crc


def console_dict(stats: ConsoleStats) -> dict:
    return asdict(stats)


def run_case(probe: C6SerialProbe, name: str, fn, require_traffic: bool) -> CaseResult:
    before_lines = probe.console_stats.lines
    before_overflow = probe.console_stats.rx_overflow
    before_error = probe.console_stats.uart_error
    before_timeout = probe.console_stats.rx_timeout
    before_rx_hits = probe.console_stats.rx_hits
    before_rx_events = probe.console_stats.rx_events
    before_usb = probe.usb_observed_bytes
    before_usb_chunks = probe.usb_observed_chunks
    started = time.monotonic()
    tx_bytes = tx_frames = crc = 0
    note = ""
    try:
        tx_bytes, tx_frames, crc, note = fn()
        status = "pass"
    except Exception as exc:  # serial failures are reported per case
        status = "fail"
        note = f"{type(exc).__name__}: {exc}"
    duration = time.monotonic() - started
    console = console_dict(probe.console_stats)
    case_lines = probe.console_stats.lines - before_lines
    case_overflow = probe.console_stats.rx_overflow - before_overflow
    case_error = probe.console_stats.uart_error - before_error
    case_timeout = probe.console_stats.rx_timeout - before_timeout
    usb_bytes = probe.usb_observed_bytes - before_usb
    if status == "pass" and (case_overflow or case_error or case_timeout):
        status = "fail"
        note = (f"C6 console errors: overflow={case_overflow}, "
                f"uart_error={case_error}, timeout={case_timeout}")
    case_hits = probe.console_stats.rx_hits - before_rx_hits
    case_events = probe.console_stats.rx_events - before_rx_events
    if status == "pass" and require_traffic and not (usb_bytes or case_hits or case_events):
        status = "inconclusive"
        note = "未观察到 C6 UART0 外设请求或 RX hits；请确认 UART0 channel 已配置并有流量"
    if not note:
        note = f"console_lines={case_lines}, observed_c6_tx={usb_bytes}B"
    return CaseResult(
        name=name,
        status=status,
        duration_s=round(duration, 3),
        tx_bytes=tx_bytes,
        tx_frames=tx_frames,
        observed_usb_bytes=usb_bytes,
        observed_usb_chunks=probe.usb_observed_chunks - before_usb_chunks,
        crc32=f"{crc:08x}" if tx_frames else "",
        console=console,
        note=note,
    )


def parse_hex(text: str) -> bytes:
    cleaned = text.replace(" ", "").replace(":", "").replace("-", "")
    if len(cleaned) % 2:
        raise argparse.ArgumentTypeError("hex response must contain complete bytes")
    try:
        return bytes.fromhex(cleaned)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(str(exc)) from exc


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--mode", choices=("smoke", "frames", "continuous", "all"), default="all")
    parser.add_argument("--usb-port", default=DEFAULT_USB)
    parser.add_argument("--acm-port", default=DEFAULT_ACM)
    parser.add_argument("--baud", type=int, default=DEFAULT_BAUD)
    parser.add_argument("--console-baud", type=int, default=DEFAULT_CONSOLE_BAUD)
    parser.add_argument("--duration", type=float, default=30.0,
                        help="continuous case duration in seconds (default: 30)")
    parser.add_argument("--payload-size", type=int, default=512)
    parser.add_argument("--frame-count", type=int, default=10)
    parser.add_argument("--idle-ms", type=float, default=50.0,
                        help="idle between framed cases; use 0 for continuous fill")
    parser.add_argument("--echo", action="store_true",
                        help="echo every C6 UART0 request back as the peripheral reply")
    parser.add_argument("--response-hex", type=parse_hex, default=b"",
                        help="fixed peripheral response instead of echo, e.g. 5a0102")
    parser.add_argument("--require-traffic", action="store_true",
                        help="mark a case inconclusive if no C6/UART traffic is observed")
    parser.add_argument("--json", metavar="PATH", help="write JSON evidence to PATH")
    return parser


def main(argv: Optional[list[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    if args.baud <= 0 or args.payload_size <= 0 or args.frame_count <= 0:
        raise SystemExit("baud, payload-size and frame-count must be positive")
    if args.duration <= 0 or args.idle_ms < 0:
        raise SystemExit("duration must be positive and idle-ms cannot be negative")
    if args.echo and args.response_hex:
        raise SystemExit("--echo and --response-hex are mutually exclusive")

    probe = C6SerialProbe(
        args.usb_port, args.acm_port, args.baud, args.console_baud,
        args.echo, args.response_hex,
    )
    results: list[CaseResult] = []
    started = time.time()
    try:
        probe.open()
        print(f"opened USB peripheral {args.usb_port} @ {args.baud}")
        print(f"opened read-only C6 console {args.acm_port} @ {args.console_baud}")

        if args.mode in ("smoke", "all"):
            def smoke_case():
                # Passive observation window.  Any C6 request is handled by
                # the peripheral thread; this sleep is intentionally bounded.
                time.sleep(min(args.duration, 5.0))
                return 0, 0, 0, "" 
            results.append(run_case(probe, "smoke_observe_and_respond", smoke_case,
                                    args.require_traffic))

        if args.mode in ("frames", "all"):
            def frames_case():
                tx, frames, crc = probe.send_frames(
                    args.frame_count, args.payload_size, args.idle_ms, continuous=False
                )
                return tx, frames, crc, f"idle_ms={args.idle_ms}"
            results.append(run_case(probe, "framed_idle_boundary", frames_case,
                                    args.require_traffic))

        if args.mode in ("continuous", "all"):
            def continuous_case():
                end = time.monotonic() + args.duration
                tx = frames = crc = 0
                sequence = 0
                assert probe.usb is not None
                while time.monotonic() < end:
                    frame = probe.make_frame(sequence, args.payload_size)
                    probe.usb.write(frame)
                    tx += len(frame)
                    frames += 1
                    crc = binascii.crc32(frame, crc) & 0xFFFFFFFF
                    sequence += 1
                probe.usb.flush()
                return tx, frames, crc, f"duration_s={args.duration}, continuous=true"
            results.append(run_case(probe, "continuous_input", continuous_case,
                                    args.require_traffic))
    except (serial.SerialException, OSError) as exc:
        print(f"serial setup failed: {exc}", file=sys.stderr)
        return 2
    finally:
        probe.close()

    report = {
        "tool": "c6_uart0_serial_validation",
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(started)),
        "usb_port": args.usb_port,
        "acm_port": args.acm_port,
        "baud": args.baud,
        "configuration_modified": False,
        "firmware_modified": False,
        "results": [asdict(result) for result in results],
        "final_console": console_dict(probe.console_stats),
    }
    print(json.dumps(report, ensure_ascii=False, indent=2))
    if args.json:
        with open(args.json, "w", encoding="utf-8") as output:
            json.dump(report, output, ensure_ascii=False, indent=2)
            output.write("\n")
        print(f"evidence written to {args.json}")
    return 0 if all(result.status == "pass" for result in results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
