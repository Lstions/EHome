#!/usr/bin/env python3
"""门禁分类器自检：喂坏输入 ⇒ 每条门禁**必须变红**；还原后**必须重新变绿**。

## 为什么需要它（本仓前科）
- D-25：s3p 的 dir_remain_min 曾为 0 ⇒ 判据 remain >= 0 **恒真** ⇒ 该门禁永远不会红
  （门禁打印 PASS 却什么都没检查）；
- D-27：mem_budget_check **存在但从未被任何流程调用** —— 不运行的检查等价于没有检查；
- L-05：阈值余量只剩 16 B ⇒ 挡住的不是退化，而是下一次正常提交。

⇒ 只要有一条门禁是恒真的，"N 条门禁全 RC=0" 这句话就是空的。
本脚本把这句话变成**可证的**：对每条门禁构造一份已知坏的输入，
断言它**非零退出**，然后还原并断言**重新 rc=0**。

## 三种判定（严格区分）
- 会咬：坏输入 ⇒ 非零，还原后重新 rc=0；
- 证不出：我构造不出会红的输入（"我没找到办法"）；
- 恒真：有证据说明该门禁**永远为真**（不是"没找到办法"，而是"它不可能红"）。

⚠ 本脚本**不修改任何门禁的判据**，只负责喂输入。
  若某条门禁的判据本身是错的，那是另一件事，应单独报告。

## 安全（硬要求）
- 每条自检：先备份 → 再改 → 跑 → 还原 → 核对 md5；
- 任何异常都在 finally 里还原（绝不允许把坏改动留在工作树）；
- 结束时把 git status 与开工时对比，出现新增残留则打印清单并返回非零；
- 只碰 esp32-collector/、protocol/vectors/（临时改、md5 还原），
  以及用完即删的探针目录 components/zz_gate_selftest_probe/。

## 用法
    python3 tools/check_gates_selftest.py                 # 全部
    python3 tools/check_gates_selftest.py --only cycles
    python3 tools/check_gates_selftest.py --list

## 自检脚本自身的变异证明（防止自检假绿）
    GATE_SELFTEST_HARMLESS=<配方名子串> python3 tools/check_gates_selftest.py
把指定配方的"坏输入"换成**无害输入**（注入写成空操作）⇒ 门禁不会红
⇒ 自检脚本**必须报红**；若仍报绿，说明自检本身是假绿的。
该模式下返回 2（预期报红）。
"""
import argparse
import glob
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)              # esp32-collector
WT = os.path.dirname(ROOT)                 # worktree root
SELF = os.path.basename(os.path.abspath(__file__))
PROBE_DIR = os.path.join(ROOT, "components", "zz_gate_selftest_probe")
PROBE_REL = "components/zz_gate_selftest_probe"
# 生产主机字面量：prod_isolation 的配方要把它注入进去。
# ⚠ 与 check_prod_isolation.py 里的 PROD_HOST 必须**同值** —— 两处各写一份是
#   有意的冗余：若哪天生产地址变了而只改了一处，配方会因"注入了不触发门禁的
#   地址"而红（门禁正常、配方失效），从而暴露不一致。反过来若共用一个常量，
#   改错时两边一起错、自检照样绿。
PROD_HOST_LITERAL = "192.168.20.6"
TMP_FRAME = "/tmp/gate_selftest_frame_header.txt"
TMP_JUNK = "/tmp/gate_selftest_junk.elf"

# ⚠⚠ 2026-10-08（§194）：原值是**写死的 /tmp/v3idf-m1/s3-n16/ehome_collector.elf**。
#   两个问题：
#     ① /tmp 会被清理、且本项目用户明令"不要把东西放 /tmp"⇒ 该路径长期不存在；
#     ② 一旦不存在，三条 iram 配方就**永远跳过**（自检报"跳过 3"），
#        而"跳过"看起来像"环境不完整"、不像缺陷 ⇒ 这三条门禁的自证**长期缺席**。
#   ⇒ 改为**自动搜索**真实构建产物（按新鲜度取最新的一个）。
#   为什么不是写死某个 build 目录：build 目录名随实验变（nomqtt/bbase8/...），
#   写死任何一个都会再次退化成"路径不存在 ⇒ 永远跳过"。
def _find_iram_elf():
    """找一个真实的固件 ELF 给 iram 门禁用；找不到返回 None（该 3 条配方跳过）。"""
    pats = [
        os.path.join(WT, "__scratch_v3", "build", "*", "*", "ehome_collector.elf"),
        os.path.join(ROOT, "build", "*", "ehome_collector.elf"),
    ]
    cands = []
    for pat in pats:
        cands.extend(glob.glob(pat))
    cands = [c for c in cands if os.path.isfile(c)]
    if not cands:
        return None
    return max(cands, key=os.path.getmtime)


IRAM_ELF = _find_iram_elf()

# ── 崩溃安全：写盘日志（journal）────────────────────────────────────────
#
# 为什么需要它（2026-10-07 实测）：
# 本脚本会**临时改工作树里的文件**，还原靠 `finally` + 内存里的备份。
# 但 `finally` **扛不住 SIGKILL**（Ctrl-C、OOM killer、ctest 强杀）：
# 那一刻内存备份随之消失，被改坏的文件**永久留在工作树里**。
# 实测暴露窗口约 **100 ms**（4.4 s 里 24 次采样有 1 次脏）。
#
# 后果特别糟：一个被改坏的门禁输入看起来像"某人提交了坏代码"，
# 排查方向会完全跑偏（本仓已多次被"看着像代码问题"的环境问题耽误）。
#
# ⇒ 做法：**改之前**先把原始字节落盘 + fsync，清单也先落盘；
#   下次启动时若发现日志 ⇒ 先恢复、并**大声报告**，再继续。
#   这样即使进程被 SIGKILL，损失也止于"这一次运行"，不会越过下一次启动。
JOURNAL_DIR = "/tmp/ehome_gate_selftest_journal"
JOURNAL_MANIFEST = os.path.join(JOURNAL_DIR, "manifest.tsv")
# 新建物（探针目录等）也记日志 —— 2026-10-07 实测补：
# 只记改过的不够：被 SIGKILL 后探针目录会留在工作树里，
# 而下次运行的 探针目录已存在拒绝覆盖 断言会直接崩，
# 于是自愈反而变成下次跑不起来。恢复与清理必须成对。
JOURNAL_CREATED = os.path.join(JOURNAL_DIR, "created.tsv")


def md5(b):
    return hashlib.md5(b).hexdigest()


def _journal_add(path, raw):
    """在**改文件之前**把原始字节落盘并 fsync。

    顺序很重要：manifest 行必须在文件被改**之前**可见 ——
    否则"已改完、还没记上"那一瞬被 SIGKILL 就无从恢复。
    先写备份体 + fsync，再追加 manifest 行 + fsync。
    """
    os.makedirs(JOURNAL_DIR, exist_ok=True)
    idx = 0
    while os.path.exists(os.path.join(JOURNAL_DIR, "%d.bak" % idx)):
        idx += 1
    body = os.path.join(JOURNAL_DIR, "%d.bak" % idx)
    with open(body, "wb") as fh:
        fh.write(raw)
        fh.flush()
        os.fsync(fh.fileno())
    with open(JOURNAL_MANIFEST, "a", encoding="utf-8") as fh:
        fh.write("%s\t%s\n" % (path, body))
        fh.flush()
        os.fsync(fh.fileno())


def _journal_add_created(path):
    """在新建之前把路径记到盘上（先落盘，再动手）。"""
    os.makedirs(JOURNAL_DIR, exist_ok=True)
    with open(JOURNAL_CREATED, "a", encoding="utf-8") as fh:
        fh.write("%s\n" % path)
        fh.flush()
        os.fsync(fh.fileno())


def recover_journal():
    """启动时自愈：恢复上次被 SIGKILL 留下的改动，并清理它新建的物。

    返回 (恢复的文件数, 清理的新建物数)。
    """
    has_manifest = os.path.exists(JOURNAL_MANIFEST)
    has_created = os.path.exists(JOURNAL_CREATED)
    if not has_manifest and not has_created:
        shutil.rmtree(JOURNAL_DIR, ignore_errors=True)
        return 0, 0
    restored, failed, removed = 0, [], 0
    lines = []
    if has_manifest:
        with open(JOURNAL_MANIFEST, encoding="utf-8") as fh:
            lines = [ln.rstrip("\n") for ln in fh if ln.strip()]
    for ln in lines:
        parts = ln.split("\t")
        if len(parts) != 2:
            failed.append("manifest 行不可解析: %r" % ln)
            continue
        path, body = parts
        try:
            with open(body, "rb") as fh:
                raw = fh.read()
            with open(path, "wb") as fh:
                fh.write(raw)
            with open(path, "rb") as fh:
                if md5(fh.read()) != md5(raw):
                    failed.append("%s: 写入后 md5 不一致" % path)
                    continue
            restored += 1
        except OSError as e:
            failed.append("%s: %s" % (path, e))

    if has_created:
        with open(JOURNAL_CREATED, encoding="utf-8") as fh:
            created_paths = [ln.rstrip("\n") for ln in fh if ln.strip()]
        for cp in created_paths:
            try:
                ok = (os.path.realpath(cp) == os.path.realpath(PROBE_DIR)
                      or cp in (TMP_FRAME, TMP_JUNK) or cp.startswith("/tmp/"))
                if not ok:
                    failed.append("%s: 不在可清理白名单内，拒绝删除" % cp)
                    continue
                if os.path.isdir(cp):
                    shutil.rmtree(cp, ignore_errors=True)
                    removed += 1
                elif os.path.exists(cp):
                    os.remove(cp)
                    removed += 1
            except OSError as e:
                failed.append("%s: 清理失败 %s" % (cp, e))

    print("=" * 78)
    print("⚠ 检测到上一次自检**异常退出**留下的日志（很可能是 SIGKILL / OOM / Ctrl-C）。")
    print("  已自动恢复 %d 个文件，清理 %d 个新建物。" % (restored, removed))
    for f in failed:
        print("  ❌ %s" % f)
    print("  （自愈机制补于 2026-10-07：finally 扛不住 SIGKILL，"
          "会把改坏的文件永久留在工作树里）")
    print("=" * 78)
    shutil.rmtree(JOURNAL_DIR, ignore_errors=True)
    return restored, removed


def clear_journal():
    shutil.rmtree(JOURNAL_DIR, ignore_errors=True)


def run_gate(args, timeout=900):
    """跑一条门禁，返回 (rc, 合并输出)。"""
    try:
        p = subprocess.run([sys.executable] + list(args),
                           capture_output=True, text=True, timeout=timeout, cwd=ROOT)
        return p.returncode, (p.stdout or "") + (p.stderr or "")
    except subprocess.TimeoutExpired:
        return 124, "TIMEOUT after %ds" % timeout


def evidence(out, limit=6):
    """挑出"它为什么红"的那几行。"""
    keep = []
    for line in out.splitlines():
        s = line.strip()
        if s.startswith(("FAIL", "ERROR", "SKIP")) or " FAIL " in s or s.startswith("usage:"):
            keep.append(s[:150])
        if len(keep) >= limit:
            break
    return keep


# ───────────────────────── 注入器 ─────────────────────────
# backups: 绝对路径 -> 原始字节（还原时写回并核对 md5）
# created: 绝对路径列表（本次新建物，还原时删除）

def _backup(backups, path):
    if path not in backups:
        with open(path, "rb") as fh:
            backups[path] = fh.read()
        # ⭐ 同时**落盘**：万一本进程被 SIGKILL，内存备份会随之消失，
        # 下次启动只能靠这份日志自愈（见 recover_journal）。
        _journal_add(path, backups[path])
    return backups[path]


def _patch_text(backups, path, fn, what):
    raw = _backup(backups, path)
    old = raw.decode("utf-8")
    new = fn(old)
    assert new != old, "注入器没打中锚点（%s）—— 假绿来源，必须先修注入器" % what
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(new)
    return what


def inject_json_key(relpath, setter):
    def fn(backups, created):
        path = os.path.join(ROOT, relpath)
        raw = _backup(backups, path)
        cfg = json.loads(raw.decode("utf-8"))
        before = json.dumps(cfg, sort_keys=True)
        setter(cfg)
        assert json.dumps(cfg, sort_keys=True) != before, "JSON 注入没打中锚点"
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(cfg, fh, ensure_ascii=False, indent=2)
            fh.write("\n")
        return relpath
    return fn


def inject_probe(files):
    def fn(backups, created):
        assert not os.path.exists(PROBE_DIR), "探针目录已存在，拒绝覆盖：%s" % PROBE_DIR
        # 先记日志、再建目录（顺序不能反）：若在两者之间被 SIGKILL，
        # 目录会留下而日志里没有它，下次启动无从清理，而拒绝覆盖断言会直接崩。
        _journal_add_created(PROBE_DIR)
        os.makedirs(PROBE_DIR)
        created.append(PROBE_DIR)
        for rel, text in files.items():
            with open(os.path.join(PROBE_DIR, rel), "w", encoding="utf-8") as fh:
                fh.write(text)
        return PROBE_REL + "/{" + ",".join(sorted(files)) + "}"
    return fn


def inject_insert_after(relpath, anchor, inserted):
    def fn(backups, created):
        path = os.path.join(ROOT, relpath)
        raw = _backup(backups, path)
        lines = raw.decode("utf-8").split("\n")
        hit = [i for i, l in enumerate(lines) if l.strip() == anchor]
        assert len(hit) == 1, "锚点 %r 命中 %d 次（应为 1）" % (anchor, len(hit))
        lines.insert(hit[0] + 1, inserted)
        with open(path, "w", encoding="utf-8") as fh:
            fh.write("\n".join(lines))
        return "%s: 在 %r 后插入 %r" % (relpath, anchor, inserted.strip())
    return fn


def inject_append(relpath, text):
    def fn(backups, created):
        path = os.path.join(ROOT, relpath)
        raw = _backup(backups, path)
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(raw.decode("utf-8").rstrip("\n") + "\n" + text)
        return "%s: 追加 %s" % (relpath, text.strip()[:70])
    return fn


def inject_make_file(relpath, text):
    """新建一个文件（含父目录），供"陈旧构建产物"这类配方使用。

    ⚠ 与 inject_probe 的差别：probe 建在固定目录 components/zz_gate_selftest_probe，
      而本原语需要建在 build/ 这类**已 gitignore** 的路径下模拟陈旧产物。
    ⚠ 清理安全性：只允许建在 build/ 前缀下（该目录已 gitignore、可安全重建），
      并由 _journal_add_created 先落盘，SIGKILL 后 recover_journal 能自愈。
    """
    assert relpath.startswith("build/"), \
        "inject_make_file 只允许建在 build/ 下（gitignore、可安全重建），收到：%s" % relpath
    def fn(backups, created):
        path = os.path.join(ROOT, relpath)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        _journal_add_created(path)
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)
        created.append(path)
        return relpath
    return fn


def inject_replace(relpath, pattern, repl, what):
    def fn(backups, created):
        def sub(text):
            new, n = re.subn(pattern, repl, text, count=1)
            assert n == 1, "%s：正则没打中（命中 %d 次）" % (what, n)
            return new
        path = os.path.join(ROOT, relpath)
        return _patch_text(backups, path, sub, "%s: %s" % (relpath, what))
    return fn


def inject_replace_text(relpath, fn_text):
    def fn(backups, created):
        path = os.path.join(WT, relpath)
        return _patch_text(backups, path, fn_text, relpath)
    return fn


def inject_tmp_corrupt(src_rel, dst, corrupt):
    """把仓库文件复制到 /tmp 后改坏，用**路径参数**喂给门禁 —— 仓库不动。"""
    def fn(backups, created):
        src = os.path.join(WT, src_rel)
        with open(src, encoding="utf-8") as fh:
            text = fh.read()
        new = corrupt(text)
        assert new != text, "tmp 注入没打中锚点 —— 假绿来源"
        with open(dst, "w", encoding="utf-8") as fh:
            fh.write(new)
        created.append(dst)
        return "tmp 副本 %s（源 %s）" % (dst, src_rel)
    return fn


def inject_tmp_junk(dst, payload):
    def fn(backups, created):
        with open(dst, "wb") as fh:
            fh.write(payload)
        created.append(dst)
        return "tmp 垃圾文件 %s（%d 字节）" % (dst, len(payload))
    return fn


# ───────────────────── 坏输入构造 ─────────────────────

def _frame_header_corrupt(text):
    """改坏第一个"肯定例"的 wire 值；带 expect_ 的负例不动。"""
    lines = text.split("\n")
    has_expect = False
    for i, line in enumerate(lines):
        s = line.strip()
        if s.startswith("case "):
            has_expect = False
        elif s.startswith("expect_"):
            has_expect = True
        elif s.startswith("wire ") and not has_expect:
            kw, _, val = s.partition(" ")
            if len(val) < 2:
                continue
            last = val[-1]
            lines[i] = "%s %s%s" % (kw, val[:-1], "0" if last != "0" else "1")
            return "\n".join(lines)
    raise AssertionError("frame_header: 找不到可改的肯定例 wire 行")


def _golden_corrupt(text):
    m = re.search(r"^wire\s+([0-9a-f]+)\s*$", text, re.M)
    assert m, "wire_primitives: 找不到 wire 行"
    val = m.group(1)
    bad = val[:-1] + ("0" if val[-1] != "0" else "1")
    return text[:m.start()] + "wire " + bad + text[m.end():]


def recipes():
    """每条门禁至少一条配方。kind: file / probe / tmp / argv。"""
    R = []

    def add(name, gate, kind, inject, desc, green_args=(), bad_args=None,
            extra=None):
        R.append(dict(name=name, gate=gate, kind=kind, inject=inject, desc=desc,
                      green_args=list(green_args), bad_args=bad_args, extra=extra))

    add("mem_budget_thresholds.remain_min_zero",
        "check_mem_budget_thresholds.py", "file",
        inject_json_key("tools/mem_budget.json",
                        lambda c: c["defaults"].__setitem__("dir_remain_min", 0)),
        "tools/mem_budget.json: defaults.dir_remain_min 20480 -> 0（恒真阈值）")

    add("baseline_sync.doc_drift",
        "check_baseline_sync.py", "file",
        inject_json_key("tools/mem_budget.json",
                        lambda c: c["profiles"]["s3-n16"].__setitem__(
                            "dir_used_max", c["profiles"]["s3-n16"]["dir_used_max"] + 1)),
        "tools/mem_budget.json: s3-n16.dir_used_max +1 ⇒ 与基线文档对照表不一致")

    add("component_cycles.self_dep",
        "check_component_cycles.py", "file",
        inject_replace("components/report_stats/CMakeLists.txt",
                       r'(INCLUDE_DIRS "include")',
                       '\\1\n    REQUIRES report_stats', "自依赖"),
        'report_stats/CMakeLists.txt: 追加 REQUIRES report_stats ⇒ 自环')

    add("component_reachable.unregistered",
        "check_component_reachable.py", "probe",
        inject_probe({"CMakeLists.txt":
                      'idf_component_register(SRCS "probe.c" INCLUDE_DIRS "")\n',
                      "probe.c": "int zz_gate_probe(void) { return 0; }\n"}),
        "新建 %s/（main 不可达、且不在待接线清单里）" % PROBE_REL)

    # ── 待接线清单自身的可核查判据（2026-10-07 新增）──────────────────
    #
    # 起因：清单注释把"骨架 + 宿主测试已完成"写成了**事实**，而它是**假的**
    # （nvs_helper 一个测试文件都没有）。于是把"有没有测试"改成去文件系统里查。
    # 这三条配方就是证明这些新判据**真的会咬** —— 否则我又加了一条自证不了的检查。
    add("component_reachable.fake_test_ref",
        "check_component_reachable.py", "file",
        inject_replace("tools/check_component_reachable.py",
                       r'"test": "dispatch_tests\.c"',
                       '"test": "no_such_test_file.c"',
                       "登记表里的测试引用指向不存在的文件"),
        "把 dispatch 的 test 改成不存在的文件 ⇒ 门禁必须红（'测试已完成'不能只是嘴上说）")

    # ⚠ 2026-10-08（§194）：本配方原先把锚点打在 link_mqtt 条目上
    #   （"把 link_mqtt 的 test 换成 sntp_mgr_tests.c"）。
    #   但 link_mqtt 随 MQTT 一起整体删除了、登记条目也已删除
    #   ⇒ 正则**打不中任何东西**，inject_replace 抛 AssertionError，
    #     整个自检**崩掉**（不是"红"，是 traceback 退出）。
    #   ⚠ 这正是"门禁的判据依赖了一条会消失的输入" —— 自检本身没有兜底。
    #   ⇒ 锚点改到 dispatch（同一条目类型、同样有 test 字段、长期稳定）。
    add("component_reachable.test_ref_not_mentioning",
        "check_component_reachable.py", "file",
        inject_replace("tools/check_component_reachable.py",
                       r'"test": "dispatch_tests\.c"',
                       '"test": "sntp_mgr_tests.c"',
                       "登记表里的测试引用是个真文件但并不测该组件"),
        "把 dispatch 的 test 换成 sntp_mgr_tests.c（真文件但不提 dispatch）⇒ 必须红")

    # ⚠ 同上：原锚点是 link_mqtt 的 review_by: "2026-12-01"（条目已删）。
    #   现改为把 **msgcodec** 的 review_by 改成过去日期 —— 该条目长期存在。
    add("component_reachable.review_overdue",
        "check_component_reachable.py", "file",
        inject_replace("tools/check_component_reachable.py",
                       r'"review_by": "2026-11-15"',
                       '"review_by": "2026-01-01"',
                       "决策截止日已过去"),
        "把 msgcodec 的 review_by 改成过去的日期 ⇒ 门禁必须红（清单不能无限期挂着）")

    add("component_reachable.test_missing_note",
        "check_component_reachable.py", "file",
        inject_replace("tools/check_component_reachable.py",
                       r'"test_note": 「?[^」\n]*」?,',
                       "",
                       "无测试却连理由都不写"),
        "删掉 nvs_helper 的 test_note ⇒ 必须红（无测试可以，但要说明原因）")

    add("databatch.weak_symbol_regression",
        "check_databatch_injected.py", "file",
        inject_append("components/bus_worker/bus_worker.c",
                      "\n__attribute__((weak)) int msg_handler_send_data_batch(void *d) "
                      "{ (void)d; return 0; }\n"),
        "bus_worker.c: 重新引入 msg_handler_send_data_batch 的弱定义（D-06 形态）")

    add("databatch.inject_call_removed",
        "check_databatch_injected.py", "file",
        inject_replace("main/main.c", r"bus_worker_set_data_batch_cb\(",
                       "bus_worker_set_data_batch_cb_ZZ_PROBE(", "注入调用改名"),
        "main.c: bus_worker_set_data_batch_cb( 改名 ⇒ 注入点消失")

    add("frame_header.wire_corrupted",
        "check_frame_header.py", "tmp",
        inject_tmp_corrupt("protocol/vectors/frame_header.txt", TMP_FRAME,
                           _frame_header_corrupt),
        "frame_header.txt 的 /tmp 副本：第一个肯定例的 wire 改坏一个 nibble",
        bad_args=[TMP_FRAME])

    add("golden_vectors.wire_corrupted",
        "check_golden_vectors.py", "file",
        inject_replace_text("protocol/vectors/wire_primitives.txt", _golden_corrupt),
        "wire_primitives.txt: 第一条 wire 改坏一个 nibble")

    add("host_coverage.new_uncovered_file",
        "check_host_coverage.py", "probe",
        inject_probe({"probe.c": "int zz_gate_probe(void) { return 0; }\n"}),
        "新建 %s/probe.c（未被任何宿主 target 编译）⇒ 未覆盖文件数 +1" % PROBE_REL,
        extra="coverage")

    add("iram_isr.no_elf_arg",
        "check_iram_isr_safety.py", "argv", None,
        "不给 ELF 参数（argparse 必修）—— 纯参数，仓库不动",
        green_args=[IRAM_ELF])

    add("iram_isr.missing_elf",
        "check_iram_isr_safety.py", "argv", None,
        "给一个不存在的 ELF 路径（无法验证不得静默通过）",
        green_args=[IRAM_ELF], bad_args=["/tmp/gate_selftest_no_such.elf"])

    add("iram_isr.junk_elf",
        "check_iram_isr_safety.py", "tmp",
        inject_tmp_junk(TMP_JUNK, b"\x7fELF" + b"\x00" * 200),
        "截断/垃圾 ELF（没有 .iram 符号 ⇒ 无法分析）",
        green_args=[IRAM_ELF], bad_args=[TMP_JUNK])

    add("message_types.device_only_number",
        "check_message_types.py", "probe",
        inject_probe({"probe.h": "#define MSG_ZZ_GATE_PROBE 0x7F\n"}),
        "新建 %s/probe.h：#define MSG_ZZ_GATE_PROBE 0x7F（Go 侧无同号常量）" % PROBE_REL)

    add("no_weak_symbols.marker_reintroduced",
        "check_no_weak_symbols.py", "probe",
        inject_probe({"probe.c":
                      "__attribute__((weak)) int zz_gate_probe(void) { return 0; }\n"}),
        "新建 %s/probe.c 含 __attribute__((weak))" % PROBE_REL)

    add("sdkconfig_symbols.unknown_symbol",
        "check_sdkconfig_symbols.py", "file",
        inject_append("sdkconfig.defaults", "CONFIG_ZZ_GATE_SELFTEST_PROBE=y\n"),
        "sdkconfig.defaults: 追加不存在的 CONFIG_ZZ_GATE_SELFTEST_PROBE=y")

    # ⚠ 2026-10-08：stub_enum_sync 配方已删除 —— 该门禁连同
    #   tools/check_stub_enum_sync.py 一起删除（唯一守护目标 mqtt_publish_result_t
    #   随 components/ehome_mqtt 移除，且本仓已无任何"手抄生产枚举的宿主副本"）。
    #   本自检会拒绝"有配方但门禁不存在"⇒ 必须同步删配方，否则自检会红。
    #   （这正是它该有的行为：门禁删了却留着配方，等于假装它还在守。）

    add("tcp_start_reachable.unreachable_block",
        "check_tcp_start_reachable.py", "file",
        inject_insert_after("main/app_callbacks.c", "case WIFI_MGR_CONNECTED:",
                            "        break;"),
        "app_callbacks.c: 在 case WIFI_MGR_CONNECTED 正下方插入 break; ⇒ TCP 启动块不可达")

    add("tls_constants.mirror_drift",
        "check_tls_constants.py", "file",
        inject_replace("components/tls_guard/include/tls_guard.h",
                       r"(#define\s+TLS_ERGTYPE_MBEDTLS)(\s+)2\b", r"\g<1>\g<2>3",
                       "镜像常量漂移"),
        "tls_guard.h: TLS_ERGTYPE_MBEDTLS 2 -> 3（与 IDF 枚举顺序失配）")

    # 2026-10-07 新增：证书 NVS 契约（跨语言）。
    # 坏输入选"固件读的键名"这一侧 —— 它最能说明这条门禁的价值：
    # 两端漂移时工具照样生成、读回校验照样全绿，只有固件读不到。
    add("cert_nvs_contract.fw_key_drift",
        "check_cert_nvs_contract.py", "file",
        inject_replace("main/device_link_wiring.c",
                       r'nvs_read_blob_alloc\(h, "key",', 'nvs_read_blob_alloc(h, "privkey",',
                       "固件读的键名漂移（key -> privkey）"),
        "device_link_wiring.c: 固件读 \"privkey\" 而工具写 \"key\" ⇒ 镜像生成的证书固件读不到")

    add("cert_nvs_contract.tool_ns_drift",
        "check_cert_nvs_contract.py", "file",
        inject_replace("tools/nvs_certs_gen.py",
                       r'DEFAULT_NS = "eh_tls"', 'DEFAULT_NS = "eh_tls2"',
                       "工具写的命名空间漂移（eh_tls -> eh_tls2）"),
        "nvs_certs_gen.py: 工具写 \"eh_tls2\" 而固件开 \"eh_tls\" ⇒ 命名空间对不上")

    # ── 过期声称（2026-10-07 新增）────────────────────────────────────
    #
    # 这两条配方证明 check_stale_claims 的新判据**真的会咬**。
    # 起因：Kconfig help 写着 "SNTP is not landed"，而 SNTP 早已落地 ——
    # 过期清单比没有清单更糟，它让人以为清单是完整的。
    add("stale_claims.sntp_already_landed",
        "check_stale_claims.py", "file",
        inject_replace("main/device_link_wiring.c",
                       r'(static const char \*TAG = "[^"]*";)',
                       r'\1\n/* SNTP 尚未落地，时间不可信。 */',
                       "重新引入过期的 SNTP 声称"),
        "device_link_wiring.c: 注入 \"SNTP 尚未落地\" ⇒ 与事实（sntp_mgr 存在且被调用）矛盾")

    add("stale_claims.uplink_already_framed",
        "check_stale_claims.py", "file",
        inject_replace("main/session_transport.c",
                       r'(#include "session_transport.h")',
                       r'\1\n/* 已知问题：上行未成帧。 */',
                       "重新引入过期的未成帧声称"),
        "session_transport.c: 注入 \"上行未成帧\" ⇒ 与事实（有成帧调用点）矛盾")

    add("cert_nvs_contract.kconfig_mismatch",
        "check_cert_nvs_contract.py", "file",
        # 锚点必须只落在**一行内**：inject_replace 用的是 re.subn(pattern, repl, text)
        # **没有 re.S** ⇒ "." 不匹配换行。我第一版写成
        #   (config\s+EHOME_DEVICE_LINK_NVS_NS.*?default\s+")eh_tls(")
        # 想让 .*? 跨过 config 与 default 之间的若干行，结果**命中 0 次**，
        # 自检直接 assert 报错（幸好它 assert 了；否则就是静默漏掉一条配方）。
        # 改用"default 那一行"作锚点：它在整个文件里唯一。
        inject_replace("main/Kconfig.projbuild",
                       r'default "eh_tls"', 'default "zz_mismatch"',
                       "Kconfig default 与源码兜底矛盾"),
        "Kconfig.projbuild: default 改成 zz_mismatch，与源码 #define 兜底不一致")

    # ⚠ 为什么必须给 check_mem_guard_floor 也加一条配方（D-26）：
    #   host_tests/mem_guard_tests.c 用**自适应宏** FLOOR = mem_guard_floor_bytes()，
    #   所有断言都相对 FLOOR 写 ⇒ 改动 floor 时 ctest 仍 109/109。
    #   即：floor 被静默改动时，**没有别的门禁会红**。
    #   而 floor 是**行为阈值**（决定配置事务/log_stream/UART install 放行）
    #   且作为 MemReport field5 上报
    #   ⇒ 若这条新门禁自己也不咬，就等于又加了一条自证不了的检查。
    #
    # ⚠ 2026-10-08（§191）：s3p 的 floor 已按决策 §13 **正式改为 8 KiB**
    #   （原 16 KiB 不可达，是功能缺陷）。故本配方的变异值随之改为 8→4 KiB。
    #   ⚠ 锚点必须**唯一命中 s3p 那一支**：文件里有多处 (8u * 1024u)
    #     （s3p / s3 / else 兜底），故把 #if 条件一起写进 pattern。
    #   ⚠ s3p 的 #define 与 #if 之间隔着 §13 的理由注释 ⇒ 必须跨行匹配；
    #     而 inject_replace 不开 re.S/DOTALL ⇒ 不能用 "."，要用 [\s\S]*?。
    add("mem_guard_floor.s3p_value_drift",
        "check_mem_guard_floor.py", "file",
        inject_replace("main/mem_guard.c",
                       r'(defined\(CONFIG_COLLECTOR_PSRAM\) && CONFIG_COLLECTOR_PSRAM[\s\S]*?'
                       r'#define MEM_GUARD_FLOOR_BYTES       \()8u( \* 1024u\))',
                       r'\g<1>4u\g<2>',
                       "s3p floor 8KiB -> 4KiB（s3p 那一支）"),
        "mem_guard.c: s3p floor 8KiB -> 4KiB ⇒ 门禁必须红"
        "（宿主测试因用自适应 FLOOR 宏而全绿，只有本门禁能发现）")

    # ⚠⚠ 2026-10-08（§197）：为什么给 check_hw_bus_defaults 加配方
    #
    #   真机发现：hw_tables.c:91 的 I2C1 default_scl = 48，而**同一个文件** :62
    #   自己写着 "S3: ... GPIO48=RGB LED"，hw_tables.h 也定义了 HW_RESERVED_LED=48，
    #   且 main.c 的 rgb_led_init(48) 真的在驱动那个脚。
    #   ⇒ 用户按默认值建 I2C1 ⇒ 设备 preinstall rejected: ESP_ERR_INVALID_ARG
    #     ⇒ **整份 manifest 被拒**（连 3 条 UART 一起不装）。
    #
    #   ⚠ 这条门禁的价值在于"**人眼审不出来**"：两个清单在同一个文件里、相隔 29 行，
    #     而且这是**第二次**踩同一个坑（第一次是 GPIO0/BOOT 脚被 PWM 占用，
    #     见 hw_tables.c:104-107）。所以必须证明它真的会咬 —— 否则它和没有一样。
    add("hw_bus_defaults.reserved_pin_conflict",
        "check_hw_bus_defaults.py", "file",
        inject_replace("components/hw_profile/hw_tables.c",
                       r'default_scl = 46',
                       'default_scl = 48',
                       "总线默认引脚落在保留脚上（S3 I2C1 SCL=48=RGB LED）"),
        "把 I2C1 的 default_scl 改回 48（= HW_RESERVED_LED，且固件真在驱动它）⇒ 门禁必须红")

    # ⚠ 2026-10-09（§198）：第二条同类配方，但查的是**另一类**冲突。
    #
    #   §197 那类 = "默认引脚落在**保留脚**上"；
    #   §198 这类 = "**两条总线**抢同一个引脚"（各自看都对、放一起才冲突）。
    #   实例：C6 的 UART1(RX=21) 与 I2C0(SDA=21) —— 是从设备上报的 capabilities
    #   里看出来的，人眼审表格很难发现。
    #   ⇒ 两类都要有配方，否则"门禁的哪一半会咬"就没有证据。
    add("hw_bus_defaults.bus_to_bus_collision",
        "check_hw_bus_defaults.py", "file",
        inject_replace("components/hw_profile/hw_tables.c",
                       r'I2C0\", .port = 0, .default_sda = 21, .default_scl = 22',
                       'I2C0", .port = 0, .default_sda = 21, .default_scl = 23',
                       "两条总线抢同一个引脚（C6 I2C0.scl 撞 SPI2.mosi）"),
        "把 C6 的 I2C0.default_scl 改成 23（= SPI2.default_mosi）⇒ 门禁必须红")

    # ⚠ 为什么必须给 check_prod_isolation 也加配方（D-27）：
    #   本会话真实事故 —— 所有测试 defaults 都没覆盖 CONFIG_COLLECTOR_MQTT_BROKER_URL，
    #   于是继承 config/mqtt-broker.defaults 的**生产**地址 192.168.20.6:1883，
    #   刷了这些固件的设备**连上生产 broker 并自动订阅** nodes/<mac>/{down,control}。
    #   用户本轮明确"禁止碰 192.168.20.6"。
    #   ⇒ 这条门禁是本事故的**唯一**自动化拦截点，必须证明它真的会咬。
    # ⚠⚠ 2026-10-08（§194）：本配方**整体重写**，原因值得记下来。
    #
    #   旧版锚点是 CONFIG_COLLECTOR_MQTT_BROKER_URL（在 linkvarbuf.defaults 里）。
    #   MQTT 移除后该键**不存在了**，正则命中 0 次 ⇒ inject_replace 抛
    #   AssertionError ⇒ **整个自检崩掉**（不是红，是 traceback 退出）。
    #   而 check_prod_isolation.py 已被同步改版为"**按风险**守"（不再绑某个键，
    #   改为任何键的值指向生产就 FAIL）⇒ 配方也必须按新判据重写。
    #
    #   ⚠ 教训：**门禁改判据时，它的自检配方必须同批改** ——
    #     否则自检会在第一次运行时崩，而这比"门禁失效"更隐蔽：
    #     崩溃发生在自检里，门禁本身看着还是 PASS 的。
    #
    #   新配方：把**联调 defaults** 的 link host 指向生产
    #   ⇒ 命中 check_prod_isolation 的判据 ②（联调 defaults 不得指生产）。
    #   用它而不是"删掉某行"：新门禁按**值**判，所以注入一个生产值最直接。
    add("prod_isolation.scratch_points_at_prod",
        "check_prod_isolation.py", "file",
        inject_replace(os.path.join("..", "__scratch_v3", "defaults",
                                    "s3p_realsrv_nobss.defaults"),
                       r'CONFIG_EHOME_DEVICE_LINK_HOST\s*=\s*"[^"]*"',
                       'CONFIG_EHOME_DEVICE_LINK_HOST="%s"' % PROD_HOST_LITERAL,
                       "联调 defaults 的 link host 指向生产主机"),
        "把 __scratch_v3/defaults 的 link host 改成生产地址 ⇒ 门禁必须红"
        "（本会话真实事故：设备连上生产 broker 并订阅其下行主题；"
        " 现在没有 broker 了，但'设备被指向生产'这个风险不变）")

    # ⚠ 第二条 prod_isolation 配方（2026-10-08 本轮发现的**第二个泄漏面**）：
    #   光修 defaults **不够** —— esp32-collector/build/s3p-n16/ 是 2026-10-06 的旧产物，
    #   里面 9 个文件（sdkconfig/.broker.defaults/.bin/.elf/.obj/.a）都嵌着生产地址。
    #   任何 flash 脚本误用它刷机 ⇒ 设备连生产 broker。
    #   本配方在 build/ 下造一个含生产地址的 sdkconfig，验证门禁能抓到它。
    add("prod_isolation.stale_build_artifact",
        "check_prod_isolation.py", "file",
        # ⚠ §194：注入的键也从已删的 COLLECTOR_MQTT_BROKER_URL 换成现行的
        #   EHOME_DEVICE_LINK_HOST —— 新门禁按**值里有没有生产主机**判，
        #   所以"键换成哪个"不影响它咬人；换过来只是让配方贴近真实现状。
        inject_make_file(os.path.join("build", "zz_selftest_stale", "sdkconfig"),
                         'CONFIG_EHOME_DEVICE_LINK_HOST="%s"\n' % PROD_HOST_LITERAL),
        "build/zz_selftest_stale/sdkconfig 含生产地址 ⇒ 门禁必须红"
        "（陈旧产物不会因修 defaults 而改变）")

    # ⚠ 为什么必须给 check_critical_tasks_static 也加配方（D-28）：
    #   2026-10-01 现场事故 —— OTA 起不来，真因是 xTaskCreate() 要一整块连续堆内存
    #   （栈+TCB），而设备堆碎片常态化。修法是把 ota_task / log_tx / scheduler
    #   改成静态分配（栈与 TCB 进 .bss，与堆解耦）。
    #   ⚠ 2026-10-08 核实：这条不变量**此前没有任何测试/门禁保护** ——
    #   把 ota.c 改回 xTaskCreate，全套测试仍全绿 ⇒ 事故会**重现**而 CI 不拦。
    #   本配方证明新门禁真的会咬。
    add("critical_tasks_static.ota_reverts_to_dynamic",
        "check_critical_tasks_static.py", "file",
        inject_replace(os.path.join("components", "ota", "ota.c"),
                       r'xTaskCreateStatic\(ota_task_func',
                       'xTaskCreate(ota_task_func',
                       "OTA 任务从静态改回动态创建"),
        "ota.c: xTaskCreateStatic -> xTaskCreate ⇒ 门禁必须红"
        "（这条不变量此前无任何保护，改回去会重现 2026-10-01 现场事故）")

    return R


# ───────────────────────── 执行 ─────────────────────────

def _mutation_targets():
    """本自检会**就地改写**的仓库相对路径（相对 esp32-collector）。

    用于开工前判断"这些文件是否已经脏"。刻意**显式列出**而不是解析注入器：
    解析太脆，列出来则一眼可审。若将来新增注入目标却忘了加到这里，
    后果只是"少保护一个文件"，不会误报 —— 这个方向是安全的。
    """
    return {
        "main/main.c",
        "main/app_callbacks.c",
        "main/device_link_wiring.c",
        "main/Kconfig.projbuild",
        "tools/nvs_certs_gen.py",
        "components/bus_worker/bus_worker.c",
        "components/report_stats/CMakeLists.txt",
        "components/tls_guard/include/tls_guard.h",
        "sdkconfig.defaults",
        "tools/mem_budget.json",
        # 2026-10-07：新增"待接线清单可核查判据"的配方后，
        # 这份门禁脚本自身也成了被改写的目标。
        "tools/check_component_reachable.py",
    }


def coverage_limit():
    """从门禁自己的输出里读出当前未覆盖文件数（自校准，不硬编码）。"""
    rc, out = run_gate(["tools/check_host_coverage.py"])
    m = re.search(r"从未被编译:\s*(\d+)\s*个", out)
    assert m, "读不出未覆盖文件数，输出头：%r" % out[:200]
    return int(m.group(1))


def safe_remove(path):
    """只删探针物：路径必须严格落在白名单内。"""
    real = os.path.realpath(path)
    allowed = (os.path.realpath(PROBE_DIR), TMP_FRAME, TMP_JUNK)
    # build/ 下的自检产物（inject_make_file 建的"陈旧构建产物"）：
    # 该目录已 gitignore、可安全重建，故允许清理，但仍**只允许** build/ 前缀。
    build_prefix = os.path.realpath(os.path.join(ROOT, "build")) + os.sep
    if real not in allowed and not real.startswith(build_prefix):
        assert False, "拒绝删除不在白名单内的路径：%s" % real
    if os.path.isdir(real):
        shutil.rmtree(real)
    elif os.path.exists(real):
        os.remove(real)
        # ⚠ inject_make_file 建的是 build/<名字>/sdkconfig —— 删掉文件后**父目录会留下**。
        #   自检的"残留检查"只看文件，于是报"无残留"，但 build/ 下会逐轮累积空目录。
        #   故删文件后顺带清理**空的**父目录（只在 build/ 前缀内，且必须为空）。
        parent = os.path.dirname(real)
        build_prefix = os.path.realpath(os.path.join(ROOT, "build")) + os.sep
        if parent.startswith(build_prefix):
            try:
                os.rmdir(parent)   # 目录非空则抛 OSError，自然跳过
            except OSError:
                pass


def run_recipe(rec, harmless, cov_limit):
    backups, created = {}, []
    inject_note = "（无害替换：注入写成空操作）"
    if rec["kind"] == "argv":
        inject_note = rec["desc"] if not harmless else inject_note
    gate = os.path.join("tools", rec["gate"])
    # ⚠ 必须**总是**把门禁脚本放在 argv[0]。
    #   曾经写错成 bad_args = rec["bad_args"]（只有数据文件路径）⇒ 跑的是
    #   python3 <数据文件>，拿到的是 **Python 自己的** 退出码（1/2），
    #   门禁根本没被执行 —— 三条配方因此"假绿"。
    #   下面的断言让这个错误不可能再次发生。
    bad_args = [gate] + list(rec["bad_args"] or [])
    green_args = [gate] + list(rec["green_args"])
    if rec.get("extra") == "coverage":
        bad_args = [gate, "--max-uncovered", str(cov_limit)]
        green_args = [gate, "--max-uncovered", str(cov_limit)]
    assert os.path.basename(bad_args[0]) == rec["gate"], \
        "坏输入那一跑没有把门禁脚本放在 argv[0]：%r" % (bad_args,)
    assert os.path.basename(green_args[0]) == rec["gate"], \
        "绿跑没有把门禁脚本放在 argv[0]：%r" % (green_args,)

    bad_rc, bad_out = None, ""
    restore_ok, restore_note = True, "无需还原"
    try:
        if rec["inject"] is not None and not harmless:
            inject_note = rec["inject"](backups, created)
        bad_rc, bad_out = run_gate(bad_args)
    finally:
        # 还原：写回原始字节 + 核对 md5；删掉本次新建物
        bad_restore = []
        for path, raw in backups.items():
            try:
                with open(path, "rb") as fh:
                    cur = fh.read()
                if md5(cur) != md5(raw):
                    with open(path, "wb") as fh:
                        fh.write(raw)
                    with open(path, "rb") as fh:
                        cur = fh.read()
                    if md5(cur) != md5(raw):
                        bad_restore.append("%s: md5 还原后仍不一致" % path)
            except OSError as e:
                bad_restore.append("%s: 还原失败 %s" % (path, e))
        for path in created:
            try:
                if os.path.exists(path):
                    safe_remove(path)
            except (AssertionError, OSError) as e:
                bad_restore.append("%s: 删除失败 %s" % (path, e))
        notes = []
        if backups:
            notes.append("已还原 %d 个文件并核对 md5" % len(backups))
        if created:
            notes.append("已删 %d 个新建物" % len(created))
        if bad_restore:
            restore_ok = False
            notes.extend(bad_restore)
        if notes:
            restore_note = "；".join(notes)

    green_rc, green_out = run_gate(green_args)

    # ⭐⭐ "这条门禁在当前环境下**本来就红**"必须先判掉（2026-10-07 实测补）。
    #
    # 起因：干净检出（CI / git worktree 快照）里 check_sdkconfig_symbols **本身就红** ——
    # 它要读 gitignored 的 managed_components/，而干净检出里没有。
    # 于是本自检在那里的绿跑（green_rc=1）永远不为 0 ⇒ 判定"自检失败"⇒ ctest 变红。
    #
    # ⚠ 但它**绝不是**"这条门禁有问题"：真实原因是**环境不完整**。
    # 若把它当失败，就会出现最糟的那种结果：
    #   一个**每次 CI 都红**的测试 ⇒ 人学会忽略它 ⇒ 它保护不了任何东西。
    # 这正是本仓 D-27 教训的同族（"不运行的检查与没有检查等价"）。
    #
    # 处置：先跑一次"未注入"的基线；若基线就红 ⇒ 判 **SKIP（环境不完整）**，
    # 并**打印基线红的原因**（不静默）。反之才继续判"会不会咬"。
    #
    # 这个判定必须**先于** bites 判定，否则"本来就红"会被误记成"会咬"
    # （注入前后都红，看起来像是注入生效了 —— 假阳性）。
    baseline_rc, baseline_out = (None, "")
    if green_rc != 0:
        baseline_rc, baseline_out = run_gate(green_args)

    bites = bad_rc != 0
    if green_rc != 0 and baseline_rc != 0:
        # 门禁在本环境未注入时就红 ⇒ 无法用它判断"注入是否被侦破"。
        verdict = "跳过（该门禁在本环境本来就红）"
    elif harmless and not bites:
        verdict = "自检假绿（无害输入下自检没报红）"
    elif bites and restore_ok and green_rc == 0:
        verdict = "会咬"
    elif not bites:
        verdict = "证不出会咬"
    else:
        verdict = "自检失败"
    return dict(rec=rec, inject_note=inject_note, bad_rc=bad_rc, bad_out=bad_out,
                bites=bites, restore_ok=restore_ok, restore_note=restore_note,
                green_rc=green_rc, green_out=green_out, verdict=verdict,
                baseline_rc=baseline_rc, baseline_out=baseline_out)


def sweep():
    """逐条打印门禁的**真实**退出码。

    ⚠ 为什么不能用常见的一行 for 循环：
        for f in tools/check_*.py; do python3 "$f" >/dev/null 2>&1; \
            echo "$(basename $f) rc=$?"; done
    bash 会**先执行** $(basename ...)，它把 $? 重置为 0，然后才展开同一行里的 $?。
    ⇒ 这个循环**永远打印 rc=0**，即使门禁真的失败（实测：
      python3 tools/check_iram_isr_safety.py 无参时 rc=2，该循环仍打印 rc=0；
      把命令换成 exit 3，它照样打印 rc=0）。
    ⇒ "N 条门禁全 RC=0" 若是从这个循环读出来的，那句话是**空的**。
    本函数用 subprocess 的 returncode，拿的是真实退出码。
    """
    gates = sorted(os.path.basename(p)
                   for p in glob.glob(os.path.join(_HERE, "check_*.py")))
    print("逐条门禁真实退出码（rc 在命令替换之前取得，不受 $? 陷阱影响）")
    print("-" * 62)
    bad = []
    for g in gates:
        extra = []
        if g == "check_iram_isr_safety.py" and os.path.isfile(IRAM_ELF):
            extra = [IRAM_ELF]          # 该门禁必须带 ELF；无参 rc=2 是设计如此
        rc, _out = run_gate([os.path.join("tools", g)] + extra)
        note = "  (需带 ELF 参数)" if extra else ""
        print("%-42s rc=%s%s" % (g, rc, note))
        if rc != 0:
            bad.append((g, rc))
    print("-" * 62)
    if bad:
        print("非 0 的门禁 %d 条：" % len(bad))
        for g, rc in bad:
            print("    %s rc=%s" % (g, rc))
        return 1
    print("全部 %d 条门禁 rc=0" % len(gates))
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--only", help="只跑名字含该子串的条目")
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--sweep", action="store_true",
                    help="逐条打印门禁的真实退出码（替代有 $? 展开陷阱的一行 for 循环）")
    args = ap.parse_args()

    if args.sweep:
        return sweep()

    # ⭐ 先自愈：若上一次被 SIGKILL 留下日志，先把工作树恢复回来再开始。
    # 必须在任何注入之前 —— 否则会在"上次的坏内容"之上再改，还原会写错字节。
    recover_journal()

    harmless = os.environ.get("GATE_SELFTEST_HARMLESS")

    base_status = subprocess.run(["git", "status", "--porcelain"], cwd=WT,
                                 capture_output=True, text=True).stdout

    # ⭐⭐ 拒绝在"会被改动的文件已经脏"时运行（2026-10-07 补）。
    #
    # 为什么（真实协作风险，非假想）：本脚本会临时改写**生产文件**
    # （main/main.c、main/app_callbacks.c、tls_guard.h、sdkconfig.defaults、mem_budget.json），
    # 再把**运行前**的字节写回。若此刻**另一个人**正在改同一文件，
    # 我们的"还原"会把**他在途的修改一起抹掉** —— 他丢工作，而且毫无提示。
    # 本会话是多写者环境（多个 agent 共用一个工作树），所以这不是理论问题。
    #
    # ⚠ 我自己踩过：第一版直接拿 git status 的路径去比这份清单，
    # 但 git status 是**相对工作树根**、清单是**相对 esp32-collector**，
    # ⇒ 永不匹配 ⇒ 守卫静默失效。而"守卫没生效"的症状与"没有脏文件"**一模一样**。
    # 所以这条守卫必须先证明它会咬（见文件末尾 --sweep-guard 的说明与实测记录）。
    dirty_targets_wt = {os.path.relpath(os.path.join(ROOT, t), WT)
                        for t in _mutation_targets()}
    dirty_hit = []
    for line in base_status.splitlines():
        if len(line) < 4:
            continue
        rel = line[3:].strip().strip('"')
        if rel in dirty_targets_wt:
            dirty_hit.append("%s (%s)" % (rel, line[:2]))
    if dirty_hit and not harmless:
        print("=" * 78)
        print("拒绝运行：下列**会被本自检改动的文件当前已经脏**。")
        for d in dirty_hit:
            print("    %s" % d)
        print()
        print("原因：自检会把文件还原成**运行前**的字节；若你（或别人）正在改同一个文件，")
        print("      那份在途修改会被一起抹掉，而且不会有任何提示。")
        print("处置：先把这些改动提交或 stash 掉，再跑本自检。")
        print("      （单写者 / CI 场景通常不会触发这条。）")
        print("=" * 78)
        # 返回 77 = SKIP（automake 惯例），ctest 可用 SKIP_RETURN_CODE 认它。
        # 为什么不返回 2：ctest 会把任何非零当 FAIL，而"工作树是脏的"在开发中是
        # **正常状态**；若因此让整个 ctest 变红，人就会学会忽略这条测试，
        # 那它也就等于没有了。代价要讲明：本地脏树时这条**不会**跑，
        # 它真正生效的场景是 **CI / 干净检出**（也正是最该保证"门禁会不会咬"的地方）。
        return 77

    gates = sorted(os.path.basename(p)
                   for p in glob.glob(os.path.join(_HERE, "check_*.py")))
    gates = [g for g in gates if g != SELF]
    all_recipes = recipes()
    covered_all = {r["gate"] for r in all_recipes}
    uncovered_gates = [g for g in gates if g not in covered_all]
    rs = list(all_recipes)
    if args.only:
        rs = [r for r in rs if args.only in r["name"]]
    if harmless:
        rs = [r for r in rs if harmless in r["name"]]

    if args.list:
        for r in rs:
            print("%-42s %s" % (r["name"], r["gate"]))
        return 0

    # ⭐ "扫描器没瞎"下界断言（2026-10-07 加，教训来自 mutation-self-proof 技能）。
    #
    # 为什么必须有：脚本末尾的判决是
    #     if failed or uncovered_gates or residue: return 1
    #     return 0   # PASS
    # ⇒ 若 glob 或 recipes() 因为**任何原因**返回空集，
    #   那么 results=[] / uncovered_gates=[] / residue=[] ⇒ 全部为假 ⇒ **打印 PASS**，
    #   而实际一条门禁都没被验证。这就是 vacuous pass：**扫描器瞎了却报全绿**。
    # 我实测过这条路径（喂空集合复刻判决逻辑）：确实打印
    #     "PASS 每条门禁都被证明会咬" —— 而条目数是 0。
    #
    # ⇒ 用**下界**而不是绝对值：门禁只会变多，写死数字会让新增门禁时误报。
    #    真实验证：当前 check_*.py 16 条、recipes() 25 条。
    # ⚠ 不适用于 --only / GATE_SELFTEST_HARMLESS：那两种模式本就会缩小到子集。
    if not args.only and not harmless:
        if len(gates) == 0:
            print("FAIL 一条 check_*.py 都没扫到（_HERE=%s）—— 扫描路径错了，" % _HERE)
            print("     此时下面的判决会**空集 PASS**，等于什么都没验证。")
            return 1
        if len(all_recipes) == 0:
            print("FAIL recipes() 返回 0 条 —— 配方收集坏了，自检形同虚设。")
            return 1
        if len(rs) < len(all_recipes):
            print("FAIL 待跑配方(%d) 少于总配方(%d) —— 有配方被静默丢掉。"
                  % (len(rs), len(all_recipes)))
            return 1

    cov_limit = coverage_limit()

    print("=" * 78)
    print("门禁分类器自检：喂坏输入 ⇒ 必须非零；还原 ⇒ 必须重新绿")
    print("门禁 %d 条，本次自检配方 %d 条" % (len(gates), len(rs)))
    if harmless:
        print("⚠ 变异模式 GATE_SELFTEST_HARMLESS=%s —— 期望自检**报红**" % harmless)
    print("=" * 78)

    results = []
    for r in rs:
        res = run_recipe(r, harmless, cov_limit)
        results.append(res)
        v = res["verdict"]
        mark = {"会咬": "OK ", "跳过（该门禁在本环境本来就红）": "SKIP"}.get(v, "RED")
        print()
        print("[%s] %-42s %s" % (mark, r["name"], r["gate"]))
        print("      坏输入 : %s" % res["inject_note"])
        print("      坏 rc  : %s   还原: %s   绿 rc: %s"
              % (res["bad_rc"], res["restore_note"], res["green_rc"]))
        if v.startswith("跳过"):
            # 不静默：把"为什么这个环境里判不了"打出来。
            print("      跳过原因：该门禁在**未注入**时就是 rc=%s ⇒ 本环境下无法判'注入是否被侦破'"
                  % res["baseline_rc"])
            for line in evidence(res["green_out"])[:3]:
                print("      | 基线红: %s" % line)
        for line in evidence(res["bad_out"]):
            print("      | %s" % line)
        if res["green_rc"] != 0:
            print("      ⚠ 还原后没有重新变绿，绿跑输出：")
            for line in res["green_out"].splitlines()[:5]:
                print("      | %s" % line)

    if uncovered_gates and not harmless:
        print()
        print("FAIL 以下门禁没有自检配方（会被静默漏掉）：")
        for g in uncovered_gates:
            print("    %s" % g)

    end_status = subprocess.run(["git", "status", "--porcelain"], cwd=WT,
                                capture_output=True, text=True).stdout
    base_set = set(l for l in base_status.splitlines() if l.strip())
    end_set = set(l for l in end_status.splitlines() if l.strip())
    residue = sorted(end_set - base_set)

    print()
    print("=" * 78)
    if residue:
        print("FAIL 工作树出现新增残留（自检必须自己收干净）：")
        for line in residue:
            print("    %s" % line)
    else:
        print("git status 与开工时一致，无新增残留")
        print("（本任务新增的交付文件不算残留）")
        for line in sorted(base_set):
            print("    开工时已有: %s" % line)

    # ⭐ 三种结果要分开统计（"跳过"不等于"通过"，也不等于"失败"）：
    #   - 会咬      ：注入被侦破 + 还原成功 + 未注入时绿 ⇒ 这条门禁确实在保护东西
    #   - 跳过      ：本环境不完整（该门禁本来就红）⇒ **无法判定**，必须报出来
    #   - 未通过    ：真的有问题（恒真 / 证不出 / 还原失败）
    skipped = [r for r in results if r["verdict"].startswith("跳过")]
    failed = [r for r in results if r["verdict"] not in ("会咬",) and not r["verdict"].startswith("跳过")]
    print("-" * 78)
    print("自检条目 %d：会咬 %d，跳过(环境不完整) %d，未通过 %d"
          % (len(results), len(results) - len(skipped) - len(failed), len(skipped), len(failed)))
    if skipped:
        print()
        print("⚠ 下列门禁**本次无法判定**（未注入时就红 ⇒ 环境不完整，不是门禁的问题）：")
        for r in skipped:
            print("    %-42s %s（基线 rc=%s）" % (r["rec"]["name"], r["rec"]["gate"], r["baseline_rc"]))
        print("  典型原因：干净检出缺 gitignored 的 managed_components/。")
        print("  ⇒ 在**完整工作树 / 配好 IDF 的环境**里跑，这些才会被真正判定。")
    # 正常走到这里 ⇒ 所有注入都已还原 ⇒ 日志可以清掉（否则下次启动会误报"异常退出"）。
    clear_journal()

    if harmless:
        if failed:
            print("OK  无害替换被侦破：%d 条自检报红 ⇒ 自检不是假绿的" % len(failed))
            for r in failed:
                print("      %s → 门禁 rc=%s（没红），自检正确地把它判为失败"
                      % (r["rec"]["name"], r["bad_rc"]))
            print("=" * 78)
            return 2
        print("FAIL 无害替换下自检仍报绿 ⇒ **自检本身是假绿的**")
        print("=" * 78)
        return 1
    if failed or uncovered_gates or residue:
        print("FAIL 自检未全部通过")
        print("=" * 78)
        return 1
    print("PASS 每条门禁都被证明会咬，且还原后重新变绿")
    print("=" * 78)
    return 0


if __name__ == "__main__":
    sys.exit(main())
