#!/usr/bin/env python3
"""mem_budget_check.py — 构建期固件内存预算门禁（WS-G）。

用法示例：
    # 推荐：BUILD_ROOT 与 build_firmware.sh 保持同一变量；map 不存在会报错，
    # 不会静默回退到仓库里的旧 map。
    BUILD_ROOT=/tmp/ehome_memgate_build \
        python3 tools/mem_budget_check.py --profile s3-n16

    # 不设 BUILD_ROOT 时自动找最新 build*/.fwbuild*/<profile>/ehome_collector.map
    python3 tools/mem_budget_check.py --profile s3-n16

    # 显式指定历史 map（profile 从路径推断；也可同时给 --profile）
    python3 tools/mem_budget_check.py \
        --map /mnt/storage/WorkSpace/EHome/esp32-collector/build61/c6-n16/ehome_collector.map

    # 只看报告不按退出码拦截（用于基线记录/阈值调整期）
    python3 tools/mem_budget_check.py --profile s3-n16 --report-only

    # known_pending（如 task-3 尚未完成的 IRAM→flash）默认只打印 PENDING 不阻塞；
    # 传 --strict 时也按 FAIL 拦截。
    python3 tools/mem_budget_check.py --profile s3-n16 --strict

退出码：
    0  全部通过（或只有默认放行的 known_pending / --report-only）
    1  有预算超限（--strict 下 known_pending 也算）
    2  无法验证（map 不存在、esp-idf-size 不可用等）


门禁口径（方案 §2.3 / task-6）：
    - DIRAM 静态占用 = idf.py size 的 DIRAM used（.dram0.bss + .dram0.data +
      IRAM 别名段落在数据总线视图里的 .text），按组件 top-N 输出。
    - 静态任务栈：.dram0.bss 里符号名含 "stack" 的静态数组（FreeRTOS
      xTaskCreateStatic 用），单独列出；它们已经计入 DIRAM used，不重复相加。
    - IRAM 段：idf.py size 的 IRAM used/remain。S3 的 IRAM 只有 16 KiB，
      规格要求 remain >= 4 KiB。
    - C6 没有独立 IRAM 段，代码在 flash，检查 DIRAM remain >= 20 KiB。

阈值全部在 tools/mem_budget.json 内，按 profile 覆盖 defaults。
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import re
import subprocess
import sys
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
PROJECT_DIR = SCRIPT_DIR.parent  # esp32-collector/

# 与 memorymap/esp_idf_size 的 Memory Type 名称保持一致
MEM_DIRAM = "DIRAM"
MEM_IRAM = "IRAM"


# --------------------------------------------------------------------------
# map 定位
# --------------------------------------------------------------------------
def find_map(project_dir: Path, profile: str, explicit: str | None) -> Path | None:
    if explicit:
        p = Path(explicit)
        if not p.is_absolute():
            p = (Path.cwd() / p).resolve()
        return p if p.is_file() else None

    # BUILD_ROOT（build_firmware.sh 支持）是显式输入，**优先且唯一**：
    # 它允许构建目录在 /tmp 或某个 teammate 的隔离目录，而不用猜。
    build_root = os.environ.get("BUILD_ROOT")
    if build_root:
        p = Path(build_root) / profile / "ehome_collector.map"
        if p.is_file():
            return p
        # 显式指定但不存在时要报错，而不是静默回退到仓库里的旧 map。
        return None

    # 否则按常见命名搜集；同一 profile 取 mtime 最新者。
    candidates: list[Path] = []
    patterns = [
        project_dir / "build*" / profile / "ehome_collector.map",
        project_dir / ".fwbuild*" / profile / "ehome_collector.map",
        project_dir / "build" / profile / "ehome_collector.map",
    ]
    for pat in patterns:
        candidates.extend(Path(p) for p in glob.glob(str(pat)))
    candidates = sorted(set(candidates), key=lambda p: p.stat().st_mtime, reverse=True)
    return candidates[0] if candidates else None


def run_idf_size(py: str, fmt: str, map_path: Path, archives: bool) -> tuple[int, str, str]:
    cmd = [py, "-m", "esp_idf_size", "--format", fmt]
    if archives:
        cmd.append("--archives")
    cmd.append(str(map_path))
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
    except Exception as exc:  # noqa: BLE001
        return 127, "", str(exc)
    return proc.returncode, proc.stdout, proc.stderr


def candidate_pythons() -> list[str]:
    out = [sys.executable]
    env_py = os.environ.get("IDF_PYTHON_ENV_PATH")
    if env_py:
        for name in ("bin/python", "bin/python3", "Scripts/python.exe"):
            p = Path(env_py) / name
            if p.is_file():
                out.append(str(p))
    # 去重保序
    seen: set[str] = set()
    uniq: list[str] = []
    for p in out:
        if p not in seen:
            seen.add(p)
            uniq.append(p)
    return uniq


def load_layout(map_path: Path) -> tuple[dict, str]:
    """返回 (memory_type_name -> layout dict, python used)。失败抛 RuntimeError。"""
    errs: list[str] = []
    for py in candidate_pythons():
        rc, out, err = run_idf_size(py, "json2", map_path, archives=False)
        if rc == 0 and out.strip():
            data = json.loads(out)
            return {item["name"]: item for item in data.get("layout", [])}, py
        errs.append(f"{py}: rc={rc} {err.strip()[:200]}")
    raise RuntimeError("esp-idf-size 不可用，无法解析布局: " + "; ".join(errs))


def true_iram_usage(map_path: Path) -> dict | None:
    """从 map 的链接器事实里读出 IRAM 段的真实容量与占用。

    为什么需要这个函数（L-04，2026-10-06）：
      esp_idf_size 报的 IRAM "total" **不是 IRAM 段的容量**。在 S3 上
      iram0_0_seg(0x57700) 与 dram0_0_seg(0x53700) 是同一物理 SRAM 的两个
      总线别名，idf_size 把两者长度之差 (0x4000 = 16384) 当作 IRAM 的
      "size"，于是恒报 total=used=16384 / free=0 —— 无论实际用了多少。

    实测反证（2026-10-06）：往 IRAM 里放 32 KiB 并链接，_iram_end 从
      0x40389800 移到 0x40391800（+32768 B），**构建成功**，而报告的
      IRAM free 始终是 0。⇒ "IRAM 满、新增 IRAM 代码会链接失败"是假警报。

    真实口径取自 map 自身：Memory Configuration 里 iram0_0_seg 的
      ORIGIN/LENGTH 与符号 _iram_end。链接器还带着权威断言：
        ASSERT(((_iram_end - ORIGIN(iram0_0_seg)) <= LENGTH(iram0_0_seg)),
               "IRAM0 segment data does not fit.")

    返回 None 表示解析不到（调用方必须按"无法验证"处理，不能当通过）。
    """
    seg = None
    iram_end = None
    try:
        with open(map_path, "r", errors="replace") as fh:
            for line in fh:
                if seg is None:
                    m = _IRAM_SEG_RE.match(line)
                    if m:
                        seg = (int(m.group(2), 16), int(m.group(3), 16))
                        continue
                if iram_end is None:
                    m = _IRAM_END_RE.match(line)
                    if m:
                        iram_end = int(m.group(1), 16)
                if seg is not None and iram_end is not None:
                    break
    except OSError:
        return None
    if seg is None or iram_end is None:
        return None
    origin, length = seg
    used = iram_end - origin
    return {"origin": origin, "length": length, "end": iram_end,
            "used": used, "free": length - used}

def load_archives(map_path: Path, py: str) -> dict:
    rc, out, err = run_idf_size(py, "json2", map_path, archives=True)
    if rc != 0 or not out.strip():
        raise RuntimeError(f"esp-idf-size --archives 失败: rc={rc} {err.strip()[:200]}")
    return json.loads(out)


# --------------------------------------------------------------------------
# map 文本扫描：静态任务栈 + 备用组件归因
# --------------------------------------------------------------------------
_TARGET_SECTIONS = {
    ".dram0.bss", ".dram0.data", ".noinit",          # DIRAM 数据
    ".iram0.text", ".iram0.vectors", ".iram0.data", ".iram0.bss",  # IRAM
}
_OUT_SEC_RE = re.compile(r"^(\.(?:dram0|iram0|noinit)[\w.]*)\s+(0x[0-9a-fA-F]+)\s+(0x[0-9a-fA-F]+)")
# Memory Configuration 行:  iram0_0_seg      0x40374000         0x00057700         xr
_IRAM_SEG_RE = re.compile(r"^\s*(iram0_0_seg)\s+(0x[0-9a-fA-F]+)\s+(0x[0-9a-fA-F]+)")
# 链接输出行:               0x40389800                        _iram_end = ABSOLUTE (.)
_IRAM_END_RE = re.compile(r"^\s+(0x[0-9a-fA-F]{8})\s+_iram_end = ABSOLUTE")
_ENTRY_RE = re.compile(
    r"^\s+(?:(\.[\w.]+)\s+)?(0x[0-9a-fA-F]+)\s+(0x[0-9a-fA-F]+)\s+(\S+\.a\(\S+\))\s*$"
)


def scan_map(map_path: Path) -> dict:
    """扫描 map：返回 {'static_task_stacks': [(name,size,archive)...]}。"""
    static_stacks: list[tuple[str, int, str]] = []
    current_section: str | None = None
    pending_name: str | None = None

    with open(map_path, "r", errors="replace") as fh:
        for line in fh:
            if current_section is None:
                # 只在 Memory Configuration 之后开始，避免命中 Discarded input sections
                if line.startswith("Memory Configuration"):
                    current_section = ""
                    pending_name = None
                continue

            m_sec = _OUT_SEC_RE.match(line)
            if m_sec:
                current_section = m_sec.group(1)
                pending_name = line.rstrip("\n").split()[0]
                continue

            if current_section not in _TARGET_SECTIONS:
                pending_name = None
                continue

            # map 有两种写法：
            #   .bss.sym 0xaddr 0xsize archive   （同一行，本仓常见于 BSS 小对象）
            #   .bss.sym\n  0xaddr 0xsize archive（符号名独占一行，常见于对齐对象）
            # 后者必须先记住符号名，否则会漏掉所有静态任务栈（s_*_stack）。
            m_name_only = re.match(r"^\s+(\.[\w.]+)\s*$", line)
            if m_name_only:
                pending_name = m_name_only.group(1)
                continue

            m = _ENTRY_RE.match(line)
            if not m:
                continue
            sec_name, _addr, size_s, archive = m.group(1), m.group(2), m.group(3), m.group(4)
            symbol = None
            if sec_name and sec_name.startswith(".bss."):
                symbol = sec_name[len(".bss."):]
            elif pending_name and pending_name.startswith(".bss."):
                symbol = pending_name[len(".bss."):]
            pending_name = None

            size = int(size_s, 16)
            if size <= 0 or not symbol:
                continue
            low = symbol.lower()
            # 静态任务栈：xTaskCreateStatic 的 StackType_t 数组。
            # 只取名字里明确带 stack 且不是 tcb/high_water 的符号。
            if "stack" in low and "tcb" not in low and "high_water" not in low:
                static_stacks.append((symbol, size, archive))

    # 传递性去重（同一个符号可能出现一次）；保留原始
    return {"static_task_stacks": static_stacks}


# --------------------------------------------------------------------------
# 组件归因
# --------------------------------------------------------------------------
def component_name(archive_path: str, abbrev: str) -> str:
    """把 archive 路径/短名归一到组件名，例如 libbus_worker.a -> bus_worker。"""
    name = abbrev or os.path.basename(archive_path)
    m = re.search(r"esp-idf/([^/]+)/lib([^/]+)\.a$", archive_path)
    if m:
        return m.group(1)
    m = re.match(r"lib(.+)\.a$", name)
    if m:
        return m.group(1)
    return name


def top_components(archives: dict, mem_type: str, print_all: bool = False) -> list[tuple[str, int]]:
    agg: dict[str, int] = {}
    for _path, info in archives.items():
        mt = info.get("memory_types", {}).get(mem_type)
        if not mt:
            continue
        size = int(mt.get("size", 0))
        if size <= 0:
            continue
        comp = component_name(_path, info.get("abbrev_name", ""))
        agg[comp] = agg.get(comp, 0) + size
    _ = print_all
    return sorted(agg.items(), key=lambda kv: kv[1], reverse=True)


# --------------------------------------------------------------------------
# 主流程
# --------------------------------------------------------------------------
def main() -> int:
    ap = argparse.ArgumentParser(description="固件静态内存预算门禁")
    ap.add_argument("--profile", required=False, help="profile 名，如 s3-n16 / s3p-n16 / c6-n16")
    ap.add_argument("--map", required=False, help="显式 map 路径；默认自动找最新 build*/<profile>/")
    ap.add_argument("--thresholds", default=str(SCRIPT_DIR / "mem_budget.json"), help="阈值 JSON")
    ap.add_argument("--top", type=int, default=10, help="组件 top-N（默认 10）")
    ap.add_argument("--report-only", action="store_true", help="只报告，不按退出码拦截")
    ap.add_argument("--strict", action="store_true",
                    help="known_pending 的缺口也按 FAIL 拦截（默认只打印 PENDING）")
    ap.add_argument("--allow-pending", action="store_true",
                    help="兼容别名：known_pending 缺口不阻塞（默认行为）")
    args = ap.parse_args()

    thr_path = Path(args.thresholds)
    if not thr_path.is_file():
        print(f"ERROR: 阈值文件不存在: {thr_path}", file=sys.stderr)
        return 2
    thr_all = json.loads(thr_path.read_text())
    defaults = thr_all.get("defaults", {})

    map_path = find_map(PROJECT_DIR, args.profile or "", args.map)
    if map_path is None:
        print(f"ERROR: 找不到 {args.profile} 的 ehome_collector.map（可用 --map 指定）", file=sys.stderr)
        return 2

    profile = args.profile
    if not profile:
        # 从路径反推：.../<profile>/ehome_collector.map
        profile = map_path.parent.name
    thresholds = dict(defaults)
    thresholds.update(thr_all.get("profiles", {}).get(profile, {}))

    print(f"=== mem_budget_check: profile={profile} ===")
    print(f"map: {map_path}")
    try:
        layout, py_used = load_layout(map_path)
    except RuntimeError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    print(f"parser: {py_used} -m esp_idf_size")

    raw = scan_map(map_path)
    static_stacks = sorted(raw["static_task_stacks"], key=lambda t: t[1], reverse=True)
    static_stack_total = sum(s for _n, s, _a in static_stacks)

    diram = layout.get(MEM_DIRAM, {})
    iram = layout.get(MEM_IRAM, {})
    dir_used = int(diram.get("used", 0))
    dir_total = int(diram.get("total", 0))
    dir_remain = int(diram.get("free", 0)) if dir_total else 0
    iram_used = int(iram.get("used", 0))
    iram_total = int(iram.get("total", 0))
    iram_remain = int(iram.get("free", 0)) if iram_total else 0
    # 链接器事实口径（L-04）。None = 该目标无 IRAM 段（如 C6）或解析失败；
    # 两种情况都不做 IRAM 校验，但失败信息不同，见下方打印。
    tiu = true_iram_usage(map_path)

    print()
    print(f"DIRAM used={dir_used} total={dir_total} remain={dir_remain}")
    if iram_total:
        print(f"IRAM  used={iram_used} total={iram_total} remain={iram_remain}")
    else:
        print("IRAM  该目标无独立 IRAM 段（代码在 flash）")
    if tiu:
        print(f"IRAM  真实段（链接器口径）: used={tiu['used']} length={tiu['length']} remain={tiu['free']}"
              f"   [idf_size 分栏报 used={iram_used} total={iram_total} remain={iram_remain}，该栏 total 与段容量无关]")
    print(f"静态任务栈（含在 DIRAM used 内）: total={static_stack_total} B")
    for name, size, archive in static_stacks:
        print(f"    {size:7d}  {name:24s} {archive}")

    # 组件 top-N（优先 archives json；失败则跳过）
    top_dir: list[tuple[str, int]] = []
    top_iram: list[tuple[str, int]] = []
    try:
        archives = load_archives(map_path, py_used)
        top_dir = top_components(archives, MEM_DIRAM)[: args.top]
        if iram_total:
            top_iram = top_components(archives, MEM_IRAM)[: args.top]
    except RuntimeError as exc:
        print(f"WARN: {exc}; 跳过组件 top-N", file=sys.stderr)

    if top_dir:
        print(f"\nDIRAM 组件 top-{args.top}（含 .bss/.data/别名 .text）:")
        for i, (name, size) in enumerate(top_dir, 1):
            print(f"  {i:2d}. {name:34s} {size:8d} B")
    if top_iram:
        print(f"IRAM 组件 top-{args.top}:")
        for i, (name, size) in enumerate(top_iram, 1):
            print(f"  {i:2d}. {name:34s} {size:8d} B")

    # ---------------- 校验 ----------------
    checks: list[tuple[str, int, int, str]] = []  # (name, value, limit, op) op=max|min
    if "dir_used_max" in thresholds:
        checks.append(("DIRAM used", dir_used, int(thresholds["dir_used_max"]), "max"))
    if "dir_remain_min" in thresholds and dir_total:
        checks.append(("DIRAM remain", dir_remain, int(thresholds["dir_remain_min"]), "min"))
    # IRAM 校验用【链接器事实】，不用 idf_size 的 IRAM 分栏（L-04：那一栏的
    # total 恒为 16384，与真实段容量 358,144 B 无关，见 true_iram_usage 注释）。
    if "iram_used_max" in thresholds and tiu:
        checks.append(("IRAM used (true)", tiu["used"], int(thresholds["iram_used_max"]), "max"))
    if "iram_remain_min" in thresholds and tiu:
        checks.append(("IRAM remain (true)", tiu["free"], int(thresholds["iram_remain_min"]), "min"))

    known_pending = thresholds.get("known_pending", {})
    if isinstance(known_pending, list):  # 兼容简写：["iram_remain_min"]
        known_pending = {k: "(未注明原因)" for k in known_pending}

    print("\n--- checks ---")
    failures = 0
    pending = 0
    for name, value, limit, op in checks:
        if op == "max":
            ok = value <= limit
            limit_s = f"<= {limit}"
        else:
            ok = value >= limit
            limit_s = f">= {limit}"
        key = {
            "DIRAM used": "dir_used_max",
            "DIRAM remain": "dir_remain_min",
            "IRAM used": "iram_used_max",
            "IRAM remain": "iram_remain_min",
            # 真实口径的 IRAM 校验同属这两个预算键（同一预算，两个口径）
            "IRAM used (true)": "iram_used_max",
            "IRAM remain (true)": "iram_remain_min",
        }[name]
        if ok:
            status = "PASS"
        elif key in known_pending:
            status = "PENDING"
            pending += 1
        else:
            status = "FAIL"
            failures += 1
        extra = ""
        if status == "PENDING":
            extra = f"  ({known_pending[key]})"
        print(f"  {status:7s} {name:14s} {value:8d}  {limit_s:>10s}{extra}")

    if failures:
        print(f"\nRESULT: FAIL ({failures} 项超限, {pending} 项已知待办)")
    elif pending:
        print(f"\nRESULT: PASS ({pending} 项已知待办，默认不阻塞；加 --strict 可拦)")
    else:
        print("\nRESULT: PASS")
    print("提示：接入方式见 esp32-collector/tools/README-mem-budget.md 与 build_firmware.sh 建议插入点。")

    if args.report_only:
        return 0
    if failures:
        return 1
    if pending and args.strict:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
