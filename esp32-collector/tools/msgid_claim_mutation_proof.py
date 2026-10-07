#!/usr/bin/env python3
"""变异自证：check_stale_claims.py 的**规则 3**（消息 ID 声称 vs 权威表）。

判据（本项目铁律）：变异体**必须能编译/运行**、"变坏"后门禁**必须变红**、
还原后 md5 逐字节一致且重新变绿。
"""
import hashlib
import os
import subprocess
import sys

B = "/mnt/storage/WorkSpace/EHome/.worktrees/v3/esp32-collector"
GATE = os.path.join(B, "tools", "check_stale_claims.py")
DATA = os.path.join(B, "components", "msg_handler", "handler_data.c")


def md5(p):
    return hashlib.md5(open(p, "rb").read()).hexdigest()


def run_gate():
    r = subprocess.run([sys.executable, GATE], capture_output=True, text=True, timeout=180)
    return r.returncode, (r.stdout + r.stderr)


MUTANTS = [
    ("M1", DATA, " * Receives: MSG_OTA_CMD (0x0A)", " * Receives: MSG_OTA_CMD (0x0C)",
     "把注释改回错误的 0x0C（真实事故原状）"),
    ("M2", DATA, " * Receives: MSG_OTA_CMD (0x0A)", " * Receives: MSG_OTA_CMD (0x99)",
     "改成另一个错误值 0x99（证明不是只认那一个已知错值）"),
]


def main():
    base = md5(DATA)
    rc, out = run_gate()
    if rc != 0:
        print("FAIL 基线不是绿的（rc=%d）—— 后面的红毫无意义" % rc)
        print(out[-800:])
        return 2
    print("  OK  基线门禁 rc=0（%s）" % base[:8])

    for name, path, old, new, why in MUTANTS:
        print("[%s] %s" % (name, why))
        body = open(path, encoding="utf-8").read()
        assert old in body, "锚点不唯一/找不到：%s" % old
        assert body.count(old) == 1, "锚点出现 %d 次，不唯一" % body.count(old)
        open(path, "w", encoding="utf-8").write(body.replace(old, new))
        after = md5(path)
        if after == base:
            print("     FAIL 变异后 md5 未变 —— 变异没落地")
            return 1
        print("     OK  变异落地且 md5 变化（%s -> %s）" % (base[:8], after[:8]))
        rc, out = run_gate()
        if rc == 0:
            print("     FAIL 变异后门禁**没有变红** —— 这条规则没在测东西")
            return 1
        print("     OK  门禁变红（rc=%d）" % rc)
        line = [l for l in out.splitlines() if "MSG_OTA_CMD" in l]
        if line:
            print("          | %s" % line[0].strip())
        # 还原
        open(path, "w", encoding="utf-8").write(body)
        if md5(path) != base:
            print("     FAIL 还原后 md5 不一致 —— 工作树被污染")
            return 1
        rc, _ = run_gate()
        if rc != 0:
            print("     FAIL 还原后没有重新变绿")
            return 1
        print("     OK  还原后 md5 逐字节一致且重新变绿")

    print()
    print("PASS 规则 3 变异自证：%d/%d 变异体都让门禁变红，还原后一致复绿" % (len(MUTANTS), len(MUTANTS)))
    return 0


if __name__ == "__main__":
    sys.exit(main())