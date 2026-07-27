#!/usr/bin/env python3
"""Full temporary UART0 validation through MQTT + ttyUSB0/ttyACM0.

The script uses the existing MQTT control/up topics only for a temporary
manifest and for observing ConfigResult/DataReport.  ``ttyUSB0`` is the
external UART0 device simulator; ``ttyACM0`` is read-only C6 logging.  The
temporary manifest is always followed by an empty restore manifest using the
original ``v2-59739be4`` identifier.

No flash operation is performed by this script.  It is intentionally separate
from the build/flash workflow so a test run cannot silently replace firmware.
"""

from __future__ import annotations

import argparse
import binascii
import json
import os
import select
import struct
import termios
import threading
import time
import uuid
from dataclasses import dataclass, field
from typing import Optional

import paho.mqtt.client as mqtt


NODE_ID = "F0F5BDFFFE02"
ORIGINAL_MANIFEST = "v2-59739be4"
MSG_CONFIG = 0x04
MSG_CONFIG_RESULT = 0x05
MSG_DATA_REPORT = 0x03
MSG_WRITE_RSP = 0x07
MSG_RESOURCE_REPORT = 0x19
FRAME_MAGIC = b"ECV1"


class PosixSerial:
    """Raw tty fd that never changes DTR/RTS while opening the port."""

    def __init__(self, path: str, baud: int):
        self.fd = os.open(path, os.O_RDWR | os.O_NOCTTY | os.O_NONBLOCK)
        self.is_open = True
        attrs = termios.tcgetattr(self.fd)
        attrs[0] = 0
        attrs[1] = 0
        attrs[2] = termios.CS8 | termios.CLOCAL | termios.CREAD
        attrs[3] = 0
        speed = getattr(termios, f"B{baud}", None)
        if speed is None:
            self.close()
            raise ValueError(f"unsupported host baud constant: {baud}")
        attrs[4] = speed
        attrs[5] = speed
        termios.tcsetattr(self.fd, termios.TCSANOW, attrs)

    def reset_input_buffer(self) -> None:
        while True:
            try:
                if not os.read(self.fd, 4096):
                    return
            except BlockingIOError:
                return

    def reset_output_buffer(self) -> None:
        termios.tcflush(self.fd, termios.TCOFLUSH)

    def read(self, size: int) -> bytes:
        try:
            return os.read(self.fd, size)
        except BlockingIOError:
            return b""

    def write(self, data: bytes) -> int:
        view = memoryview(data)
        written = 0
        while written < len(view):
            try:
                n = os.write(self.fd, view[written:])
                if n <= 0:
                    break
                written += n
            except BlockingIOError:
                select.select([], [self.fd], [], 1.0)
        return written

    def flush(self) -> None:
        termios.tcdrain(self.fd)

    def close(self) -> None:
        if self.is_open:
            os.close(self.fd)
            self.is_open = False


def varint(value: int) -> bytes:
    out = bytearray()
    value = int(value)
    while value > 0x7F:
        out.append((value & 0x7F) | 0x80)
        value >>= 7
    out.append(value)
    return bytes(out)


def field_varint(number: int, value: int) -> bytes:
    return varint(number << 3) + varint(value)


def field_bytes(number: int, value: bytes) -> bytes:
    return varint((number << 3) | 2) + varint(len(value)) + value


def parse_fields(frame: bytes) -> tuple[int, list[tuple[int, int, object]]]:
    """Parse the small length-delimited/varint frame subset used here."""
    if not frame:
        return -1, []
    msg_type = frame[0]
    pos = 1
    fields: list[tuple[int, int, object]] = []
    while pos < len(frame):
        tag = 0
        shift = 0
        while pos < len(frame):
            byte = frame[pos]
            pos += 1
            tag |= (byte & 0x7F) << shift
            if not byte & 0x80:
                break
            shift += 7
            if shift > 63:
                return msg_type, fields
        number, wire = tag >> 3, tag & 7
        if wire == 0:
            value = 0
            shift = 0
            while pos < len(frame):
                byte = frame[pos]
                pos += 1
                value |= (byte & 0x7F) << shift
                if not byte & 0x80:
                    break
                shift += 7
                if shift > 63:
                    return msg_type, fields
            fields.append((number, wire, value))
        elif wire == 2:
            length = 0
            shift = 0
            while pos < len(frame):
                byte = frame[pos]
                pos += 1
                length |= (byte & 0x7F) << shift
                if not byte & 0x80:
                    break
                shift += 7
                if shift > 63:
                    return msg_type, fields
            value = frame[pos:pos + length]
            pos += length
            if len(value) != length:
                return msg_type, fields
            fields.append((number, wire, value))
        else:
            return msg_type, fields
    return msg_type, fields


def first_field(fields: list[tuple[int, int, object]], number: int, default=None):
    for field_number, _wire, value in fields:
        if field_number == number:
            return value
    return default


def test_frame(sequence: int, payload_size: int) -> bytes:
    payload = bytes((sequence + i) & 0xFF for i in range(payload_size))
    body = FRAME_MAGIC + struct.pack(">IH", sequence, payload_size) + payload
    return body + struct.pack(">I", binascii.crc32(body) & 0xFFFFFFFF)


def make_channel(channel_id: int, baud: int, dma: bool) -> bytes:
    # config_mgr's UART bus_config is TX, RX, big-endian baud.  Field 8 is
    # explicit DMA preference and is deliberately present in both variants.
    config = bytes([16, 17]) + baud.to_bytes(4, "big")
    return (
        field_varint(1, channel_id)
        + field_varint(2, 0)
        + field_varint(4, 0)
        + field_varint(5, 1)
        + field_varint(6, 1)
        + field_bytes(7, config)
        + field_varint(8, int(dma))
    )


def make_manifest(manifest_id: str, sync_id: str, channel: Optional[bytes]) -> bytes:
    frame = bytes([MSG_CONFIG]) + field_bytes(1, manifest_id.encode())
    if channel is not None:
        frame += field_bytes(4, channel)
    frame += field_bytes(8, sync_id.encode())
    return frame


@dataclass
class ConsoleStats:
    lines: int = 0
    overflow: int = 0
    uart_error: int = 0
    timeout: int = 0
    text_tail: list[str] = field(default_factory=list)
    interesting: list[str] = field(default_factory=list)
    all_lines: list[str] = field(default_factory=list)

    def add(self, text: str) -> None:
        self.lines += 1
        lower = text.lower()
        if "rx overflow" in lower or "fifo_ovf" in lower or "buffer_full" in lower:
            self.overflow += 1
        if ("parity" in lower or "frame_err" in lower or
                ("uart" in lower and "event=" in lower)):
            self.uart_error += 1
        if "rx timeout" in lower:
            self.timeout += 1
        if any(token in lower for token in (
                "rst:", "panic", "abort", "watchdog", "wdt", "bus_dma",
                "bus_worker", "cfg_h", "config transaction", "configresult",
                "config result", "uart0", "uart1", "dma")):
            self.interesting.append(text)
            del self.interesting[:-200]
        self.text_tail.append(text)
        del self.text_tail[:-20]
        self.all_lines.append(text)
        del self.all_lines[:-5000]


@dataclass
class UpstreamState:
    config_results: list[tuple[str, bool, str]] = field(default_factory=list)
    data_reports: list[dict] = field(default_factory=list)
    write_responses: list[dict] = field(default_factory=list)
    resource_reports: int = 0
    other_frames: int = 0
    lock: threading.Lock = field(default_factory=threading.Lock)

    def on_frame(self, payload: bytes) -> None:
        msg_type, fields = parse_fields(payload)
        with self.lock:
            if msg_type == MSG_CONFIG_RESULT:
                manifest = first_field(fields, 1, b"")
                if isinstance(manifest, bytes):
                    manifest = manifest.decode(errors="replace")
                success = bool(first_field(fields, 2, 0))
                sync_id = first_field(fields, 4, b"")
                if isinstance(sync_id, bytes):
                    sync_id = sync_id.decode(errors="replace")
                self.config_results.append((str(manifest), success, str(sync_id)))
            elif msg_type == MSG_DATA_REPORT:
                raw = first_field(fields, 4, b"")
                if not isinstance(raw, bytes):
                    raw = b""
                self.data_reports.append({
                    "channel_id": int(first_field(fields, 1, 0)),
                    "sequence": int(first_field(fields, 3, 0)),
                    "raw": raw,
                    "error_code": int(first_field(fields, 5, 0)),
                })
            elif msg_type == MSG_WRITE_RSP:
                error_msg = first_field(fields, 4, b"")
                if isinstance(error_msg, bytes):
                    error_msg = error_msg.decode(errors="replace")
                self.write_responses.append({
                    "request_id": int(first_field(fields, 1, 0)),
                    "success": bool(first_field(fields, 2, 0)),
                    "error_code": int(first_field(fields, 3, 0)),
                    "error_msg": str(error_msg),
                })
            elif msg_type == MSG_RESOURCE_REPORT:
                self.resource_reports += 1
            else:
                self.other_frames += 1


class Validation:
    def __init__(self, broker: str, usb: str, acm: str, baud: int, echo: bool):
        self.broker = broker
        self.usb_name = usb
        self.acm_name = acm
        self.baud = baud
        self.echo = echo
        self.mqtt = mqtt.Client()
        self.up = UpstreamState()
        self.usb: Optional[PosixSerial] = None
        self.acm: Optional[int] = None
        self.stop = threading.Event()
        self.echo_enabled = threading.Event()
        self.console = ConsoleStats()
        self.console_thread: Optional[threading.Thread] = None
        self.peripheral_thread: Optional[threading.Thread] = None
        self.usb_tx_observed = 0
        self.usb_lock = threading.Lock()

    def open(self) -> None:
        self.mqtt.on_message = self._mqtt_message
        self.mqtt.connect(self.broker, 1883, 30)
        self.mqtt.subscribe(f"nodes/{NODE_ID}/up", qos=1)
        self.mqtt.loop_start()
        self.usb = PosixSerial(self.usb_name, self.baud)
        # Do not use pyserial for the C6 USB Serial/JTAG console.  Its normal
        # open path toggles DTR/RTS, which is wired to the C6 reset circuit on
        # this board.  A read-only POSIX fd leaves modem-control lines alone.
        self.acm = os.open(self.acm_name, os.O_RDONLY | os.O_NOCTTY | os.O_NONBLOCK)
        self.usb.reset_input_buffer()
        self.usb.reset_output_buffer()
        self.console_thread = threading.Thread(target=self._console_loop, daemon=True)
        self.peripheral_thread = threading.Thread(target=self._peripheral_loop, daemon=True)
        self.console_thread.start()
        self.peripheral_thread.start()
        # Opening either USB serial adapter can reset this board through its
        # modem-control wiring.  Allow Wi-Fi, MQTT connect and both topic
        # subscriptions to settle before sending the first temporary manifest.
        # This is deliberately longer than the normal boot time so a reset
        # caused by the tty setup cannot race the control message.
        time.sleep(8.0)

    def close(self) -> None:
        self.stop.set()
        for thread in (self.console_thread, self.peripheral_thread):
            if thread:
                thread.join(timeout=1)
        if self.usb and self.usb.is_open:
            self.usb.close()
        if self.acm is not None:
            try:
                os.close(self.acm)
            except OSError:
                pass
            self.acm = None
        self.mqtt.loop_stop()
        self.mqtt.disconnect()

    def _mqtt_message(self, _client, _userdata, message) -> None:
        self.up.on_frame(bytes(message.payload))

    def _console_loop(self) -> None:
        assert self.acm is not None
        pending = bytearray()
        while not self.stop.is_set():
            try:
                chunk = os.read(self.acm, 2048)
            except BlockingIOError:
                chunk = b""
            except OSError:
                break
            if not chunk:
                continue
            pending.extend(chunk)
            while b"\n" in pending:
                line, _, pending = pending.partition(b"\n")
                text = line.decode(errors="replace").strip()
                if text:
                    self.console.add(text)

    def _peripheral_loop(self) -> None:
        assert self.usb is not None
        while not self.stop.is_set():
            chunk = self.usb.read(4096)
            if not chunk:
                continue
            with self.usb_lock:
                self.usb_tx_observed += len(chunk)
            if self.echo and self.echo_enabled.is_set():
                self.usb.write(chunk)
                self.usb.flush()

    def set_echo(self, enabled: bool) -> None:
        if enabled:
            self.echo_enabled.set()
        else:
            self.echo_enabled.clear()

    def publish_manifest(self, manifest: bytes) -> None:
        token = self.mqtt.publish(f"nodes/{NODE_ID}/control", manifest, qos=1)
        token.wait_for_publish(timeout=10)

    def wait_config(self, manifest_id: str, timeout: float = 15) -> tuple[bool, str]:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            with self.up.lock:
                for got_id, success, sync_id in reversed(self.up.config_results):
                    if got_id == manifest_id:
                        return success, sync_id
            time.sleep(0.05)
        return False, "timeout"

    def send_frames(self, count: int, payload_size: int,
                    idle_ms: float, duration: Optional[float] = None) -> tuple[bytes, int]:
        assert self.usb is not None
        sent = bytearray()
        sequence = 0
        deadline = time.monotonic() + duration if duration else None
        while (sequence < count or
               (deadline is not None and time.monotonic() < deadline)):
            frame = test_frame(sequence, payload_size)
            self.usb.write(frame)
            sent.extend(frame)
            sequence += 1
            if idle_ms:
                self.usb.flush()
                time.sleep(idle_ms / 1000)
        self.usb.flush()
        return bytes(sent), sequence

    def wait_data(self, expected: bytes, channel_id: int, timeout: float) -> dict:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            with self.up.lock:
                reports = [r for r in self.up.data_reports if r["channel_id"] == channel_id]
                raw = b"".join(r["raw"] for r in reports if not r["error_code"])
                errors = [r for r in reports if r["error_code"]]
                if len(raw) >= len(expected):
                    return {
                        "reports": len(reports),
                        "raw_bytes": len(raw),
                        "expected_bytes": len(expected),
                        "exact": raw[:len(expected)] == expected,
                        "raw_crc32": f"{binascii.crc32(raw) & 0xffffffff:08x}",
                        "expected_crc32": f"{binascii.crc32(expected) & 0xffffffff:08x}",
                        "raw_hex": raw[:64].hex(),
                        "errors": errors,
                    }
            time.sleep(0.05)
        with self.up.lock:
            reports = [r for r in self.up.data_reports if r["channel_id"] == channel_id]
            raw = b"".join(r["raw"] for r in reports if not r["error_code"])
        return {
            "reports": len(reports),
            "raw_bytes": len(raw),
            "expected_bytes": len(expected),
        "exact": raw == expected,
        "raw_crc32": f"{binascii.crc32(raw) & 0xffffffff:08x}",
        "expected_crc32": f"{binascii.crc32(expected) & 0xffffffff:08x}",
        "raw_hex": raw[:64].hex(),
        "errors": [r for r in reports if r["error_code"]],
        }


def run(args) -> int:
    validation = Validation(args.broker, args.usb_port, args.acm_port, args.baud, args.echo)
    temporary_id = f"uart0-event-{int(time.time())}"
    restore_sync = str(uuid.uuid4())
    results: dict = {"configuration_modified": True, "firmware_modified": False,
                     "temporary_manifest": temporary_id, "cases": []}
    applied = False
    opened = False
    manifest_sent = False
    try:
        validation.open()
        opened = True
        if args.dma_only:
            dma_cases = (True,)
        else:
            dma_cases = (False, True) if args.test_dma else (False,)
        for dma in dma_cases:
            manifest_id = f"{temporary_id}-dma{int(dma)}"
            sync_id = str(uuid.uuid4())
            manifest_sent = True
            validation.publish_manifest(make_manifest(
                manifest_id, sync_id, make_channel(args.channel_id, args.baud, dma)
            ))
            success, result_sync = validation.wait_config(manifest_id)
            case = {"name": f"manifest_dma_{int(dma)}", "success": success,
                    "sync_id": result_sync, "requested_dma": dma}
            results["cases"].append(case)
            if not success:
                continue
            applied = True
            # UART driver installation can leave bridge-generated line-status
            # events in the queue.  Let that startup noise expire before the
            # deterministic payload is injected, then discard only reports
            # produced during the settle window.
            time.sleep(args.config_settle_ms / 1000.0)
            with validation.up.lock:
                validation.up.data_reports.clear()
            sent, count = validation.send_frames(args.frame_count, args.payload_size, args.idle_ms)
            case["frames"] = count
            case["sent_bytes"] = len(sent)
            case["crc32"] = f"{binascii.crc32(sent) & 0xffffffff:08x}"
            case["data"] = validation.wait_data(sent, args.channel_id, args.report_timeout)

        # Control response path: C6 sends the request to the ttyUSB0 echo
        # peripheral and publishes WriteRsp/DataReport through the publisher.
        if applied and args.test_write:
            validation.set_echo(True)
            request_id = 0xA501
            request = (bytes([0x06]) + field_varint(1, request_id) +
                       field_varint(2, args.channel_id) + field_bytes(3, b"V2T1") +
                       field_varint(4, 4) + field_varint(6, 2000))
            with validation.up.lock:
                before_rsp = len(validation.up.write_responses)
            validation.mqtt.publish(f"nodes/{NODE_ID}/control", request, qos=1).wait_for_publish(10)
            deadline = time.monotonic() + args.report_timeout
            response = None
            while time.monotonic() < deadline:
                with validation.up.lock:
                    matches = [r for r in validation.up.write_responses[before_rsp:]
                               if r["request_id"] == request_id]
                    if matches:
                        response = matches[-1]
                if response is not None:
                    break
                time.sleep(0.05)
            results["cases"].append({
                "name": "write_command_echo",
                "write_rsp": response,
            })
            validation.set_echo(False)
    finally:
        if opened and manifest_sent:
            try:
                validation.publish_manifest(make_manifest(ORIGINAL_MANIFEST, restore_sync, None))
                restored, restore_result_sync = validation.wait_config(ORIGINAL_MANIFEST)
                results["restore"] = {"success": restored, "sync_id": restore_result_sync}
            except Exception as exc:
                results["restore"] = {"success": False, "error": str(exc)}
        if opened:
            validation.close()
    results["console"] = {
        "lines": validation.console.lines,
        "overflow": validation.console.overflow,
        "uart_error": validation.console.uart_error,
        "timeout": validation.console.timeout,
        "usb_tx_observed": validation.usb_tx_observed,
        "interesting": validation.console.interesting,
        "all_lines": validation.console.all_lines,
        "tail": validation.console.text_tail,
    }
    print(json.dumps(results, ensure_ascii=False, indent=2))
    if args.json:
        with open(args.json, "w", encoding="utf-8") as output:
            json.dump(results, output, ensure_ascii=False, indent=2)
            output.write("\n")
    return 0 if results.get("restore", {}).get("success") and all(
        case.get("success", True) and case.get("data", {}).get("exact", True)
        for case in results["cases"] if case.get("name", "").startswith("manifest")
    ) else 1


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--broker", default="127.0.0.1")
    parser.add_argument("--usb-port", default="/dev/ttyUSB0")
    parser.add_argument("--acm-port", default="/dev/ttyACM0")
    parser.add_argument("--baud", type=int, default=1_000_000)
    parser.add_argument("--channel-id", type=int, default=9001)
    parser.add_argument("--frame-count", type=int, default=10)
    parser.add_argument("--payload-size", type=int, default=512)
    parser.add_argument("--idle-ms", type=float, default=50)
    parser.add_argument("--report-timeout", type=float, default=20)
    parser.add_argument("--config-settle-ms", type=float, default=300,
                        help="settle UART line-status events after config")
    parser.add_argument("--test-dma", action="store_true")
    parser.add_argument("--dma-only", action="store_true",
                        help="apply only the DMA-enabled temporary manifest")
    parser.add_argument("--test-write", action="store_true")
    parser.add_argument("--echo", action="store_true")
    parser.add_argument("--json")
    return run(parser.parse_args())


if __name__ == "__main__":
    raise SystemExit(main())
