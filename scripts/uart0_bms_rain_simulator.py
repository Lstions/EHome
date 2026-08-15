#!/usr/bin/env python3
"""EHomeSystem UART0 模拟器: 同时模拟嘉佰达 BMS 和 SN-3001 雨量计。

监听 /dev/ttyUSB0 (CP2102 连 ESP32 UART0 GPIO16/17, 9600 8N1)。
根据接收帧的首字节区分请求:
  - 0xDD 0xA5/0x5A ... 0x77  -> 嘉佰达 BMS 帧
  - 0x01 0x03 ...           -> SN-3001 Modbus 读雨量

用法:
  python3 scripts/uart0_bms_rain_simulator.py [--port /dev/ttyUSB0] [--baud 9600]
    [--bms-voltage 48.0] [--bms-current 2.0] [--bms-soc 80] [--rain-mm 0.5]
    [--mos X]  (X=0x00 解除, 0x01 禁充, 0x02 禁放, 0x03 双禁)
    [--duration 0]  (0=一直运行)
"""

from __future__ import annotations

import argparse
import struct
import sys
import time

import serial


def checksum(data: bytes) -> int:
    """嘉佰达校验和: 逐字节求和取反加一. 返回 16 位. 覆盖范围由调用方决定."""
    s = 0
    for b in data:
        s += b
    return (~s + 1) & 0xFFFF


def bms_response(cmd: int, data: bytes, target_len: int = 0) -> bytes:
    """嘉佰达正常响应帧: DD CMD 00 LEN DATA CRC_H CRC_L 77.
    校验和覆盖 LEN + DATA (不含 CMD/STATUS), 见 verifyJiabaidaChecksum.
    target_len>0 时把 DATA 填充(0x00)到 帧总长=target_len, 满足 ESP32 read_size."""
    if target_len > 0:
        frame_head = 7  # DD CMD 00 LEN CRC_H CRC_L 77
        need_data = target_len - frame_head
        if len(data) < need_data:
            data = data + bytes([0x00]) * (need_data - len(data))
    ck = checksum(bytes([len(data)]) + data)  # LEN+DATA 求和
    return bytes([0xDD, cmd, 0x00, len(data)]) + data + struct.pack(">H", ck) + bytes([0x77])


def bms_basic_info(total_v: float, current_a: float, remaining_ah: float,
                   nominal_ah: float, cycle: int, rsoc: int,
                   fet_status: int, cell_count: int, ntc_count: int,
                   temps_c: list[float]) -> bytes:
    """构造 0x03 基本信息的 DATA 段 (见 jiabaida.go parse0x03 布局)."""
    data = bytearray()
    data += struct.pack(">H", int(total_v * 100))          # [0:2] 总压 10mV
    data += struct.pack(">h", int(current_a * 100))        # [2:4] 电流 10mA 有符号
    data += struct.pack(">H", int(remaining_ah * 100))     # [4:6] 剩余容量 10mAh
    data += struct.pack(">H", int(nominal_ah * 100))       # [6:8] 额定容量 10mAh
    data += struct.pack(">H", cycle)                       # [8:10] 循环次数
    data += struct.pack(">H", 0)                           # [10:12] 生产日期
    data += struct.pack(">H", 0)                           # [12:14] 均衡低
    data += struct.pack(">H", 0)                           # [14:16] 均衡高
    data += struct.pack(">H", 0)                           # [16:18] 保护状态
    data += bytes([0x01])                                  # [18] 软件版本
    data += bytes([rsoc])                                  # [19] RSOC %
    data += bytes([fet_status])                            # [20] FET 控制状态
    data += bytes([cell_count])                            # [21] 电芯数
    data += bytes([ntc_count])                             # [22] NTC 数
    for t in temps_c[:ntc_count]:                          # [23:] 温度
        data += struct.pack(">H", int((t + 273.15) * 10))  # 0.1K
    return data


def bms_cell_voltage(cell_mv: list[int]) -> bytes:
    """0x04 单体电压 DATA: N×2B mV."""
    data = bytearray()
    for mv in cell_mv:
        data += struct.pack(">H", mv)
    return bytes(data)


def bms_hardware_version(ver: str) -> bytes:
    """0x05 硬件版本 DATA: 字符串 ASCII."""
    return ver.encode("ascii")


def bms_comprehensive() -> bytes:
    """0x0F 综合信息 DATA (简化, 与 parse0x0F 布局一致)."""
    data = bytearray()
    data += bytes([0x00])                                   # [0] reserved
    data += struct.pack(">H", int(48.0 * 100))              # [1:3] 总压
    data += struct.pack(">h", 200)                          # [3:5] 电流 20A
    data += bytes([80])                                     # [5] SOC
    data += struct.pack(">H", int(100.0 * 100))             # [6:8] 剩余容量
    data += struct.pack(">H", int(100.0 * 100))             # [8:10] 满容量
    data += struct.pack(">H", 0)                            # [10:12] 保护
    data += struct.pack(">H", 3300)                         # [12:14] 最高单体
    data += struct.pack(">H", 3200)                         # [14:16] 最低单体
    data += struct.pack(">H", 0)                            # [16:18] 均衡低
    data += struct.pack(">H", 0)                            # [18:20] 均衡高
    data += struct.pack(">H", 10)                           # [20:22] 循环
    data += bytes([0x00])                                   # [22] FET
    data += bytes([1])                                      # [23] NTC 数
    data += struct.pack(">H", int((25.0 + 273.15) * 10))    # [24:26] 温度
    data += bytes([16])                                     # cell count M
    for _ in range(16):                                     # M×2B 单体电压
        data += struct.pack(">H", 3250)
    # trailer: current_state(1) + charge_cap(2) + runtime(2) + seq(2) + hum(1)
    data += bytes([0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00])
    return bytes(data)


def bms_protection_count(restart_count: int = 0) -> bytes:
    """0xAA 保护历史次数 DATA: 12×uint16, 最后一个是 restart_count."""
    data = bytearray()
    for i in range(12):
        v = restart_count if i == 11 else 0
        data += struct.pack(">H", v)
    return bytes(data)


def modbus_crc16(data: bytes) -> int:
    """Modbus CRC16 (poly 0xA001, LSB-first)."""
    crc = 0xFFFF
    for b in data:
        crc ^= b
        for _ in range(8):
            if crc & 1:
                crc = (crc >> 1) ^ 0xA001
            else:
                crc >>= 1
    return crc


def sn3001_read_rainfall_response(mm: float, addr: int = 0x01) -> bytes:
    """SN-3001 读雨量响应: <addr> 03 02 <rainfall*10> <CRC16 LE>."""
    rain_raw = int(mm * 10)
    payload = bytes([addr, 0x03, 0x02]) + struct.pack(">H", rain_raw)
    crc = modbus_crc16(payload)
    return payload + struct.pack("<H", crc)


def sn3001_read_register_response(addr: int, reg: int, value: int) -> bytes:
    """SN-3001 读寄存器响应: <addr> 03 02 <value BE> <CRC16 LE>."""
    payload = bytes([addr, 0x03, 0x02]) + struct.pack(">H", value)
    crc = modbus_crc16(payload)
    return payload + struct.pack("<H", crc)


def sn3001_write_register_response(addr: int, reg: int, value: int) -> bytes:
    """SN-3001 写寄存器响应 (echo): <addr> 06 <reg BE> <value BE> <CRC16 LE>."""
    payload = bytes([addr, 0x06]) + struct.pack(">H", reg) + struct.pack(">H", value)
    crc = modbus_crc16(payload)
    return payload + struct.pack("<H", crc)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", default="/dev/ttyUSB0")
    ap.add_argument("--baud", type=int, default=9600)
    ap.add_argument("--bms-voltage", type=float, default=48.0)
    ap.add_argument("--bms-current", type=float, default=2.0)
    ap.add_argument("--bms-soc", type=int, default=80)
    ap.add_argument("--rain-mm", type=float, default=0.5)
    ap.add_argument("--mos", type=lambda x: int(x, 0), default=0x00)
    ap.add_argument("--restart-count", type=int, default=0, help="0xAA restart_count 初值")
    ap.add_argument("--sensitivity", type=int, default=60, help="SN-3001 0x0052 灵敏度寄存器初值")
    ap.add_argument("--duration", type=float, default=0.0, help="0=一直运行")
    args = ap.parse_args()

    try:
        s = serial.Serial(args.port, args.baud, timeout=0.3)
    except Exception as e:
        print(f"无法打开 {args.port}: {e}", file=sys.stderr)
        return 1

    print(f"[模拟器] 监听 {args.port} @ {args.baud}, BMS+雨量计, mos=0x{args.mos:02x}, "
          f"sensitivity=0x{args.sensitivity:04x}")
    start = time.time()
    try:
        while True:
            if args.duration > 0 and time.time() - start > args.duration:
                break
            data = s.read(512)
            if not data:
                continue
            # 按帧逐个处理
            i = 0
            while i < len(data):
                b = data[i]
                # 嘉佰达 BMS 帧
                if b == 0xDD and i + 3 < len(data) and data[i + 1] in (0xA5, 0x5A):
                    rw = data[i + 1]
                    cmd = data[i + 2]
                    ln = data[i + 3]
                    end = i + 4 + ln + 2 + 1  # +crc2 +0x77
                    if end <= len(data):
                        frame = data[i:end]
                        resp = handle_bms(cmd, rw, args, frame)
                        if resp:
                            s.write(resp)
                            print(f"  BMS req 0x{cmd:02x} -> {resp.hex()}")
                        else:
                            print(f"  BMS req 0x{cmd:02x} (无响应)")
                        i = end
                        continue
                # SN-3001 Modbus 帧 (任意从机地址 addr): 03 读寄存器 / 06 写寄存器
                if i + 7 < len(data) and data[i + 1] in (0x03, 0x06):
                    addr = data[i]
                    fc = data[i + 1]
                    reg = struct.unpack(">H", data[i + 2:i + 4])[0]
                    end = i + 8
                    if end > len(data):
                        i += 1
                        continue
                    if fc == 0x03:
                        # 读雨量累计 (0x0000) 或灵敏度 (0x0052)
                        if reg == 0x0000:
                            resp = sn3001_read_rainfall_response(args.rain_mm, addr)
                            print(f"  雨量计[0x{addr:02x}] req 0x03 0x{reg:04x} -> {resp.hex()}")
                        elif reg == 0x0052:
                            resp = sn3001_read_register_response(addr, reg, args.sensitivity)
                            print(f"  雨量计[0x{addr:02x}] req 0x03 0x{reg:04x} (灵敏度) -> {resp.hex()} "
                                  f"(value=0x{args.sensitivity:04x})")
                        else:
                            resp = None
                            print(f"  雨量计[0x{addr:02x}] req 0x03 0x{reg:04x} (无实现)")
                    else:  # fc == 0x06 写寄存器
                        value = struct.unpack(">H", data[i + 4:i + 6])[0]
                        if reg == 0x0052:
                            args.sensitivity = value
                            print(f"  [写] 雨量计[0x{addr:02x}] 灵敏度 0x{reg:04x} <- 0x{value:04x}")
                            resp = sn3001_write_register_response(addr, reg, value)
                        elif reg == 0x0000:
                            # 清零累计雨量 (reset workflow: write 0x005A 到 0x0000)
                            args.rain_mm = 0.0
                            print(f"  [写] 雨量计[0x{addr:02x}] 清零累计雨量 0x{reg:04x} <- 0x{value:04x} (rain now 0)")
                            resp = sn3001_write_register_response(addr, reg, value)
                        else:
                            resp = None
                            print(f"  [写] 雨量计[0x{addr:02x}] 0x{reg:04x} <- 0x{value:04x} (无实现)")
                    if resp:
                        s.write(resp)
                    i = end
                    continue
                i += 1
    except KeyboardInterrupt:
        pass
    finally:
        s.close()
    return 0


def handle_bms(cmd: int, rw: int, args, frame: bytes | None = None) -> bytes | None:
    """构造 BMS 响应. rw 0xA5=读, 0x5A=写. frame 为完整请求帧(写命令解析用)."""
    if rw == 0x5A:
        # 写命令: 0xE1 MOS 策略 / 0x0E BMS 复位 → 成功响应
        if cmd == 0xE1:
            # MOS 策略写: 更新 args.mos 使后续 0x03 readback 反映新状态
            # 帧: DD 5A E1 02 YY XX CRC 77 (YY=00 user/AA operator, XX bit0 禁充/bit1 禁放)
            if frame:
                try:
                    yy = frame[4]
                    xx = frame[5]
                    args.mos = xx & 0x03
                    print(f"  [写] BMS MOS 策略 -> 0x{args.mos:02x} (priority=0x{yy:02x})")
                except IndexError:
                    print(f"  [写] BMS MOS 策略帧过短: {frame.hex()}")
            return bms_response(0xE1, b"")
        if cmd == 0x0E:
            # BMS 复位: 收到后进入复位重启状态, 返回 DD 0E 00 00 CRC 77
            args.restart_count = (getattr(args, "restart_count", 0) + 1) & 0xFFFF
            print(f"  [写] BMS 复位触发, restart_count -> {args.restart_count}")
            return bms_response(0x0E, b"")
        print(f"  [写] cmd=0x{cmd:02x} 无实现")
        return None
    # 读命令
    if cmd == 0x03:
        data = bms_basic_info(
            args.bms_voltage, args.bms_current, 100.0, 100.0, 10,
            args.bms_soc, args.mos, 16, 4, [25.0, 26.0, 27.0, 28.0],
        )
        return bms_response(0x03, data, target_len=60)
    if cmd == 0x04:
        return bms_response(0x04, bms_cell_voltage([3250] * 16), target_len=50)
    if cmd == 0x05:
        return bms_response(0x05, bms_hardware_version("V1.0"), target_len=40)
    if cmd == 0x0F:
        return bms_response(0x0F, bms_comprehensive(), target_len=100)
    if cmd == 0xAA:
        return bms_response(0xAA, bms_protection_count(getattr(args, "restart_count", 0)), target_len=40)
    print(f"  [读] cmd=0x{cmd:02x} 无实现")
    return None


if __name__ == "__main__":
    sys.exit(main())