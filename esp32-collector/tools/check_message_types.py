#!/usr/bin/env python3
"""门禁：设备侧与 Go 侧的【消息类型表】必须一致。

## 为什么需要
3.0 的契约是"两端各有一套对锚测试"（设计 S0）。帧头形状已有
protocol/vectors/frame_header.txt 对锚，但**消息类型编号**没有 ——
而编号不一致的后果是**静默错路由**：
  设备发 0x22，服务端不认识 ⇒ 丢弃或当成别的消息 ⇒
  前端功能"点了没反应"，且两端各自的测试都是绿的。

## 真实案例（2026-10-06，本轮发现）
设备侧早已有 MSG_DEVICE_OP 0x22 / MSG_DEVICE_OP_ACK 0x23
（components/device_op/include/device_op.h:62-63，"远程重启/恢复出厂"），
但 `grep MsgDeviceOp backend/pkg/frame/` **零命中**。
⇒ 设备侧做完的功能在 Go 侧**无法被命名，因此无法被路由**，
端到端不可达，而没有任何一处会报错。

## 判据
设备侧所有 `#define MSG_xxx 0xNN` 的编号，必须都能在 Go 侧
`pkg/frame` 里找到**同号**的常量。
（反向不强制：Go 侧允许多出——例如 0x15-0x18 是服务端主动的
ChannelCmdV2/PongAck，设备侧不需要定义。）
"""
import os
import re
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)                 # esp32-collector
WT = os.path.dirname(ROOT)                    # worktree root

GO_FRAME = os.path.join(WT, "backend", "pkg", "frame", "frame.go")

DEV_DEF = re.compile(r"#define\s+(MSG_[A-Z0-9_]+)\s+0x([0-9A-Fa-f]+)")
GO_DEF = re.compile(r"\bMsg[A-Za-z0-9_]*\s*=\s*0x([0-9A-Fa-f]+)")


def device_msgs():
    out = {}
    for root in (os.path.join(ROOT, "components"), os.path.join(ROOT, "main")):
        if not os.path.isdir(root):
            continue
        for dirpath, dirnames, filenames in os.walk(root):
            dirnames[:] = [d for d in dirnames if d != "build"]
            for fn in filenames:
                if not fn.endswith((".h", ".c")):
                    continue
                try:
                    txt = open(os.path.join(dirpath, fn), encoding="utf-8",
                               errors="replace").read()
                except OSError:
                    continue
                for name, val in DEV_DEF.findall(txt):
                    out.setdefault(int(val, 16), set()).add(name)
    return out


def go_msgs():
    if not os.path.isfile(GO_FRAME):
        return None
    txt = open(GO_FRAME, encoding="utf-8").read()
    return {int(v, 16) for v in GO_DEF.findall(txt)}


def main():
    dev = device_msgs()
    go = go_msgs()
    if go is None:
        # ⚠ 这里**不能**返回 0。返回 0 意味着"没找到对锚的那一端 ⇒ 判它通过"，
        # 于是这个门禁在"Go 文件被改名/移动/路径算错"时会**永远报 PASS**
        # （vacuous pass）—— 它守护的恰恰是"两端编号一致"，而它自己
        # 在对锚缺失时反而放行。实测过：把 GO_FRAME 指到不存在的路径 ⇒ 原实现
        # 打印 SKIP 并返回 0。
        # 找不到对锚 = **无法判定**，不是"通过"。判为失败并说清怎么修。
        print("FAIL check_message_types: 找不到 %s" % GO_FRAME)
        print("    这个门禁靠'设备侧定义'与'Go 侧常量'两端对锚来判一致性；")
        print("    任一端读不到 ⇒ **无法判定**，不能当成通过（否则文件一改名门禁就失效）。")
        print("    请确认 backend/pkg/frame/frame.go 存在，或修正本脚本的路径推导。")
        return 2
    if not dev:
        print("FAIL 设备侧一个 MSG_ 定义都没解析到（解析器坏了？）")
        return 1

    missing = sorted(v for v in dev if v not in go)
    print("设备侧 %d 个消息类型（%d 个编号），Go 侧 %d 个编号"
          % (sum(len(n) for n in dev.values()), len(dev), len(go)))
    if missing:
        for v in missing:
            print("FAIL 0x%02X 设备侧有 %s，但 Go 侧 pkg/frame 里没有同号常量"
                  % (v, "/".join(sorted(dev[v]))))
        print()
        print("    两端编号不一致 ⇒ **静默错路由**：设备发出去，服务端认不出，")
        print("    功能端到端不可达，而两端各自的测试都是绿的。")
        print("    修法：在 backend/pkg/frame/frame.go 里补同号常量（设计 §2.5.2：")
        print("    MsgType 只能定义在 pkg/frame，不要建新包）。")
        return 1
    print("PASS check_message_types: 设备侧每个消息类型在 Go 侧都有同号常量")
    return 0


if __name__ == "__main__":
    sys.exit(main())
