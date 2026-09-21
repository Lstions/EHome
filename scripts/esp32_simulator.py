#!/usr/bin/env python3
"""ESP32 MQTT 模拟器: 自动化引擎端到端验收。

与 uart0_bms_rain_simulator.py 不同, 本脚本工作在 MQTT 层 (模拟整个 ESP32 节点),
不需要真实 ESP32 硬件。直接对 backend 的 nodemgr.HandleMessage 链路施压:
  - 上报 StatusRpt / DataRpt 触发 alert + automation 求值
  - 订阅 control topic, 应答 PeriphCmd (0x1B) 与 ChannelCmdV2 (0x15)
  - 用于验证 S1 (光照→GPIO) / S2 (时间窗→BMS MOS) 端到端

用法:
  python3 scripts/esp32_simulator.py --node-id F0F5BDFFFE02 [--broker 127.0.0.1:1883]
"""
from __future__ import annotations

import argparse
import logging
import queue
import struct
import sys
import threading
import time
from dataclasses import dataclass, field
from typing import Any

import paho.mqtt.client as mqtt

log = logging.getLogger("esp32sim")

# 与 backend/pkg/frame/frame.go 对齐
MSG_HELLO = 0x01
MSG_STATUS_RPT = 0x02
MSG_DATA_RPT = 0x03
MSG_RESOURCE_RPT = 0x19
MSG_CHANNEL_CMD_V2 = 0x15
MSG_CHANNEL_CMD_V2_ACK = 0x16
MSG_CHANNEL_CMD_V2_FINAL = 0x17
MSG_PERIPH_CMD = 0x1B
MSG_PERIPH_RSP = 0x1C

WIRE_VARINT = 0
WIRE_LEN = 2


# ── protobuf-lite 编码（与 backend/pkg/frame 兼容）─────────────────────

def _varint(v: int) -> bytes:
    out = bytearray()
    while True:
        b = v & 0x7F
        v >>= 7
        if v:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def _tag(field_no: int, wire: int) -> bytes:
    return _varint((field_no << 3) | wire)


def ev(field_no: int, value: int) -> bytes:
    """Encode varint field."""
    return _tag(field_no, WIRE_VARINT) + _varint(value)


def es(field_no: int, value: str) -> bytes:
    """Encode string field."""
    data = value.encode()
    return _tag(field_no, WIRE_LEN) + _varint(len(data)) + data


def eb(field_no: int, value: bytes) -> bytes:
    """Encode bytes field."""
    return _tag(field_no, WIRE_LEN) + _varint(len(value)) + value


@dataclass
class Field:
    num: int
    wire: int
    value: Any


def decode(payload: bytes, start_offset: int = 1) -> tuple[int, list[Field]]:
    """Decode a frame. Returns (msg_type, fields). start_offset=1 skips msg_type byte; pass 0 to parse a sub-message body."""
    if not payload:
        return 0, []
    msg_type = payload[0] if start_offset > 0 else 0
    fields: list[Field] = []
    i = start_offset
    while i < len(payload):
        # read varint tag
        tag = 0
        shift = 0
        while True:
            b = payload[i]; i += 1
            tag |= (b & 0x7F) << shift
            shift += 7
            if not (b & 0x80):
                break
        num = tag >> 3
        wire = tag & 0x7
        if wire == WIRE_VARINT:
            v = 0
            shift = 0
            while True:
                b = payload[i]; i += 1
                v |= (b & 0x7F) << shift
                shift += 7
                if not (b & 0x80):
                    break
            fields.append(Field(num, wire, v))
        elif wire == WIRE_LEN:
            ln = 0
            shift = 0
            while True:
                b = payload[i]; i += 1
                ln |= (b & 0x7F) << shift
                shift += 7
                if not (b & 0x80):
                    break
            data = payload[i:i + ln]; i += ln
            fields.append(Field(num, wire, bytes(data)))
        else:
            raise ValueError(f"unsupported wire type {wire}")
    return msg_type, fields


# ── 嘉佰达 BMS 帧构造（参照 scripts/uart0_bms_rain_simulator.py）───────

def jbd_checksum(data: bytes) -> int:
    s = sum(data)
    return (~s + 1) & 0xFFFF


def jbd_response(cmd: int, data: bytes) -> bytes:
    """DD CMD 00 LEN DATA CRC_H CRC_L 77"""
    ck = jbd_checksum(bytes([len(data)]) + data)
    return bytes([0xDD, cmd, 0x00, len(data)]) + data + struct.pack(">H", ck) + bytes([0x77])


def jbd_basic_info(rsoc: int = 80, fet_status: int = 0x00, current_a: float = 2.0) -> bytes:
    """0x03 响应 DATA 段."""
    d = bytearray()
    d += struct.pack(">H", int(48.0 * 100))          # 总压 10mV
    d += struct.pack(">h", int(current_a * 100))     # 电流 10mA
    d += struct.pack(">H", int(100.0 * 100))         # 剩余容量
    d += struct.pack(">H", int(100.0 * 100))         # 额定容量
    d += struct.pack(">H", 10)                       # 循环次数
    d += struct.pack(">H", 0) * 4                    # 生产日期/均衡低/高/保护状态
    d += bytes([0x01])                               # 软件版本
    d += bytes([rsoc])                               # RSOC %
    d += bytes([fet_status])                         # FET 状态
    d += bytes([16])                                 # 电芯数
    d += bytes([4])                                  # NTC 数
    for t in (25.0, 26.0, 27.0, 28.0):
        d += struct.pack(">H", int((t + 273.15) * 10))
    return bytes(d)


# ── 模拟器主体 ──────────────────────────────────────────────────────

@dataclass
class SimState:
    rsoc: int = 80
    fet_status: int = 0x00  # bit0=charge MOS / bit1=discharge MOS; 1=open(关断)
    illuminance: float = 1000.0  # lux, S1 用
    gpio_states: dict[int, int] = field(default_factory=dict)  # pin -> level


class ESP32Simulator:
    def __init__(self, node_id: str, broker_host: str, broker_port: int):
        self.node_id = node_id
        self.up_topic = f"nodes/{node_id}/up"
        self.down_topic = f"nodes/{node_id}/down"
        self.ctrl_topic = f"nodes/{node_id}/control"
        self.state = SimState()
        self.request_counter = 0
        self.rx_log: queue.Queue = queue.Queue()  # 记录接收的下行帧

        self.client = mqtt.Client(
            callback_api_version=mqtt.CallbackAPIVersion.VERSION2,
            client_id=node_id,  # ACL: clientid == node_id
            clean_session=True,
        )
        self.client.on_connect = self._on_connect
        self.client.on_message = self._on_message
        self.client.on_disconnect = self._on_disconnect
        self.broker_host = broker_host
        self.broker_port = broker_port

    def _on_connect(self, client, userdata, flags, reason_code, properties=None):
        log.info(f"MQTT connected rc={reason_code}; subscribing {self.down_topic}/# {self.ctrl_topic}")
        client.subscribe(self.down_topic, qos=1)
        client.subscribe(self.ctrl_topic, qos=1)

    def _on_disconnect(self, client, userdata, flags, reason_code, properties=None):
        log.warning(f"MQTT disconnected rc={reason_code}")

    def _on_message(self, client, userdata, msg):
        try:
            msg_type, fields = decode(msg.payload)
        except Exception as e:
            log.error(f"decode fail topic={msg.topic}: {e}; hex={msg.payload.hex()}")
            return
        log.info(f"RX topic={msg.topic} type=0x{msg_type:02x} fields={[(f.num, f.wire, f.value if not isinstance(f.value, bytes) else f.value.hex()) for f in fields]}")
        self.rx_log.put((msg.topic, msg_type, fields, msg.payload))
        if msg_type == MSG_PERIPH_CMD:
            self._handle_periph_cmd(fields)
        elif msg_type == MSG_CHANNEL_CMD_V2:
            self._handle_channel_cmd_v2(fields)

    # ── PeriphCmd 应答 ──
    def _handle_periph_cmd(self, fields: list[Field]):
        f = {f.num: f.value for f in fields}
        req_id = f.get(1, 0)
        periph_type = f.get(2, 0)
        resource_id = f.get(3, 0)
        action = f.get(4, 0)
        value = f.get(5, 0)
        log.info(f"  >> PeriphCmd req_id={req_id} type={periph_type} res={resource_id} action={action} val={value}")
        # GPIO: periph_type=1, action 0=low / 1=high
        if periph_type == 1:
            self.state.gpio_states[resource_id] = 1 if action == 1 else 0
            log.info(f"     GPIO[{resource_id}] -> {self.state.gpio_states[resource_id]}")
        # 应答 PeriphRsp (0x1C): request_id / success / value / periph_type / resource_id / action
        # (后端必备字段: 1, 2, 5, 6, 7)
        resp = bytes([MSG_PERIPH_RSP]) + ev(1, req_id) + ev(2, 1) + ev(3, value) + ev(5, periph_type) + ev(6, resource_id) + ev(7, action)
        self.client.publish(self.up_topic, resp, qos=1)
        log.info(f"  << PeriphRsp req_id={req_id} success=1")

    # ── ChannelCmdV2 应答（BMS set_mos_policy 链路）──
    def _handle_channel_cmd_v2(self, fields: list[Field]):
        f = {f.num: f.value for f in fields}
        cmd_id = f.get(1, b"\x00" * 16)
        digest = f.get(2, b"\x00" * 16)
        attempt = f.get(3, 0)
        boot_id = f.get(4, b"").decode(errors="replace") if isinstance(f.get(4), bytes) else ""
        edge_device_id = f.get(5, 0)
        channel_id = f.get(6, 0)
        tx_data = f.get(8, b"")
        # Plan 在 field 15 (repeated sub-message). 注意 decoder 对 repeated field 的返回.
        plan_blobs = [f_.value for f_ in fields if f_.num == 15]
        log.info(f"  >> ChannelCmdV2 edge={edge_device_id} ch={channel_id} tx={tx_data.hex() if isinstance(tx_data, bytes) else tx_data} plan_steps={len(plan_blobs)}")

        # 逐 step 执行业务: 收集每个 step 的 raw response
        step_responses: list[tuple[int, bytes]] = []  # (kind, response_bytes)
        targets = plan_blobs if plan_blobs else [None]  # 无 plan 时按 single-step 处理
        for step_blob in targets:
            if step_blob is None:
                step_tx = tx_data
                step_kind = 0
            else:
                _, sub_fields = decode(step_blob, start_offset=0)  # 子消息无 msg_type 头
                sf = {sf_.num: sf_.value for sf_ in sub_fields}
                step_kind = sf.get(1, 0)
                step_tx = sf.get(2, b"")
            resp = b""
            if isinstance(step_tx, bytes) and len(step_tx) >= 4 and step_tx[0] == 0xDD:
                if step_tx[1] == 0x5A:  # ESP32 -> BMS write
                    jbd_cmd = step_tx[2]
                    if jbd_cmd == 0xE1:
                        if len(step_tx) >= 6:
                            # E1 写帧 bit0=charge_close / bit1=discharge_close
                            # fet_status 反向语义: bit0=charge_open / bit1=discharge_open
                            close_bits = step_tx[5] & 0x03
                            self.state.fet_status = (~close_bits) & 0x03
                            log.info(f"     BMS MOS set: close_bits=0x{close_bits:02x} → fet_status=0x{self.state.fet_status:02x}")
                        resp = jbd_response(0xE1, b"")
                    else:
                        resp = jbd_response(jbd_cmd, b"")
                elif step_tx[1] == 0xA5:  # ESP32 -> BMS read
                    jbd_cmd = step_tx[2]
                    if jbd_cmd == 0x03:
                        data = jbd_basic_info(rsoc=self.state.rsoc, fet_status=self.state.fet_status)
                        resp = jbd_response(0x03, data)
                    else:
                        resp = jbd_response(jbd_cmd, b"")
            step_responses.append((step_kind, resp))

        # 打包 batch envelope: N(1B) + per step kind(1B) + length_le(2B) + response bytes
        # (后端 decodeBatchPlanEnvelope 期望此格式; 无 plan 的 single-step 也走 batch N=1)
        import struct
        raw_response = bytes([len(step_responses)])
        for kind, resp in step_responses:
            raw_response += bytes([kind]) + struct.pack('<H', len(resp)) + resp

        # 应答 Ack (0x16) — 简化: 直接 Final (0x17) 一帧终止
        resp = bytes([MSG_CHANNEL_CMD_V2_FINAL])
        resp += eb(1, cmd_id if isinstance(cmd_id, bytes) else bytes(16))
        resp += eb(2, digest if isinstance(digest, bytes) else bytes(16))
        resp += ev(3, attempt)
        resp += es(4, boot_id)
        resp += ev(5, 1)  # event_sequence
        resp += ev(6, 1)  # success=true
        resp += ev(7, 0)  # error_code=0
        if raw_response:
            resp += eb(8, raw_response)
        resp += ev(9, 0)  # replayed=false
        self.client.publish(self.up_topic, resp, qos=1)
        log.info(f"  << ChannelCmdV2Final success=1 steps={len(step_responses)} raw={raw_response.hex()}")

    # ── 上报 ──
    def next_request_id(self) -> int:
        self.request_counter += 1
        return self.request_counter

    def send_status_report(self):
        """MsgStatusRpt (0x02): 让节点保持 online.
        字段 (handler_status.go):
          1=uptime_sec varint / 2=status string / 3=channel_count varint /
          4=config_epoch varint / 5=sync_state varint (0=idle) /
          6=config_hash string / 8=sync_id string
        必备字段: 1, 2, 5 (seen check)."""
        payload = bytes([MSG_STATUS_RPT])
        payload += ev(1, int(time.time()) % 86400)   # uptime_sec
        payload += es(2, "online")                   # status
        payload += ev(3, 1)                          # channel_count
        payload += ev(4, 0)                          # config_epoch
        payload += ev(5, 0)                          # sync_state=idle
        payload += es(6, "sim-v1")                   # config_hash
        self.client.publish(self.up_topic, payload, qos=1)
        log.info(f"TX StatusRpt")

    def send_resource_report(self):
        """MsgResourceRpt (0x19): 上报 command_engine 能力, 刷新 resource_reported_at.
        字段 (handler_resources.go):
          1=platform string / 2=resource_count varint / 3=buses blob / 4=channels blob /
          9=command_engine sub-message
        buses blob: 子消息 repeated field — 1=UART entry, 4=GPIO entry, 6=PWM entry 等
        channels blob: 子消息 repeated field 1=channel_entry {1=id, 2=bus_type(1=UART), 3=hw_id, 5=enabled}
        """
        # 内部子消息 command_engine
        eng = b""
        eng += ev(1, 2)              # revision=2
        eng += es(2, "SIM-BOOT-001") # boot_id
        eng += ev(3, 1)              # supports_channel_cmd_v2
        eng += ev(4, 1)              # supports_bounded_batch
        eng += ev(5, 1)              # supports_finally
        eng += ev(6, 8)              # max_batch_steps
        eng += ev(7, 128)            # max_tx_bytes
        eng += ev(8, 256)            # max_rx_bytes
        eng += ev(9, 30000)          # max_step_timeout_ms
        eng += ev(10, 4)             # ram_dedup_entries

        # buses blob: 一个 UART entry + 一个 GPIO entry (匹配 gpio_configs pin=5)
        uart_entry = es(1, "UART0") + ev(2, 0) + ev(3, 17) + ev(4, 16) + ev(5, 115200)
        gpio_entry = es(1, "gpio5") + ev(2, 5)
        buses = eb(1, uart_entry) + eb(4, gpio_entry)

        # channels blob: channel_id=2 (与 DB 里 channels.id 一致, bus_type=1=UART, hw_id=0)
        channel_entry = ev(1, 2) + ev(2, 1) + ev(3, 0) + ev(5, 1)
        channels = eb(1, channel_entry)

        payload = bytes([MSG_RESOURCE_RPT])
        payload += es(1, "esp32c6")          # platform
        payload += ev(2, 2)                  # resource_count (uart + gpio)
        payload += eb(3, buses)              # buses blob
        payload += eb(4, channels)           # channels blob
        payload += eb(9, eng)                # command_engine sub-message
        self.client.publish(self.up_topic, payload, qos=1)
        log.info(f"TX ResourceRpt")

    def send_bms_data_report(self, edge_device_id: int = 1, rsoc: int | None = None, fet_status: int | None = None, current_a: float = 2.0):
        """MsgDataRpt (0x03) 携带嘉佰达 0x03 响应 raw — 触发 BMS 解析 + automation 求值."""
        if rsoc is not None:
            self.state.rsoc = rsoc
        if fet_status is not None:
            self.state.fet_status = fet_status

        # BMS 0x03 响应帧
        jbd_data = jbd_basic_info(rsoc=self.state.rsoc, fet_status=self.state.fet_status, current_a=current_a)
        jbd_frame = jbd_response(0x03, jbd_data)

        # DataRpt 字段: 1=channel_id, 2=timestamp, 3=sequence, 4=raw_data,
        #                5=error_code, 6=request_id, 7=edge_device_id
        req_id = self.next_request_id()
        payload = bytes([MSG_DATA_RPT])
        payload += ev(1, 1)                          # channel_id=1 (UART0)
        payload += ev(2, int(time.time()))
        payload += ev(3, req_id)                     # sequence
        payload += eb(4, jbd_frame)                  # raw_data
        payload += ev(5, 0)                          # error_code
        payload += ev(6, req_id)                     # request_id
        payload += ev(7, edge_device_id)             # edge_device_id=1 (测试BMS)
        self.client.publish(self.up_topic, payload, qos=1)
        log.info(f"TX DataRpt edge={edge_device_id} rsoc={self.state.rsoc} fet=0x{self.state.fet_status:02x} raw={jbd_frame.hex()[:40]}...")

    def send_custom_data_report(self, edge_device_id: int, channel_id: int, raw_data: bytes):
        """通用 DataRpt."""
        req_id = self.next_request_id()
        payload = bytes([MSG_DATA_RPT])
        payload += ev(1, channel_id)
        payload += ev(2, int(time.time()))
        payload += ev(3, req_id)
        payload += eb(4, raw_data)
        payload += ev(5, 0)
        payload += ev(6, req_id)
        payload += ev(7, edge_device_id)
        self.client.publish(self.up_topic, payload, qos=1)
        log.info(f"TX DataRpt edge={edge_device_id} raw={raw_data.hex()}")

    def run(self):
        self.client.connect(self.broker_host, self.broker_port, keepalive=30)
        self.client.loop_start()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--node-id", default="SIM-ESP32-01")
    ap.add_argument("--broker", default="127.0.0.1:1883")
    ap.add_argument("--verbose", "-v", action="store_true")
    ap.add_argument("--rsoc", type=int, default=None, help="周期上报的 SOC 值 (默认不上报)")
    ap.add_argument("--edge-id", type=int, default=7053, help="BMS edge_device_id")
    ap.add_argument("--interval", type=int, default=10, help="上报间隔秒")
    args = ap.parse_args()

    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    host, port = args.broker.split(":")
    sim = ESP32Simulator(args.node_id, host, int(port))
    sim.run()
    log.info(f"simulator running; node_id={args.node_id} up={sim.up_topic} ctrl={sim.ctrl_topic}")

    # 周期心跳 + 数据上报
    last_status = 0.0
    last_resource = 0.0
    last_data = 0.0
    try:
        while True:
            now = time.time()
            if now - last_status >= 30:
                sim.send_status_report()
                last_status = now
            # ResourceRpt 每 2min 刷一次。口径来源：commandexec.MaxCapabilityAge
            # = resourceReportInterval(10min) + capabilityReportMargin(5min) = 15min
            # (backend/internal/commandexec/channel_cmd_v2_transport.go)。2min << 15min，
            # 留足裕量，避免仿真节点被误判为 capability_stale。
            if now - last_resource >= 120:
                sim.send_resource_report()
                last_resource = now
            if args.rsoc is not None and now - last_data >= args.interval:
                sim.send_bms_data_report(edge_device_id=args.edge_id, rsoc=args.rsoc)
                last_data = now
            time.sleep(0.5)
    except KeyboardInterrupt:
        log.info("exit")
    return 0


if __name__ == "__main__":
    sys.exit(main())
