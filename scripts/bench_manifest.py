#!/usr/bin/env python3
"""台架 manifest 取证 / 注入工具（task-3 第 2 步）。

背景与边界
----------
第 2 步要在一条通道上证明"链路够宽时固件能跑 100 Hz"。这需要一份改过
ch8（UART0）bus_config 波特率与 interval_ms 的 ConfigManifest，而现场 manifest
由生产后端（192.168.20.6）下发、我们无法改后端。因此本工具：

  capture  -- 复位 S3，抓生产后端下发的真实 manifest（ground truth），存 JSON。
  inject   -- 把"只改必要字段"的 manifest 发到 nodes/<node>/down，设备事务化应用。
  restore  -- 复位 S3，让生产后端重新下发真实 manifest（设备 NVS 不存配置，
              重启后 in-memory 为空 -> Hello nvs_has=0 -> 服务端必推），并打印验证要点。

为什么 inject 必须复用现场 manifest_id：后端 SyncGate.decide() 用
deviceHash == serverHash.ManifestID 判 hash_match；若我们用一个新的 manifest_id，
设备下一次 StatusReport（1 Hz）就会报出不同的 config_hash，后端在去重窗口
（5 s）后立即重推它自己的 manifest，把台架配置冲掉。复用同一个 manifest_id 才能
让台架配置稳定保持。代价是设备期间持有的 manifest_id 与后端内容不一致 —— 这是
台架限定的偏离，恢复方式就是复位设备（restore）。

不改固件源码 / 后端源码 / 生产容器；只往设备订阅的 down topic 发一帧。
"""

from __future__ import annotations

import argparse
import json
import sys
import time

try:
    import paho.mqtt.client as mqtt
except ImportError:
    sys.stderr.write("需要 paho-mqtt\n")
    raise SystemExit(2)


# ------------------------------------------------------------------ TLV
def enc_varint(v: int) -> bytes:
    out = bytearray()
    while v > 0x7F:
        out.append((v & 0x7F) | 0x80)
        v >>= 7
    out.append(v & 0x7F)
    return bytes(out)


def dec_varint(b, p):
    v = s = 0
    while True:
        x = b[p]; p += 1
        v |= (x & 0x7F) << s
        if not (x & 0x80):
            return v, p
        s += 7


def decode_fields(b):
    p = 0
    out = []
    while p < len(b):
        tag, p = dec_varint(b, p)
        fn, w = tag >> 3, tag & 7
        if w == 0:
            val, p = dec_varint(b, p)
        elif w == 2:
            ln, p = dec_varint(b, p)
            val = b[p:p + ln]; p += ln
        else:
            break
        out.append((fn, w, val))
    return out


def decode_manifest(frame: bytes) -> dict:
    """把 ConfigManifest 帧解成结构化 dict（供人读 + 供 inject 复用）。"""
    assert frame[0] == 0x04, "not a ConfigManifest: %#x" % frame[0]
    out = {"manifest_id": None, "sync_id": None, "templates": [], "channels": [],
           "dma_channels": [], "log_stream": None, "gpio": [], "pwm": [], "raw_len": len(frame)}
    for fn, w, val in decode_fields(frame[1:]):
        if fn == 1 and w == 2:
            out["manifest_id"] = val.decode()
        elif fn == 8 and w == 2:
            out["sync_id"] = val.decode()
        elif fn == 3 and w == 2:
            t = {}
            for tf, tw, tv in decode_fields(val):
                key = {1: "id", 2: "write_data", 3: "read_length", 4: "delay_ms"}.get(tf, "f%d" % tf)
                t[key] = tv.hex() if tw == 2 else tv
            out["templates"].append(t)
        elif fn == 4 and w == 2:
            ch = {"edge_devices": []}
            for cf, cw, cv in decode_fields(val):
                if cf == 1:
                    ch["id"] = cv
                elif cf == 2:
                    ch["hardware_id"] = cv
                elif cf == 3:
                    ch["template_ids"] = ch.get("template_ids", []) + [cv]
                elif cf == 4:
                    ch["interval_ms"] = cv
                elif cf == 5:
                    ch["enabled"] = cv
                elif cf == 6:
                    ch["bus_type"] = cv
                elif cf == 7:
                    ch["bus_config"] = cv.hex()
                elif cf == 8:
                    ch["dma_enabled"] = cv
                elif cf == 9:
                    ed = {}
                    cmds = []
                    for ef, ew, ev in decode_fields(cv):
                        if ef == 1:
                            ed["edge_device_id"] = ev
                        elif ef == 2:
                            ed["hardware_id"] = ev
                        elif ef == 3:
                            c = {}
                            for mf, mw, mv in decode_fields(ev):
                                c[{1: "template_id", 2: "interval_ms", 3: "enabled"}.get(mf, "f%d" % mf)] = mv
                            cmds.append(c)
                    ed["commands"] = cmds
                    ch["edge_devices"].append(ed)
            out["channels"].append(ch)
        elif fn == 9 and w == 2:
            out["log_stream"] = {"raw": val.hex()}
        elif fn == 11 and w == 2:
            out["gpio"].append(val.hex())
        elif fn == 12 and w == 2:
            out["pwm"].append(val.hex())
        elif fn == 10 and w == 2:
            out["dma_channels"].append(val.hex())
    return out


# ------------------------------------------------------------------ MQTT
class DownCapture:
    def __init__(self, node, broker, port):
        self.node = node
        self.broker = broker
        self.port = port
        self.frames = []
        self.client = None
        self.connected = False

    def on_connect(self, c, u, f, rc, p=None):
        self.connected = True
        # ConfigManifest 由后端发到 **control** topic（sender.go:614
        # mqtt.ControlTopicForNode + PublishQoS2），不是 down —— 两者都订阅。
        c.subscribe("nodes/%s/#" % self.node, qos=1)
        print("[capture] connected, subscribed nodes/%s/#" % self.node, flush=True)

    def on_message(self, c, u, msg):
        if msg.payload and msg.payload[0] == 0x04:
            self.frames.append(bytes(msg.payload))
            print("[capture] ConfigManifest received: %d B" % len(msg.payload), flush=True)

    def start(self):
        self.client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2,
                                  client_id="bench-capture-%d" % int(time.time()))
        self.client.on_connect = self.on_connect
        self.client.on_message = self.on_message
        self.client.connect(self.broker, self.port, keepalive=30)
        self.client.loop_start()
        for _ in range(100):
            if self.connected:
                break
            time.sleep(0.1)

    def stop(self):
        try:
            self.client.loop_stop(); self.client.disconnect()
        except Exception:
            pass


def reset_device(port):
    """按验收矩阵：DTR 释放、RTS(EN) 拉低再放开 -> 正常启动（非下载模式）。"""
    import serial
    s = serial.Serial(port, 115200, timeout=0.2)
    time.sleep(0.25)
    s.setDTR(False)
    s.setRTS(True)
    time.sleep(0.15)
    s.setRTS(False)
    s.close()


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    cap = sub.add_parser("capture")
    cap.add_argument("--node", required=True)
    cap.add_argument("--broker", default="192.168.20.6")
    cap.add_argument("--console", help="S3 控制台 by-id；给了就在订阅后复位它")
    cap.add_argument("--timeout", type=float, default=90.0)
    cap.add_argument("--out", required=True)

    inj = sub.add_parser("inject")
    inj.add_argument("--node", required=True)
    inj.add_argument("--broker", default="192.168.20.6")
    inj.add_argument("--manifest-json", required=True, help="capture 产出的 JSON")
    inj.add_argument("--set", action="append", default=[],
                     help="channel_id:field=value，如 8:baud=115200 8:interval_ms=10")
    inj.add_argument("--dry-run", action="store_true")

    res = sub.add_parser("restore")
    res.add_argument("--node", required=True)
    res.add_argument("--console", required=True)
    res.add_argument("--broker", default="192.168.20.6")
    res.add_argument("--wait", type=float, default=30.0)
    res.add_argument("--out", required=True)

    args = ap.parse_args()
    if args.cmd == "capture":
        return cmd_capture(args)
    if args.cmd == "inject":
        return cmd_inject(args)
    return cmd_restore(args)


def cmd_capture(args):
    c = DownCapture(args.node, args.broker, 1883)
    c.start()
    if args.console:
        print("[capture] resetting device ...", flush=True)
        reset_device(args.console)
    t0 = time.time()
    while time.time() - t0 < args.timeout and not c.frames:
        time.sleep(0.5)
    c.stop()
    if not c.frames:
        print("ERROR: no ConfigManifest captured", file=sys.stderr)
        return 1
    frame = c.frames[-1]
    decoded = decode_manifest(frame)
    decoded["hex"] = frame.hex()
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(decoded, fh, ensure_ascii=False, indent=2)
    print(json.dumps(decoded, ensure_ascii=False, indent=2))
    print("[capture] wrote %s" % args.out)
    return 0


def _rebuild(frame_hex: str, sets: dict) -> bytes:
    """按 channel_id 重写 bus_config 的 baud / interval_ms，其余字段原样保留。"""
    frame = bytes.fromhex(frame_hex)
    assert frame[0] == 0x04
    out = bytearray([0x04])
    for fn, w, val in decode_fields(frame[1:]):
        if fn == 4 and w == 2:
            ch_id = None
            parts = []
            for cf, cw, cv in decode_fields(val):
                if cf == 1:
                    ch_id = cv
                parts.append((cf, cw, cv))
            mod = sets.get(ch_id)
            ch = bytearray()
            for cf, cw, cv in parts:
                if mod and cf == 7:
                    bc = bytearray(cv)
                    if "baud" in mod:
                        bc[2:6] = int(mod["baud"]).to_bytes(4, "big")
                    cv = bytes(bc); cw = 2
                if mod and cf == 4 and "interval_ms" in mod:
                    cv = int(mod["interval_ms"]); cw = 0
                if mod and cf == 9 and "cmd_interval_ms" in mod:
                    ed = bytearray()
                    for ef, ew, ev in decode_fields(cv):
                        if ef == 3 and ew == 2:
                            cmd = bytearray()
                            for mf, mw, mv in decode_fields(ev):
                                if mf == 2:
                                    mv = int(mod["cmd_interval_ms"]); mw = 0
                                cmd += enc_varint((mf << 3) | mw)
                                cmd += enc_varint(mv) if mw == 0 else enc_varint(len(mv)) + mv
                            ev = bytes(cmd)
                        ed += enc_varint((ef << 3) | ew)
                        ed += enc_varint(ev) if ew == 0 else enc_varint(len(ev)) + ev
                    cv = bytes(ed)
                ch += enc_varint((cf << 3) | cw)
                ch += enc_varint(cv) if cw == 0 else enc_varint(len(cv)) + cv
            out += enc_varint((fn << 3) | 2) + enc_varint(len(ch)) + ch
        else:
            out += enc_varint((fn << 3) | w)
            out += enc_varint(val) if w == 0 else enc_varint(len(val)) + val
    return bytes(out)


def cmd_inject(args):
    data = json.load(open(args.manifest_json))
    sets = {}
    for spec in args.set:
        ch, rest = spec.split(":", 1)
        k, v = rest.split("=", 1)
        sets.setdefault(int(ch), {})[k] = v
    new = _rebuild(data["hex"], sets)
    print("[inject] original %d B -> new %d B; sets=%s" % (len(data["hex"]) // 2, len(new), sets))
    print("[inject] manifest_id kept=%s" % decode_manifest(new)["manifest_id"])
    print("[inject] new hex: %s" % new.hex())
    if args.dry_run:
        print(json.dumps(decode_manifest(new), ensure_ascii=False, indent=2))
        return 0
    c = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2, client_id="bench-inject-%d" % int(time.time()))
    c.connect(args.broker, 1883, keepalive=30)
    c.loop_start()
    time.sleep(0.5)
    # 与后端一致：manifest 走 control topic（QoS1 足够；后端用 QoS2）。
    info = c.publish("nodes/%s/control" % args.node, new, qos=1)
    info.wait_for_publish(timeout=5)
    time.sleep(1.0)
    c.loop_stop(); c.disconnect()
    print("[inject] published")
    return 0


def cmd_restore(args):
    c = DownCapture(args.node, args.broker, 1883)
    c.start()
    print("[restore] resetting device to force a server re-push ...", flush=True)
    reset_device(args.console)
    t0 = time.time()
    while time.time() - t0 < args.wait and not c.frames:
        time.sleep(0.5)
    c.stop()
    ok = bool(c.frames)
    rec = {"restored": ok, "frames": len(c.frames),
           "manifest_id": decode_manifest(c.frames[-1])["manifest_id"] if ok else None,
           "hex_len": len(c.frames[-1]) if ok else 0}
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(rec, fh, ensure_ascii=False, indent=2)
    print(json.dumps(rec, ensure_ascii=False, indent=2))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
