#!/usr/bin/env python3
"""L-02 去重判据的变异自证：证明 transport_registry_tests 真的能发现缺陷。

为什么需要
==========
host_tests/transport_registry_tests.c 现在绿。绿本身不是证据 —— 必须证明
"判据写坏时它会红"。本文件按四步法注入两种**方向相反**的缺陷：

  M1 transport_registry_has_type() 恒返回 true
     -> 后果是"MQTT 回退被无条件跳过"（TCP-only 时静默丢消息）
     -> 必须被 "TCP registered only: MQTT must NOT report present" 抓住

  M2 transport_registry_has_type() 恒返回 false
     -> 后果是"去重失效，重复发布照旧"（L-02 原样复发）
     -> 必须被 "MQTT registered: MQTT reports present" 抓住

两条都要做：只测其中一个方向，会漏掉另一条独立路径（假象 3）。

四步法：备份+md5 -> 注入可编译的行为变异（锚点 assert 命中）-> 要求
        **用例断言失败**（不是编译错误）-> 恢复 + md5 一致 + 复绿。

用法：
    python3 host_tests/mutation_proof_transport_registry.py
"""
from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))  # esp32-collector/
SRC = os.path.join(ROOT, "components/transport/transport.c")
BUILD = os.environ.get("HT_BUILD_DIR", "/tmp/ht-mr2")
TEST = os.path.join(BUILD, "transport_registry_tests")

# 注入点：函数体第一行（保留符号与类型 => 可编译，只改行为）。
ANCHOR = "bool transport_registry_has_type(transport_type_t type)\n{\n"
MUT_TRUE = ANCHOR + "    (void)type;\n    return true; /* MUTATION M1 */\n"
MUT_FALSE = ANCHOR + "    (void)type;\n    return false; /* MUTATION M2 */\n"


def md5(path: str) -> str:
    with open(path, "rb") as fh:
        return hashlib.md5(fh.read()).hexdigest()


def build_and_run():
    b = subprocess.run(["cmake", "--build", BUILD, "-j8",
                        "--target", "transport_registry_tests"],
                       capture_output=True, text=True)
    if b.returncode != 0:
        return None, "BUILD FAILED:\n" + b.stdout[-800:] + b.stderr[-400:]
    r = subprocess.run([TEST], capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr


def mutate(new_body: str) -> None:
    with open(SRC, encoding="utf-8") as fh:
        s = fh.read()
    n = s.count(ANCHOR)
    assert n == 1, "anchor matched %d times (expected 1)" % n
    with open(SRC, "w", encoding="utf-8") as fh:
        fh.write(s.replace(ANCHOR, new_body))


def main() -> int:
    if not os.path.isfile(TEST):
        print("SKIP: %s not built yet; run: cmake --build %s -j8 --target "
              "transport_registry_tests" % (TEST, BUILD), file=sys.stderr)
        return 2
    baseline = md5(SRC)
    with open(SRC, encoding="utf-8") as fh:
        original = fh.read()
    failures = []

    for label, body, expect_red, desc in (
        ("M1", MUT_TRUE, "MQTT must NOT report present",
         "predicate hard-wired true (MQTT fallback silently skipped)"),
        ("M2", MUT_FALSE, "MQTT reports present",
         "predicate hard-wired false (duplicate publish returns, L-02 recurs)"),
    ):
        print("=== %s: %s ===" % (label, desc))
        mutate(body)
        rc, out = build_and_run()
        if rc is None:
            failures.append("%s: %s" % (label, out))
            print(out)
        else:
            red = rc != 0
            hit = expect_red in out
            print(out.strip())
            print("-> exit=%d red=%s expected_message_present=%s" % (rc, red, hit))
            if not red:
                failures.append("%s: test stayed GREEN (assertion not exercised)" % label)
            elif not hit:
                failures.append("%s: red for the wrong reason (expected %r)" % (label, expect_red))
            else:
                print("%s PASS: caught by the intended assertion\n" % label)
        # restore（从本脚本自己持有的备份恢复，不依赖树里的 .orig 文件）
        with open(SRC, "w", encoding="utf-8") as fh:
            fh.write(original)

    if md5(SRC) != baseline:
        failures.append("restore: transport.c md5 differs from baseline")

    print("=== final: test must be GREEN again ===")
    rc, out = build_and_run()
    print(out.strip() if rc is not None else out)
    if rc != 0:
        failures.append("final: test did not return to green")

    print()
    if failures:
        print("MUTATION PROOF FAILED:")
        for f in failures:
            print("  - %s" % f)
        return 1
    print("MUTATION PROOF OK: 2/2 directions caught by the intended assertions, "
          "transport.c restored byte-identical, test green")
    return 0


if __name__ == "__main__":
    sys.exit(main())
