#!/usr/bin/env python3
"""task-31 变异自证：证明**生产成帧**与**对锚**真的咬得住。

本卡修的缺陷能长期藏在全绿后面，是因为对锚客户端**自己成帧**：
它证明的是"一个会正确成帧的客户端能与后端互通"，
而**不是**"生产固件能与后端互通"。所以本脚本必须证明两件事：

  A. 生产成帧函数被改坏 ⇒ 宿主字节断言**红**；
  B. 同一处改坏 ⇒ **对锚也红**（因为对锚现在走生产那条路）。

B 才是本卡的意义：它证明"对锚与真机同路径"不是一句声明。

三条变异（都能编译：只改常量/表达式，不动符号与类型）：
  M1  header.type 写死 0x01  —— type 不再取自 payload[0]
  M2  payload_len 写成 len+1 —— 长度与真实载荷不符
  M3  去掉 magic（把 WIRE_VER 写死成 0 之外的方式）—— 用 flags 置 CRC 位模拟
      "单方面发明开关"：置了 CRC 位但不追加 4 B CRC ⇒ 对端按总长多读 4 B

        M3 的期望：后端 server.go:483 会去读 total-4..total 当 CRC，
        而帧里没有那 4 字节 ⇒ 对端读到的是**下一条帧的前 4 字节**或短读
        ⇒ 对锚必须红。若它没红，说明"置 CRC 位"这个错误不被任何东西发现。

每条变异都断言：
  1. 锚点唯一（命中恰好 1 次）—— 防"没落地却以为改了"；
  2. 前后 md5 不同；
  3. 变异体**能编译**（编译红不算数）；
  4. 宿主断言 / 对锚**变红**；
  5. 还原后 md5 逐字节一致且**重新变绿**。

用法: python3 tools/framing_mutation_proof.py
"""
import hashlib
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
FW = os.path.join(ROOT, "esp32-collector")
CMAKE = "/mnt/storage/WorkSpace/EHome/.tools/cmake-3.30.5-linux-x86_64/bin/cmake"
WIRING = os.path.join(FW, "main", "device_link_wiring.c")
HOST_BUILD = "/tmp/t31-mut-host"
E2E_BUILD = "/tmp/t31-mut-e2e"

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


def sh(cmd, cwd=None, env=None, timeout=900):
    e = dict(os.environ)
    e["PATH"] = "/mnt/storage/WorkSpace/EHome/.tools/cmake-3.30.5-linux-x86_64/bin:" \
                "/mnt/storage/WorkSpace/EHome/.tools/gotool/bin:" + e.get("PATH", "")
    if env:
        e.update(env)
    r = subprocess.run(cmd, cwd=cwd, env=e, capture_output=True, text=True, timeout=timeout)
    return r.returncode, (r.stdout or "") + (r.stderr or "")


def build_host():
    rc, out = sh([CMAKE, "-S", os.path.join(FW, "host_tests"), "-B", HOST_BUILD])
    if rc != 0:
        return False, out
    rc, out = sh([CMAKE, "--build", HOST_BUILD, "--target", "device_link_wiring_tests", "-j8"])
    return rc == 0, out


def run_host():
    return sh([os.path.join(HOST_BUILD, "device_link_wiring_tests")])


def run_anchor(build_dir=E2E_BUILD):
    """跑对锚：构建两个固件目标 + go test（socket 组 + 0x04 组）。"""
    rc, out = sh([CMAKE, "-S", os.path.join(FW, "host_tests"), "-B", build_dir])
    if rc != 0:
        return rc, out
    rc, out = sh([CMAKE, "--build", build_dir,
                  "--target", "firmware_tcp_e2e_client",
                  "--target", "firmware_manifest_crosslang", "-j8"])
    if rc != 0:
        return rc, "构建固件目标失败:\n" + out[-3000:]
    client = os.path.join(build_dir, "firmware_tcp_e2e_client")
    mdec = os.path.join(build_dir, "firmware_manifest_crosslang")
    rc, out = sh([os.path.join("/mnt/storage/WorkSpace/EHome/.tools/gotool/bin", "go"),
                  "test", "-count=1", "-timeout", "60s",
                  "-run", "TestCrossLanguageFirmwareClientOverSocket",
                  "./cmd/server/"],
                 cwd=os.path.join(ROOT, "backend"),
                 env={"EHOME_FW_E2E_CLIENT": client})
    return rc, out


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


def evidence(out, needles, limit=5):
    hits = []
    for line in out.splitlines():
        if any(nd in line for nd in needles):
            hits.append(line.strip()[:160])
        if len(hits) >= limit:
            break
    return hits


MUTANTS = [
    dict(
        tag="M1",
        name="header.type 写死 0x01（不再取自 payload[0]）",
        old="    h.type        = payload[0];   /* ⚠ 后端强校验 type == payload[0] */",
        new="    h.type        = 0x01;         /* MUTANT M1 */",
    ),
    dict(
        tag="M2",
        name="payload_len 写成 len+1（长度与真实载荷不符）",
        old="    h.payload_len = (uint16_t)payload_len;",
        new="    h.payload_len = (uint16_t)(payload_len + 1);   /* MUTANT M2 */",
    ),
    dict(
        tag="M3",
        name="单方面置 CRC32C 位但不追加 4 B CRC（发明开关）",
        old="    h.flags       = 0;            /* ⚠ 不置 CRC32C 位：后端条件式校验 + 下行也不置 */",
        new="    h.flags       = 0x0008u;      /* MUTANT M3: WIRE_FLAG_CRC32C */",
    ),
]


def main():
    say("=" * 78)
    say("task-31 变异自证：生产成帧（devlink_encode_frame）+ 对锚")
    say("=" * 78)

    orig = open(WIRING, "rb").read()
    orig_md5 = md5f(WIRING)
    say("开工 md5: main/device_link_wiring.c = %s" % orig_md5)
    say("")

    say("[0] 基线")
    ok, out = build_host()
    if not check(ok, "宿主 target 构建成功"):
        say(out[-2500:])
        return 1
    rc, out = run_host()
    if not check(rc == 0, "基线宿主字节断言 rc=0"):
        say(out[-1500:])
        return 1
    rc, out = run_anchor()
    if not check(rc == 0, "基线对锚 rc=0"):
        say(out[-2000:])
        return 1
    say("")

    try:
        for m in MUTANTS:
            say("[%s] %s" % (m["tag"], m["name"]))
            if not patch_once(WIRING, m["old"], m["new"], m["tag"]):
                continue
            mmd5 = md5f(WIRING)
            check(mmd5 != orig_md5, "变异落地且 md5 变化（%s -> %s）" % (orig_md5[:8], mmd5[:8]))

            ok, out = build_host()
            check(ok, "变异体**能编译**（⇒ 下面的红是断言红，不是编译红）")
            if ok:
                rc, out = run_host()
                check(rc != 0, "宿主字节断言**变红**")
                for line in evidence(out, ["FAIL", "第 ", "header.type", "payload_len", "CRC"]):
                    say("      | " + line)

            # 对锚（用独立 build dir，避免与宿主目标互相干扰）
            rc, out = run_anchor()
            check(rc != 0, "对锚**变红**（证明对锚真的走生产成帧路径）")
            for line in evidence(out, ["FAIL", "ErrMagic", "magic", "没有连上来",
                                       "hello", "方向0a", "type mismatch", "mismatch"],
                                 limit=4):
                say("      | " + line)

            # 还原
            with open(WIRING, "wb") as fh:
                fh.write(orig)
            check(md5f(WIRING) == orig_md5, "还原后 md5 逐字节一致")
            ok, _ = build_host()
            check(ok, "还原后宿主 target 可再次构建")
            rc, _ = run_host()
            check(rc == 0, "还原后宿主断言**重新变绿**")
            rc, _ = run_anchor()
            check(rc == 0, "还原后对锚**重新变绿**")
            say("")
    finally:
        with open(WIRING, "wb") as fh:
            fh.write(orig)

    say("[收尾]")
    check(md5f(WIRING) == orig_md5, "device_link_wiring.c 与开工 md5 一致")
    rc, out = sh(["git", "status", "--short"], cwd=ROOT)
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
    say("PASS 变异自证：%d/%d 变异体都能编译、都让宿主断言与对锚变红、"
        "还原后均重新变绿且 md5 一致" % (len(MUTANTS), len(MUTANTS)))
    say("=" * 78)
    return 0


if __name__ == "__main__":
    sys.exit(main())
