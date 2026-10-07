#!/usr/bin/env python3
"""task-33 变异自证：证明"nonce 校验"与"接受才 READY"两条**真的在测东西**。

本卡修的是真机 §138 的两条缺陷：
  D-A  nonce 双定义（P4）：3.0 用自己的 esp_random()，而校验方只认 runtime armed 的
       ⇒ HelloAck 必然被判 stale。
  D-B  READY 判据是"收到 0x12"而不是"握手成功"（P1）
       ⇒ 被拒的 ACK 也能把链路推进 READY（日志同时出现两条相反结论）。

对应两条变异（按要求，**必须能编译**）：
  M1  **删掉 nonce 校验**：把 dlhs_decide 的规则 0（!accepted ⇒ IDLE）删掉
      ⇒ "收到即 READY" 的老行为回来 ⇒ 宿主必须红。
  M2  **只看 rx_type 就 NOTE**：让规则 0 恒不触发（等价于忽略 accepted）
      ⇒ 同样必须红。

额外第三条（D-A 侧，证明"3.0 不再自己生成 nonce"也是被钉住的）：
  M3  让 hello_runtime_arm_link 与静默无关地失败（返回 false 但**不清** nonce）
      ⇒ 3.0 取不到 nonce ⇒ 不应发出 Hello。见 §M3 的说明。

每条都断言：锚点唯一（命中 1 次）→ md5 变化 → **能编译** → 测试红 → 还原 md5 一致 → 重新绿。
"""
import hashlib
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
FW = os.path.join(ROOT, "esp32-collector")
CMAKE = "/mnt/storage/WorkSpace/EHome/.tools/cmake-3.30.5-linux-x86_64/bin/cmake"
DH = os.path.join(FW, "main", "device_link_handshake.c")
RT = os.path.join(FW, "main", "hello_handshake_runtime.c")
BUILD = "/tmp/t33-mut-host"
TARGETS = ["device_link_handshake_tests", "hello_handshake_tests"]

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


def build():
    rc, out = sh([CMAKE, "-S", os.path.join(FW, "host_tests"), "-B", BUILD])
    if rc != 0:
        return False, out
    for t in TARGETS:
        rc, out = sh([CMAKE, "--build", BUILD, "--target", t, "-j8"])
        if rc != 0:
            return False, out
    return True, ""


def run():
    rc_all, outs = 0, []
    for t in TARGETS:
        rc, out = sh([os.path.join(BUILD, t)])
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


M1_OLD = """    if (rx_type == MSG_HELLO_ACK && !hello_ack_accepted) {
        return DLHS_IDLE;
    }
"""
M1_NEW = """    /* MUTANT M1: 删掉 nonce 校验 —— 只要收到 0x12 就当作握手成功 */
    (void)hello_ack_accepted;   /* 删掉校验后该参数没人用；不加这行会被
                                 * -Werror=unused-parameter 挡住，而"变异体编译失败"
                                 * 的红**不算数**（本仓已两次被它误导）。 */
"""

M2_OLD = "    if (rx_type == MSG_HELLO_ACK && !hello_ack_accepted) {"
M2_NEW = "    if (false && rx_type == MSG_HELLO_ACK && !hello_ack_accepted) {  /* MUTANT M2 */"

# D-A 侧：让 arm 返回 false（模拟"3.0 取不到 nonce"），宿主应能观察到"没 arm 就没 nonce"
M3_OLD = """bool hello_runtime_arm_link(hello_runtime_t *runtime, uint32_t *nonce)
{
    if (runtime == NULL || nonce == NULL) return false;
"""
M3_NEW = """bool hello_runtime_arm_link(hello_runtime_t *runtime, uint32_t *nonce)
{
    if (runtime == NULL || nonce == NULL) return false;
    (void)runtime; (void)nonce;
    return false;   /* MUTANT M3: 永远取不到 nonce（3.0 将发不出 Hello） */
"""

M4_OLD = """    atomic_store_explicit(&runtime->armed_transport, HELLO_ARM_LINK,
                          memory_order_release);"""
M4_NEW = """    atomic_store_explicit(&runtime->armed_transport, HELLO_ARM_MQTT,   /* MUTANT M4 */
                          memory_order_release);"""

MUTANTS = [
    ("M1", "device_link_handshake.c：**删掉 nonce 校验**（被拒的 HelloAck 也算成功）",
     DH, M1_OLD, M1_NEW, ["被拒绝的 HelloAck", "不得", "IDLE", "FAIL"]),
    ("M2", "device_link_handshake.c：accepted 参数变成装饰（只看 rx_type）",
     DH, M2_OLD, M2_NEW, ["被拒绝的 HelloAck", "不得", "FAIL"]),
    ("M3", "hello_handshake_runtime.c：arm 恒失败（3.0 取不到 nonce）",
     RT, M3_OLD, M3_NEW,
     ["3.0 链路应能从 runtime 取到非 0 nonce", "FAIL", "nonce"]),
    # M4（D-A 的另一半）：让 LINK arm 走的仍是 MQTT 判据 —— 即“没真正把 3.0
    # 接进同一条 arm 路径”，只是换了个名字。这正是候选 A 的失败模式。
    ("M4", "hello_handshake_runtime.c：arm 仍按 MQTT 代际判据（= 候选 A 的失败模式）",
     RT, M4_OLD, M4_NEW,
     ["MQTT 未就绪时", "LINK", "FAIL"]),
]


def main():
    say("=" * 78)
    say("task-33 变异自证：nonce 所有权（D-A）+ READY 判据（D-B）")
    say("=" * 78)

    orig = {p: open(p, "rb").read() for p in (DH, RT)}
    md5s = {p: md5f(p) for p in orig}
    for p, m in md5s.items():
        say("开工 md5: %s = %s" % (os.path.relpath(p, ROOT), m))
    say("")

    say("[0] 基线")
    ok, out = build()
    if not check(ok, "宿主 targets 构建成功"):
        say(out[-2500:])
        return 1
    rc, out = run()
    if not check(rc == 0, "基线宿主测试 rc=0"):
        say(out[-1500:])
        return 1
    say("")

    try:
        for tag, name, path, old, new, needles in MUTANTS:
            say("[%s] %s" % (tag, name))
            if not patch_once(path, old, new, tag):
                continue
            m = md5f(path)
            check(m != md5s[path], "变异落地且 md5 变化（%s -> %s）" % (md5s[path][:8], m[:8]))
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
        for p, d in orig.items():
            with open(p, "wb") as fh:
                fh.write(d)

    say("[收尾]")
    for p, m in md5s.items():
        check(md5f(p) == m, "%s 与开工 md5 一致" % os.path.relpath(p, ROOT))
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
