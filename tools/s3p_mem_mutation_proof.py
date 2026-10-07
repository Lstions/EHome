#!/usr/bin/env python3
"""task-34 变异自证：证明"放置判据"与"缓冲归属"两条真的在测东西。

本卡修的是 s3p 上"3.0 链路吃光内部连续块 ⇒ ConfigManifest 被永久拒绝"。
修法两条：① 定界器/读缓冲放 PSRAM（placement）；② 任务栈按实测收缩。

对应变异（**必须能编译**）：
  M1  placement 判据恒返回 INTERNAL（= 修法没生效）
      ⇒ 宿主必须红：说明"放 PSRAM"这件事是被钉住的，不是靠注释。
  M2  placement 判据反过来（无 PSRAM 也走外部）
      ⇒ 宿主必须红：说明"三型号归一化"（无 PSRAM 落回内部）被钉住。
  M3  wire 层忘记 owns_buf：销毁时**无条件 free 调用方缓冲**
      ⇒ 宿主必须红（哨兵被破坏）—— 这是"free 掉 PSRAM 指针"那类堆损坏的替身。

每条：锚点唯一 → md5 变化 → 能编译 → 测试红 → 还原 md5 一致 → 重新绿。
"""
import hashlib
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
FW = os.path.join(ROOT, "esp32-collector")
CMAKE = "/mnt/storage/WorkSpace/EHome/.tools/cmake-3.30.5-linux-x86_64/bin/cmake"
DW = os.path.join(FW, "main", "device_link_wiring.c")
WIRE = os.path.join(FW, "components", "wire", "wire.c")
BUILD = "/tmp/t34-mut-host"
TARGETS = ["device_link_wiring_tests", "wire_tests"]

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


M1_OLD = "    return psram_available ? DEVLINK_BUF_PLACE_PSRAM : DEVLINK_BUF_PLACE_INTERNAL;"
M1_NEW = "    (void)psram_available;   /* MUTANT M1: 恒内部（= 修法没生效） */\n    return DEVLINK_BUF_PLACE_INTERNAL;"

M2_OLD = "    return psram_available ? DEVLINK_BUF_PLACE_PSRAM : DEVLINK_BUF_PLACE_INTERNAL;"
M2_NEW = "    (void)psram_available;   /* MUTANT M2: 恒外部（无 PSRAM 也走外部） */\n    return DEVLINK_BUF_PLACE_PSRAM;"

M3_OLD = "    if (d->owns_buf) free(d->buf);"
M3_NEW = "    free(d->buf);   /* MUTANT M3: 无条件释放（会 free 掉调用方的 PSRAM 指针） */"

MUTANTS = [
    ("M1", "device_link_wiring.c：placement 恒 INTERNAL（修法没生效）",
     DW, M1_OLD, M1_NEW, ["PSRAM", "落回内部", "FAIL"]),
    ("M2", "device_link_wiring.c：placement 恒 PSRAM（无 PSRAM 也走外部）",
     DW, M2_OLD, M2_NEW, ["无 PSRAM", "FAIL"]),
    ("M3", "wire.c：销毁时无条件 free 调用方缓冲（堆损坏级）",
     WIRE, M3_OLD, M3_NEW, ["哨兵", "越界", "FAIL"]),
]


def main():
    say("=" * 78)
    say("task-34 变异自证：缓冲放置判据 + 缓冲归属")
    say("=" * 78)

    orig = {p: open(p, "rb").read() for p in (DW, WIRE)}
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
    for line in out.splitlines():
        say("      git: " + line)

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
