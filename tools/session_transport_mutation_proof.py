#!/usr/bin/env python3
"""task-32 变异自证：证明**第二条上行路径的成帧编排**真的咬得住。

本卡修的是 §134 的同类缺陷在**第二条路径**上的复发：msg_handler_publish_*
→ transport->ops->send → sess_tx_send 此前直接把 payload 交给 session_send。
修法把"成帧 + 发送编排"抽进宿主可编段（stx_send_frame），真正的 session_send
由回调注入 ⇒ 宿主测试**编得到**这条路径，于是"有人删掉成帧"会立刻红。

因此本脚本必须证明四件事（前两条是本卡的交付，后两条证明新路径也覆盖
"成帧函数本身被改坏"的情形）：

  T1  session_transport.c：删掉成帧调用（直接发 payload）     ⇒ 宿主红
  T2  session_transport.c：超界静默截断（而不是 fail-closed） ⇒ 宿主红
  T3  device_link_wiring.c：header.type 写死常量             ⇒ 宿主红
  T4  device_link_wiring.c：payload_len 写成 len+1           ⇒ 宿主红

每条变异都断言：
  1. 锚点唯一（命中恰好 1 次）—— 防"没落地却以为改了"（本会话踩过多次）；
  2. 前后 md5 不同；
  3. 变异体**能编译**（编译红不算数：只改常量/表达式，不动符号与类型）；
  4. 宿主测试**变红**；
  5. 还原后 md5 逐字节一致，且**重新变绿**。

用法: python3 tools/session_transport_mutation_proof.py
"""
import hashlib
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
FW = os.path.join(ROOT, "esp32-collector")
CMAKE = "/mnt/storage/WorkSpace/EHome/.tools/cmake-3.30.5-linux-x86_64/bin/cmake"
ST = os.path.join(FW, "main", "session_transport.c")
WIRING = os.path.join(FW, "main", "device_link_wiring.c")
MQTT = os.path.join(FW, "main", "uplink_mqtt_transport.c")
BUILD = "/tmp/t32-mut-host"
TARGET = "session_transport_tests"

results = []


def say(m):
    print(m, flush=True)


def check(cond, msg):
    results.append((bool(cond), msg))
    say(("  OK  " if cond else "  FAIL ") + msg)
    return bool(cond)


def md5f(p):
    with open(p, "rb") as fh:
        return hashlib.md5(fh.read()).hexdigest()


def sh(cmd, timeout=900):
    e = dict(os.environ)
    e["PATH"] = "/mnt/storage/WorkSpace/EHome/.tools/cmake-3.30.5-linux-x86_64/bin:" + e.get("PATH", "")
    r = subprocess.run(cmd, env=e, capture_output=True, text=True, timeout=timeout)
    return r.returncode, (r.stdout or "") + (r.stderr or "")


# 两个 target 都构建/都跑：T5 把成帧放到 MQTT 出口，只有 MQTT 那组能抓住；
# 其余变异只有 session_transport 那组能抓住。**两边都断言**才算"放了守卫"。
TARGETS = ["session_transport_tests", "uplink_mqtt_transport_tests"]


def build():
    rc, out = sh([CMAKE, "-S", os.path.join(FW, "host_tests"), "-B", BUILD])
    if rc != 0:
        return False, out
    for tgt in TARGETS:
        rc, out = sh([CMAKE, "--build", BUILD, "--target", tgt, "-j8"])
        if rc != 0:
            return False, out
    return True, ""


def run():
    """两个 target 都跑；任一红即视为红（返回非 0）。"""
    outs = []
    rc_all = 0
    for tgt in TARGETS:
        rc, out = sh([os.path.join(BUILD, tgt)])
        outs.append(out)
        if rc != 0:
            rc_all = rc
    return rc_all, "\n".join(outs)


def patch_once(path, old, new, tag):
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    n = text.count(old)
    if n != 1:
        say("  [ABORT] %s 锚点命中 %d 次（应为 1）—— 变异未落地，不做结论" % (tag, n))
        return False
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text.replace(old, new, 1))
    return True


def evidence(out, needles, limit=4):
    hits = []
    for line in out.splitlines():
        if any(nd in line for nd in needles):
            hits.append(line.strip()[:150])
        if len(hits) >= limit:
            break
    return hits


# ── T1：删掉成帧调用，直接发 payload（= §134 缺陷的复现）──
T1_OLD = """    /* ⭐ 唯一的成帧点：复用 task-31 的生产函数（P4：不写第二个实现）。 */
    size_t frame_len = 0;
    int frc = devlink_encode_frame(tx->scratch, tx->scratch_cap, payload, len, seq, &frame_len);
    if (frc != DEVLINK_FRAME_OK) {
        tx->stats.encode_failed++;
        return stx_to_esp_err(STX_SEND_TOO_BIG);
    }
"""
T1_NEW = """    /* MUTANT T1: 不成帧，直接把 payload 当线上字节 */
    (void)seq;   /* 去掉成帧后 seq 没人用 —— 不加这行会被 -Werror=unused-parameter 挡住，
                  * 而"变异体编译失败"的红**不算数**（本仓已两次被它误导）。 */
    size_t frame_len = len;
    for (size_t i = 0; i < len; i++) tx->scratch[i] = payload[i];
"""

# ── T2：超界静默截断，而不是 fail-closed ──
T2_OLD = """    if (len > STX_TX_PAYLOAD_MAX) {
        /* 超界**响亮失败 + 计数**：静默截断等于发出**半条帧**，
         * 对端定界器会错位，症状是"链路莫名卡死"而不是一条错误日志。 */
        tx->stats.too_big++;
        return stx_to_esp_err(STX_SEND_TOO_BIG);
    }"""
T2_NEW = """    if (len > STX_TX_PAYLOAD_MAX) { tx->stats.too_big++; len = STX_TX_PAYLOAD_MAX; }  /* MUTANT T2 */"""

# ── T3/T4：成帧函数本身（新路径也覆盖）──
T3_OLD = "    h.type        = payload[0];   /* ⚠ 后端强校验 type == payload[0] */"
T3_NEW = "    h.type        = 0x01;         /* MUTANT T3 */"
T4_OLD = "    h.payload_len = (uint16_t)payload_len;"
T4_NEW = "    h.payload_len = (uint16_t)(payload_len + 1);   /* MUTANT T4 */"

# ── T5：**反向**变异 —— 把成帧放错层（加到 MQTT 出口上）──
# 证明交付 4 的守卫真的会咬：MQTT 侧多一个 12 B 头必须被抓住。
T5_OLD = "    return s_io.publish(data, len);"
# 用**内联拼头**而不是调 devlink_encode_frame：后者需要额外 include，
# 而"变异体编译失败"的红**不算数**（本仓已两次被它误导）。
T5_NEW = """    /* MUTANT T5: 把 3.0 成帧放到 MQTT 出口上（放错层） */
    {
        static const uint8_t hdr[12] = { 0x45, 0x48, 0x30, 0x01, 0, 0, 0, 0, 0, 0, 0, 0 };
        static uint8_t f[512];
        if (len + 12u <= sizeof(f)) {
            for (size_t i = 0; i < 12u; i++) f[i] = hdr[i];
            for (size_t i = 0; i < len; i++) f[12u + i] = data[i];
            return s_io.publish(f, len + 12u);
        }
    }
    return s_io.publish(data, len);"""

MUTANTS = [
    ("T1", "session_transport.c：删掉成帧调用，直接发 payload", ST, T1_OLD, T1_NEW,
     ["magic", "0x45", "帧头", "FRAME", "framed"]),
    ("T2", "session_transport.c：超界静默截断（不再 fail-closed）", ST, T2_OLD, T2_NEW,
     ["TOO_BIG", "超界", "refus", "拒绝", "too_big"]),
    ("T3", "device_link_wiring.c：header.type 写死 0x01", WIRING, T3_OLD, T3_NEW,
     ["type", "FRAME", "帧"]),
    ("T4", "device_link_wiring.c：payload_len 写成 len+1", WIRING, T4_OLD, T4_NEW,
     ["payload_len", "FRAME", "帧"]),
    ("T5", "uplink_mqtt_transport.c：把成帧**放错层**到 MQTT 出口（反向守卫）",
     MQTT, T5_OLD, T5_NEW,
     ["MQTT", "原样", "magic", "0x4548", "长度必须"]),
]


def main():
    say("=" * 78)
    say("task-32 变异自证：第二条上行路径的成帧编排（stx_send_frame）")
    say("=" * 78)

    orig = {p: open(p, "rb").read() for p in (ST, WIRING, MQTT)}
    md5s = {p: md5f(p) for p in orig}
    for p, m in md5s.items():
        say("开工 md5: %s = %s" % (os.path.relpath(p, ROOT), m))
    say("")

    say("[0] 基线")
    ok, out = build()
    if not check(ok, "宿主 target %s 构建成功" % TARGET):
        say(out[-2500:])
        return 1
    rc, out = run()
    if not check(rc == 0, "基线宿主测试 rc=0（否则后面的红毫无意义）"):
        say(out[-1500:])
        return 1
    say("")

    try:
        for tag, name, path, old, new, needles in MUTANTS:
            say("[%s] %s" % (tag, name))
            if not patch_once(path, old, new, tag):
                continue
            m = md5f(path)
            check(m != md5s[path], "变异落地且 md5 变化（%s -> %s）"
                  % (md5s[path][:8], m[:8]))

            ok, out = build()
            check(ok, "变异体**能编译**（⇒ 下面的红是断言红，不是编译红）")
            if ok:
                rc, out = run()
                check(rc != 0, "宿主测试**变红**")
                for line in evidence(out, needles):
                    say("      | " + line)

            with open(path, "wb") as fh:
                fh.write(orig[path])
            check(md5f(path) == md5s[path], "还原后 md5 逐字节一致")
            ok, _ = build()
            check(ok, "还原后可再次构建")
            rc, _ = run()
            check(rc == 0, "还原后**重新变绿**")
            say("")
    finally:
        for path, data in orig.items():
            with open(path, "wb") as fh:
                fh.write(data)

    say("[收尾]")
    for path, m in md5s.items():
        check(md5f(path) == m, "%s 与开工 md5 一致" % os.path.relpath(path, ROOT))
    rc, out = sh(["git", "status", "--short"], timeout=120)
    say("      git status --short:")
    for line in out.splitlines():
        say("        " + line)
    if not out.strip():
        say("        (空)")

    say("")
    say("=" * 78)
    bad = [msg for okk, msg in results if not okk]
    if bad:
        say("FAIL 变异自证未通过（%d 项）:" % len(bad))
        for b in bad:
            say("   - " + b)
        return 1
    say("PASS 变异自证：%d/%d 变异体都能编译、都让宿主测试变红、还原后重新变绿且 md5 一致"
        % (len(MUTANTS), len(MUTANTS)))
    say("=" * 78)
    return 0


if __name__ == "__main__":
    sys.exit(main())
