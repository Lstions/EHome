#!/usr/bin/env python3
"""UART 从机仿真器：让 S3 的两路真实 UART 总线（ch8/ch9）有从机应答。

现场拓扑（2026-10-05 实机实测确认，见 docs/验证/证据/2026-10-05-实机压测-*.md）：
  ch8 -> UART0 GPIO43/44 @ 9600  -> /dev/ttyUSB0 (CP2102) -> 嘉佰达 BMS 协议
        固件轮询帧实测 dd a5 03 00 ff fd 77，间隔 ~5.0 s（现场 manifest interval_ms=5000）
  ch9 -> UART1 GPIO4/5   @ 2400  -> /dev/ttyUSB1 (CH340)  -> GB3024/Techfine ASCII 协议
        固件轮询帧实测 48 53 54 53 0d = "HSTS\r"，间隔 ~1.0 s（现场 manifest interval_ms=1000）

本脚本**只做从机应答与计数**：不刷机、不改设备配置、不改固件/后端源码。
它是 task-3「UART 从机仿真器」交付物，复用了 scripts/uart0_bms_rain_simulator.py 的
嘉佰达帧构造器，并新增 GB3024/Techfine ASCII 档。

用法：
  python3 scripts/uart_slave_sim.py --port /dev/ttyUSB0 --baud 9600 \
      --profile jiabaida --duration 300 --json /tmp/ch8_slave.json
  python3 scripts/uart_slave_sim.py --port /dev/ttyUSB1 --baud 2400 \
      --profile techfine --duration 300 --json /tmp/ch9_slave.json

输出 JSON（证据）：{profile, port, baud, duration_s, requests, per_command, responses,
                    malformed, first_req_ts, last_req_ts, observed_hz, log:[...]}

诚实性口径：本脚本只证明"从机侧能应答、且收到/回复的帧数一致"，
它不证明设备侧解析成功——那要看 S3 的 DataReport 与后端入库。
"""

from __future__ import annotations

import argparse
import json
import os
import struct
import sys
import time

import serial

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

# 复用既有嘉佰达帧构造器（scripts/uart0_bms_rain_simulator.py）
from uart0_bms_rain_simulator import (  # noqa: E402
    bms_basic_info,
    bms_cell_voltage,
    bms_comprehensive,
    bms_hardware_version,
    bms_protection_count,
    bms_response,
    handle_bms,
    modbus_crc16,
)


# --------------------------------------------------------------------------
# GB3024 / Techfine ASCII 档
# --------------------------------------------------------------------------
# 响应必须满足后端 VerifyControlAction 的两道判据（字段族 + 最少字段数），
# 否则后端会判为"响应与请求不符"—— 那是后端的事，但从机应产生**合法形状**的帧，
# 否则压测测的是"畸形帧被拒"而不是吞吐。
TECHFINE_RESPONSES = {
    # fault_code + work_mode + 12 alarm flags（statusStr 必须 >= 13 字符）
    "HSTS": "(00 P000000000000\r",
    # grid_voltage + grid_frequency（>=6 字段）
    "HGRID": "(220.5 50.0 100 200 55 45\r",
    # fields[2]/[3] 需 >=4 位数字，fields[7] 需含 '.'（HOP 判别）
    "HOP": "(220.5 50.0 01234 01111 020 000 00000 005.5\r",
    # >=7 字段，首个为整数
    "HBAT": "(04 053.2 080 002 00000 054 11111\r",
    # PV 功率字段需 >=4 字符（防截断 fail-open 判据）
    "HPV": "(220.5 08.0 00960\r",
    "HPVB": "(219.8 07.5 00800\r",
    # >=11 字段
    "HTEMP": "(025 031 029 033 035 045 000 00 028 030 001\r",
    # fields[1] 含 ':' → HGEN
    "HGEN": "(202610 23:59 01.234 0123.4 1234.5 012345.6\r",
    # fields[1]/[2] 为 8 字符二进制 → HBMS1
    "HBMS1": "(AA 00000011 00000001 080 002 000 054 055 100 050 030 025\r",
    # fields[0] 单数字 0-2 且 >=15 字段 → HEEP1
    "HEEP1": "(1 030 030 0 1 0 1 230 0 2 0 0 0 0 1 020 030 040 045 057.6 055.2 042.0 0\r",
    # fields[1] 为 8 位数字 → HIMSG1
    "HIMSG1": "(0000.03 20230220 00\r",
}

DEFAULT_TECHFINE = "(00 P000000000000\r"


def techfine_response(command: str) -> bytes:
    cmd = command.strip().upper()
    return TECHFINE_RESPONSES.get(cmd, DEFAULT_TECHFINE).encode("ascii")


def build_bms_args(ns) -> argparse.Namespace:
    """构造 handle_bms() 需要的状态对象（与 uart0 模拟器 main() 一致）。"""
    f2_default = bytearray(53)
    struct.pack_into(">H", f2_default, 51, modbus_crc16(bytes(f2_default[:51])))
    f3_default = bytearray(52)
    struct.pack_into(">H", f3_default, 50, modbus_crc16(bytes(f3_default[:50])))
    ns.bms_params = bytes(f2_default)
    ns.bms_sys_params = bytes(f3_default)
    ns.factory_mode = False
    ns.custom_attrs = bytes(6)
    ns.resistances = bytes(60)
    ns.test_mos_status = bytes(2)
    ns.force_balance = False
    ns.find_car = False
    return ns


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--port", required=True)
    ap.add_argument("--baud", type=int, required=True)
    ap.add_argument("--profile", choices=["jiabaida", "techfine"], required=True)
    ap.add_argument("--duration", type=float, default=0.0, help="0=一直运行")
    ap.add_argument("--json", help="把统计写成 JSON")
    ap.add_argument("--poll-timeout", type=float, default=0.005,
                    help="串口读超时（秒）。100 Hz 压测需小值")
    ap.add_argument("--log-limit", type=int, default=4000, help="JSON 里保留的请求条数上限")
    ap.add_argument("--quiet", action="store_true")
    # 嘉佰达从机状态
    ap.add_argument("--bms-voltage", type=float, default=48.0)
    ap.add_argument("--bms-current", type=float, default=2.0)
    ap.add_argument("--bms-soc", type=int, default=80)
    ap.add_argument("--mos", type=lambda x: int(x, 0), default=0x00)
    ap.add_argument("--restart-count", type=int, default=0)
    ap.add_argument("--sn", default="SIM-BMS-0001")
    args = ap.parse_args()

    args.sensitivity = 60
    if not args.sn or len(args.sn) > 31:
        args.sn = "SIM-BMS-0001"
    build_bms_args(args)

    s = serial.Serial(args.port, args.baud, timeout=args.poll_timeout)
    start = time.time()
    stats = {
        "profile": args.profile,
        "port": args.port,
        "baud": args.baud,
        "requests": 0,
        "responses": 0,
        "per_command": {},
        "malformed": 0,
        "first_req_ts": None,
        "last_req_ts": None,
        "log": [],
        "inter_request_gaps_ms": [],
    }
    buf = bytearray()
    last_req_t = None
    last_log_t = 0.0
    try:
        while True:
            if args.duration > 0 and time.time() - start > args.duration:
                break
            chunk = s.read(1024)
            if chunk:
                buf += chunk
            # --- 逐帧处理 ---
            while buf:
                if args.profile == "jiabaida":
                    if len(buf) >= 4 and buf[0] == 0xDD and buf[1] in (0xA5, 0x5A):
                        ln = buf[3]
                        end = 7 + ln
                        if len(buf) < end:
                            break
                        frame = bytes(buf[:end])
                        del buf[:end]
                        _handle_bms(frame, args, s, stats)
                        last_req_t = _mark(stats, frame.hex(), last_req_t, args.log_limit)
                        continue
                    # 未识别的字节：丢弃 1 字节（避免卡死）
                    if len(buf) >= 4:
                        stats["malformed"] += 1
                        del buf[:1]
                        continue
                    break
                else:  # techfine ASCII
                    if b"\r" in buf:
                        idx = buf.index(b"\r")
                        line = bytes(buf[:idx + 1])
                        del buf[:idx + 1]
                        resp = techfine_response(line.decode("ascii", "replace"))
                        s.write(resp)
                        stats["responses"] += 1
                        cmd = line.decode("ascii", "replace").strip().upper()
                        stats["per_command"][cmd] = stats["per_command"].get(cmd, 0) + 1
                        last_req_t = _mark(stats, line.hex(), last_req_t, args.log_limit)
                        if not args.quiet:
                            print(f"  {cmd} -> {resp!r}", flush=True)
                        continue
                    break
    except KeyboardInterrupt:
        pass
    finally:
        try:
            s.close()
        except Exception:
            pass

    elapsed = max(time.time() - start, 1e-9)
    stats["duration_s"] = round(elapsed, 2)
    stats["observed_hz"] = round(stats["requests"] / elapsed, 3)
    if stats["log"]:
        stats["first_req_ts"] = stats["log"][0][0]
        stats["last_req_ts"] = stats["log"][-1][0]
    print(json.dumps({k: v for k, v in stats.items() if k != "log"},
                     ensure_ascii=False, indent=2))
    if args.json:
        with open(args.json, "w", encoding="utf-8") as fh:
            json.dump(stats, fh, ensure_ascii=False, indent=2)
    return 0


def _mark(stats: dict, hexframe: str, last_req_t, log_limit: int = 4000):
    now = round(time.time(), 4)
    stats["requests"] += 1
    if last_req_t is not None:
        stats["inter_request_gaps_ms"].append(round((now - last_req_t) * 1000.0, 2))
    if len(stats["log"]) < log_limit:
        stats["log"].append([now, hexframe])
    return now


def _handle_bms(frame: bytes, args, s, stats: dict) -> None:
    """解析一帧嘉佰达请求并应答（与 uart0 模拟器同一套构造器）。"""
    rw = frame[1]
    cmd = frame[2]
    resp = handle_bms(cmd, rw, args, frame)
    stats["per_command"][f"0x{cmd:02x}"] = stats["per_command"].get(f"0x{cmd:02x}", 0) + 1
    if resp:
        s.write(resp)
        stats["responses"] += 1


if __name__ == "__main__":
    sys.exit(main())
