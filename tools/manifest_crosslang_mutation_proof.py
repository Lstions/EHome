#!/usr/bin/env python3
"""task-30 变异自证：证明 0x04 跨语言对锚**真的会咬**。

证明方式（mutation-self-proof 的纪律）：
  1. 先确认基线是绿的（否则后面的红毫无意义）；
  2. 把两端各自的实现改坏**一点点**（Go 编码器字段号 / 固件解码器子字段号）；
  3. 该变异体**必须能编译** —— 只改常量，不动符号/类型
     （编译失败的红不算数：本仓已两次被"编译红的变异体"误导）；
  4. 对锚**必须变红**；
  5. 还原 → md5 **逐字节一致** → 对锚**必须重新变绿**。

任一环节不满足即 FAIL。特别地：
  - "变异后仍然绿" ⇒ 对锚抓不住该类漂移（假绿），必须报告；
  - "锚点命中 0 次或 >1 次" ⇒ 变异没落地，不做结论（本会话踩过）。

用法: python3 tools/manifest_crosslang_mutation_proof.py
"""
import hashlib
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
FW = os.path.join(ROOT, "esp32-collector")
BE = os.path.join(ROOT, "backend")
BUILD_DIR = os.environ.get("EHOME_FW_E2E_BUILD", "/tmp/v3-fw-e2e-build-mut")
DECODER = os.path.join(BUILD_DIR, "firmware_manifest_crosslang")
GO_FILE = os.path.join(BE, "internal", "nodemgr", "sender_snapshot.go")
C_FILE = os.path.join(FW, "components", "config_mgr", "config_mgr.c")

TOOLS = "/mnt/storage/WorkSpace/EHome/.tools"
CMAKE = os.path.join(TOOLS, "cmake-3.30.5-linux-x86_64", "bin", "cmake")
GOBIN = os.path.join(TOOLS, "gotool", "bin")

results = []


def say(msg):
    print(msg, flush=True)


def check(cond, msg):
    results.append((bool(cond), msg))
    say(("  OK  " if cond else "  FAIL ") + msg)
    return bool(cond)


def md5_file(path):
    with open(path, "rb") as fh:
        return hashlib.md5(fh.read()).hexdigest()


def env():
    e = dict(os.environ)
    e["PATH"] = os.path.join(TOOLS, "cmake-3.30.5-linux-x86_64", "bin") + ":" + \
                GOBIN + ":" + e.get("PATH", "")
    e["EHOME_FW_MANIFEST_DECODER"] = DECODER
    return e


def build_decoder():
    r1 = subprocess.run([CMAKE, "-S", os.path.join(FW, "host_tests"), "-B", BUILD_DIR],
                        capture_output=True, text=True)
    if r1.returncode != 0:
        say(r1.stdout[-1500:] + r1.stderr[-1500:])
        return False
    r2 = subprocess.run([CMAKE, "--build", BUILD_DIR,
                         "--target", "firmware_manifest_crosslang", "-j8"],
                        capture_output=True, text=True)
    if r2.returncode != 0:
        say(r2.stdout[-2500:] + r2.stderr[-2500:])
        return False
    return os.path.isfile(DECODER)


def run_anchor():
    """返回 (rc, 输出)。"""
    r = subprocess.run([os.path.join(GOBIN, "go"), "test", "-count=1", "-timeout", "60s",
                        "-run", "TestConfigManifestCrossLanguageGoEncodeCDecode",
                        "./internal/nodemgr/"],
                       cwd=BE, env=env(), capture_output=True, text=True)
    return r.returncode, (r.stdout or "") + (r.stderr or "")


def replace_once(path, old, new, tag):
    """精确替换：断言锚点**恰好出现 1 次**。返回是否落地。"""
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
            hits.append(line.strip()[:170])
        if len(hits) >= limit:
            break
    return hits


def main():
    say("=" * 76)
    say("task-30 变异自证：0x04 ConfigManifest 跨语言对锚")
    say("=" * 76)

    for p in (GO_FILE, C_FILE):
        if not os.path.isfile(p):
            say("FAIL 找不到 %s" % p)
            return 1

    orig_go = open(GO_FILE, "rb").read()
    orig_c = open(C_FILE, "rb").read()
    go_md5, c_md5 = md5_file(GO_FILE), md5_file(C_FILE)
    say("开工 md5: sender_snapshot.go=%s" % go_md5)
    say("          config_mgr.c      =%s" % c_md5)
    say("")

    # ---------- 0) 基线 ----------
    say("[0] 基线：解码器构建 + 对锚必须是绿的")
    if not check(build_decoder(), "固件解码器构建成功"):
        return 1
    rc, out = run_anchor()
    if not check(rc == 0, "基线对锚 rc=0（若基线就是红的，后面的红毫无意义）"):
        say(out[-2500:])
        return 1
    say("")

    try:
        # ---------- 1) Go 侧：sync_id 字段号 8 -> 9 ----------
        say("[1] 变异 1（Go 编码器）：sync_id 从 field 8 挪到 field 9")
        old = "enc.EncodeString(8, decision.SyncID)"
        new = "enc.EncodeString(9, decision.SyncID)"
        if not check(replace_once(GO_FILE, old, new, "M1"), "变异落地（锚点恰好 1 处）"):
            return 1
        m1_md5 = md5_file(GO_FILE)
        check(m1_md5 != go_md5, "变异后 md5 确实变了（%s -> %s）" % (go_md5[:8], m1_md5[:8]))
        rc, out = run_anchor()
        check(rc != 0, "对锚**变红**（Go 侧字段号漂移被抓住）")
        for line in evidence(out, ["DECODE=FAIL", "FAILED", "不一致", "sync_id", "decode"]):
            say("      | " + line)

        with open(GO_FILE, "wb") as fh:
            fh.write(orig_go)
        check(md5_file(GO_FILE) == go_md5, "还原后 md5 逐字节一致")
        rc, out = run_anchor()
        check(rc == 0, "还原后对锚**重新变绿**")
        say("")

        # ---------- 2) C 侧：bus_type 子字段号 6 -> 60 ----------
        say("[2] 变异 2（固件解码器）：channel.bus_type 子字段号 6 -> 60")
        old_c = (
            "            case 6: /* bus_type */\n"
            "                if (cf.wire_type != WIRE_VARINT) return false;\n"
            "                cur_channel->bus_type = (uint8_t)cf.value.varint;"
        )
        new_c = (
            "            case 60: /* bus_type */\n"
            "                if (cf.wire_type != WIRE_VARINT) return false;\n"
            "                cur_channel->bus_type = (uint8_t)cf.value.varint;"
        )
        if not check(replace_once(C_FILE, old_c, new_c, "M2"), "变异落地（锚点恰好 1 处）"):
            return 1
        m2_md5 = md5_file(C_FILE)
        check(m2_md5 != c_md5, "变异后 md5 确实变了（%s -> %s）" % (c_md5[:8], m2_md5[:8]))

        built = build_decoder()
        check(built, "变异体**能编译**（⇒ 下面的红是断言红，不是编译红）")
        if built:
            rc, out = run_anchor()
            check(rc != 0, "对锚**变红**（固件侧解码字段号漂移被抓住）")
            for line in evidence(out, ["XX ", "跨语言对锚失败", "不一致", "bus_type"]):
                say("      | " + line)

        with open(C_FILE, "wb") as fh:
            fh.write(orig_c)
        check(md5_file(C_FILE) == c_md5, "还原后 md5 逐字节一致")
        if not check(build_decoder(), "还原后解码器可再次构建"):
            return 1
        rc, out = run_anchor()
        check(rc == 0, "还原后对锚**重新变绿**")

    finally:
        # 无论上面发生什么，工作树必须回到原状
        with open(GO_FILE, "wb") as fh:
            fh.write(orig_go)
        with open(C_FILE, "wb") as fh:
            fh.write(orig_c)

    say("")
    say("[3] 收尾")
    check(md5_file(GO_FILE) == go_md5, "sender_snapshot.go 与开工 md5 一致")
    check(md5_file(C_FILE) == c_md5, "config_mgr.c 与开工 md5 一致")
    r = subprocess.run(["git", "status", "--short"], cwd=ROOT, capture_output=True, text=True)
    say("      git status --short:")
    for line in r.stdout.splitlines():
        say("        " + line)
    if not r.stdout.strip():
        say("        (空)")

    say("")
    say("=" * 76)
    bad = [m for ok, m in results if not ok]
    if bad:
        say("FAIL 变异自证未通过（%d 项）:" % len(bad))
        for m in bad:
            say("   - " + m)
        return 1
    say("PASS 变异自证：2/2 变异体都能编译、都让对锚变红、还原后均重新变绿且 md5 一致")
    say("=" * 76)
    return 0


if __name__ == "__main__":
    sys.exit(main())
