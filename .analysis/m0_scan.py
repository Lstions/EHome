#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""M0 存量字段值扫描器（只读）— 数据字段解析层方案 v0.2 §4.4 / 评审报告 #6。

分类（严格对齐方案 §4.4 + §5.1）：
  literal  匹配 ^[a-z][a-z0-9_]*$ 且不含特殊文法标记 → 原样保留，行为不变。
  special  含 @ / _* / virtual: 形态（含"语法不合法但属于特殊族"的 near-miss，
           如 @、virtual:、Temperature_*）→ 标 needs_review，升级后不自动生效。
  legacy   其余值（大写/中文/空格/标点/超长 >64 等）→ 升级后仍按精确字面量匹配。

安全声明：本脚本只发 SELECT，且连接级设置 PGOPTIONS=-c default_transaction_read_only=on，
         由 PostgreSQL 在事务层强制只读（不是靠"我以为我只读"）。
         实证：SHOW transaction_read_only = on；同连接执行 UPDATE 探针被库拒绝
         （ERROR: cannot execute UPDATE in a read-only transaction），表内 0 行变化。
口径：
  分母 = 指定库内四类来源的**非 NULL 值行**（逐值计数，不是逐行）；
  单位 = 一条存储值 = 一个待分类字符串（conditions_json 按数组元素展开）；
  时刻 = 脚本运行时刻（报告里须写明）。

用法：
  python3 .analysis/m0_scan.py --selftest     # 合成数据自检（分类器正确性）
  python3 .analysis/m0_scan.py                # 扫描生产库（docker exec ehome-postgres）
  python3 .analysis/m0_scan.py --all-dbs      # 枚举本 PG 集群所有非模板库后逐个扫描
"""

import argparse
import json
import re
import subprocess
import sys

# ── 契约常量（与 backend/internal/sensorfield 冻结契约一致）────────────────────
MAX_SELECTOR_CHARS = 64                          # 方案 §5.1 规则 2
PHYSICAL_RE = re.compile(r"^[a-z][a-z0-9_]*$")   # 方案 §5.1 physical-name
# family 文法：prefix , "_*" , [ ":" , aggregate ]；prefix 以 lower 起始、以 lower/digit 结尾
FAMILY_RE = re.compile(r"^(?P<prefix>[a-z][a-z0-9_]*[a-z0-9])_\*(?::(?P<agg>[A-Za-z][A-Za-z0-9_]*))?$")
SEMANTIC_NAME_RE = PHYSICAL_RE
VIRTUAL_NAME_RE = PHYSICAL_RE
AGGREGATES = {"min", "max", "avg", "sum", "any", "all"}

CLASSES = ("literal", "special", "legacy")


def detect_special(s):
    """返回 (is_special, syntax_ok, subkind)。只判"形态族"，不判业务可解析性。"""
    if s.startswith("virtual:"):
        name = s[len("virtual:"):]
        return True, bool(VIRTUAL_NAME_RE.match(name)), "virtual"
    if "@" in s:
        name = s[1:] if s.startswith("@") else ""
        return True, bool(SEMANTIC_NAME_RE.match(name)), "semantic"
    if "_*" in s:
        m = FAMILY_RE.match(s)
        if not m:
            return True, False, "family"
        agg = m.group("agg")
        ok = agg is None or agg in AGGREGATES
        return True, ok, "family"
    return False, False, ""


def classify_value(raw):
    """把一条存量字符串分类为 literal / special / legacy。

    返回 dict: class / reason / trimmed_len / syntax_ok。
    顺序（有意为之，见 narrow-m0.md 分类口径）：
      1) TrimSpace（方案 §5.1 规则 1）；
      2) 空串 = legacy/empty（方案会报 empty_selector，存量需人工处置）；
      3) 特殊族形态优先判定（M0 的目的是"行为是否可能改变"，
         超长/大小写写错的 family 也属于 special 风险族，用 syntax_ok=False 单列）；
      4) 长度 >64 = legacy/too_long（方案 §4.4 明写"超长"归 legacy）；
      5) 不匹配物理名正则 = legacy/charset；
      6) 否则 literal。
    """
    if raw is None:
        return {"class": "skip", "reason": "null", "trimmed_len": 0, "syntax_ok": None}
    s = raw.strip()
    n = len(s)  # Unicode 字符数（方案 §5.1：VARCHAR(64) 按字符计）
    if n == 0:
        return {"class": "legacy", "reason": "empty", "trimmed_len": 0, "syntax_ok": None}

    is_special, syntax_ok, subkind = detect_special(s)
    if is_special:
        if n > MAX_SELECTOR_CHARS:
            return {"class": "special", "reason": subkind + "_too_long",
                    "trimmed_len": n, "syntax_ok": False}
        if not syntax_ok:
            return {"class": "special", "reason": subkind + "_malformed",
                    "trimmed_len": n, "syntax_ok": False}
        return {"class": "special", "reason": subkind, "trimmed_len": n, "syntax_ok": True}

    if n > MAX_SELECTOR_CHARS:
        return {"class": "legacy", "reason": "too_long", "trimmed_len": n, "syntax_ok": None}
    if not PHYSICAL_RE.match(s):
        return {"class": "legacy", "reason": "charset_or_shape", "trimmed_len": n,
                "syntax_ok": None}
    return {"class": "literal", "reason": "", "trimmed_len": n, "syntax_ok": None}


def extract_condition_sensor_names(raw_json):
    """从 conditions_json 提取 [].sensor_name。

    返回 (names, status)：status 属于 ok / ok_with_missing_sensor_name /
    invalid_json / not_array / empty / null。
    非法 JSON 本身也是一个存量风险（保存期不校验，运行期该规则永不触发）。
    """
    if raw_json is None:
        return [], "null"
    s = raw_json.strip()
    if not s or s == "[]":
        return [], "empty"
    try:
        payload = json.loads(s)
    except (ValueError, TypeError):
        return [], "invalid_json"
    if not isinstance(payload, list):
        return [], "not_array"
    names = []
    missing = 0
    for elem in payload:
        if not isinstance(elem, dict):
            missing += 1
            continue
        v = elem.get("sensor_name")
        if isinstance(v, str):
            names.append(v)
        else:
            missing += 1
    return names, ("ok" if missing == 0 else "ok_with_missing_sensor_name")


# ── 生产库读取（只读）──────────────────────────────────────────────────────────
def psql(container, user, db, sql, timeout=30):
    """执行只读查询；PGOPTIONS 在连接级把事务设为只读，由库强制（见文件头实证）。"""
    cmd = ["docker", "exec", "-e", "PGOPTIONS=-c default_transaction_read_only=on",
           container, "psql", "-X", "-U", user, "-d", db,
           "-Atq", "-F", "\x1f", "-R", "\x1e", "-c", sql]
    p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    if p.returncode != 0:
        raise RuntimeError("psql failed: " + (p.stderr.strip() or p.stdout.strip()))
    rows = []
    for rec in p.stdout.split("\x1e"):
        rec = rec.strip("\n")
        if not rec:
            continue
        rows.append(rec.split("\x1f"))
    return rows


def source_exists(container, user, db, table, column):
    sql = ("SELECT 1 FROM information_schema.columns WHERE table_schema='public' "
           "AND table_name='" + table + "' AND column_name='" + column + "'")
    return bool(psql(container, user, db, sql))


def new_stats():
    return {"rows": 0, "nonnull": 0, "classes": {c: 0 for c in CLASSES},
            "reasons": {}, "examples": {"special": [], "legacy": []}, "skipped": 0}


def add_value(stats, raw):
    stats["nonnull"] += 1
    r = classify_value(raw)
    if r["class"] == "skip":
        stats["skipped"] += 1
        return
    stats["classes"][r["class"]] += 1
    key = r["class"] + "/" + (r["reason"] or "-")
    stats["reasons"][key] = stats["reasons"].get(key, 0) + 1
    if r["class"] in ("special", "legacy") and len(stats["examples"][r["class"]]) < 8:
        stats["examples"][r["class"]].append((raw, r["reason"]))


def scan_aux(container, user, db):
    """补充证据（**不属于**方案 §4.4 的四类来源，仅作真实词表对照）：

    unified_data.sensor_name 是库内数据面实际的物理名词表（本库 17.9 万行）。
    它回答"M0 四类来源为 0 行时，真实物理名是否也都是 literal"——
    不能替代四类来源扫描，但能提供非空分母，避免"0 行=无风险"的误读。
    """
    if not source_exists(container, user, db, "unified_data", "sensor_name"):
        return {"absent": True}
    rows = psql(container, user, db,
                "SELECT sensor_name, count(*)::text FROM unified_data "
                "WHERE sensor_name IS NOT NULL AND sensor_name <> '' GROUP BY 1 ORDER BY 2 DESC")
    out = {"distinct": 0, "rows_total": 0, "classes": {c: 0 for c in CLASSES},
           "rows_by_class": {c: 0 for c in CLASSES}, "examples": {"special": [], "legacy": []}}
    for row in rows:
        name = row[0] if row else ""
        cnt = int(row[1]) if len(row) > 1 and row[1].isdigit() else 0
        out["distinct"] += 1
        out["rows_total"] += cnt
        r = classify_value(name)
        if r["class"] not in out["classes"]:
            continue
        out["classes"][r["class"]] += 1
        out["rows_by_class"][r["class"]] += cnt
        if r["class"] in ("special", "legacy") and len(out["examples"][r["class"]]) < 8:
            out["examples"][r["class"]].append((name, r["reason"]))
    return out


def scan_db(container, user, db):
    result = {"db": db, "sources": {}, "json": {}, "error": None, "self_proof": None}
    try:
        result["self_proof"] = psql(
            container, user, db,
            "SELECT current_database(), current_user, "
            "(SELECT count(*)::text FROM information_schema.tables WHERE table_schema='public')")
        plan = [
            ("alert_rules.sensor_name", "alert_rules", "sensor_name",
             "SELECT id, sensor_name FROM alert_rules WHERE sensor_name IS NOT NULL"),
            ("automation_rules.trigger_sensor_name", "automation_rules", "trigger_sensor_name",
             "SELECT id, trigger_sensor_name FROM automation_rules WHERE trigger_sensor_name IS NOT NULL"),
            ("data_sources.category", "data_sources", "category",
             "SELECT id, category FROM data_sources WHERE category IS NOT NULL"),
        ]
        for label, table, column, sql in plan:
            if not source_exists(container, user, db, table, column):
                result["sources"][label] = {"absent": True}
                continue
            st = new_stats()
            total = psql(container, user, db, "SELECT count(*)::text FROM " + table)
            st["rows"] = int(total[0][0]) if total else 0
            for row in psql(container, user, db, sql):
                add_value(st, row[1] if len(row) > 1 else "")
            result["sources"][label] = st

        # conditions_json[].sensor_name（JSON 在脚本侧解析，避免非法 JSON 拖垮整条 SQL）
        label = "automation_rules.conditions_json[].sensor_name"
        if source_exists(container, user, db, "automation_rules", "conditions_json"):
            st = new_stats()
            json_stat = {"invalid_json": 0, "not_array": 0, "empty": 0,
                         "no_sensor_name": 0, "elements": 0, "rules_with_json": 0}
            sql = ("SELECT id, conditions_json FROM automation_rules "
                   "WHERE conditions_json IS NOT NULL AND btrim(conditions_json) <> ''")
            rows = psql(container, user, db, sql)
            st["rows"] = len(rows)
            json_stat["rules_with_json"] = len(rows)
            for row in rows:
                names, status = extract_condition_sensor_names(row[1] if len(row) > 1 else "")
                if status in ("invalid_json", "not_array", "null"):
                    key = "invalid_json" if status == "invalid_json" else status
                    json_stat[key] += 1
                    continue
                if status == "empty":
                    json_stat["empty"] += 1
                    continue
                json_stat["elements"] += len(names)
                if status == "ok_with_missing_sensor_name":
                    json_stat["no_sensor_name"] += 1
                for name in names:
                    add_value(st, name)
            result["sources"][label] = st
            result["json"] = json_stat
        else:
            result["sources"][label] = {"absent": True}

        # 补充证据（非 §4.4 四类来源）：unified_data.sensor_name 实际词表
        try:
            result["aux"] = scan_aux(container, user, db)
        except Exception as exc:
            result["aux"] = {"error": str(exc)}
    except Exception as exc:  # 扫描失败必须显式失败，不静默当 0
        result["error"] = str(exc)
    return result


# ── 合成数据自检（分类器正确性）────────────────────────────────────────────────
SYNTHETIC_VALUES = [
    # (raw, expected_class, expected_syntax_ok, why)
    ("rsoc", "literal", None, "普通字面量"),
    ("cell_voltage_10", "literal", None, "方案 §5.1 用例 14：不得被当成 family"),
    ("virtual", "literal", None, "方案 §5.1 用例 13：无冒号 = literal"),
    ("a" * 64, "literal", None, "边界：64 字符 literal 合法（用例 10）"),
    ("a" * 65, "legacy", None, "边界：65 字符 = selector_too_long → legacy/too_long"),
    ("temperature_*", "special", True, "family 合法形态"),
    ("temperature_*:max", "special", True, "family + 合法聚合"),
    ("temperature_*:MAX", "special", False, "family + 聚合大小写错 = special/malformed"),
    ("@soc", "special", True, "semantic 合法形态"),
    ("virtual:power", "special", True, "virtual 合法形态"),
    ("Temperature", "legacy", None, "大写 = legacy/charset"),
    ("温度", "legacy", None, "非 ASCII = legacy/charset"),
    ("   ", "legacy", None, "trim 后空 = legacy/empty"),
    ("a_b c", "legacy", None, "含空格 = legacy/charset"),
]

SYNTHETIC_JSON = [
    ('[{"sensor_name":"temperature_*"},{"sensor_name":"@soc"}]',
     ["temperature_*", "@soc"], "ok"),
    ("not-a-json", [], "invalid_json"),
    ('[{"sensor":"humidity"}]', [], "ok_with_missing_sensor_name"),
    ("[]", [], "empty"),
]


def selftest():
    failures = 0
    print("## 合成数据自检（纯函数，方案 §4.4 分类器）\n")
    print("| # | 输入（repr） | 期望 class | 实际 class | syntax_ok | 结果 |")
    print("|---|---|---|---|---|---|")
    for i, (raw, want_cls, want_syn, why) in enumerate(SYNTHETIC_VALUES, 1):
        got = classify_value(raw)
        ok = got["class"] == want_cls and (want_syn is None or got["syntax_ok"] == want_syn)
        if not ok:
            failures += 1
        shown = repr(raw)
        if len(shown) > 28:
            shown = shown[:25] + "..."
        print("| %d | %s | %s | %s | %s | %s |" % (
            i, shown, want_cls, got["class"], got["syntax_ok"],
            "PASS" if ok else "**FAIL**"))
    print("\n## 合成数据自检（conditions_json 提取）\n")
    print("| # | 输入 | 期望 names | 期望 status | 实际 | 结果 |")
    print("|---|---|---|---|---|---|")
    for i, (raw, want_names, want_status) in enumerate(SYNTHETIC_JSON, 1):
        names, status = extract_condition_sensor_names(raw)
        ok = names == want_names and status == want_status
        if not ok:
            failures += 1
        print("| %d | %s | %s | %s | names=%s status=%s | %s |" % (
            i, raw.replace("|", "\\|"), want_names, want_status, names, status,
            "PASS" if ok else "**FAIL**"))
    total = len(SYNTHETIC_VALUES) + len(SYNTHETIC_JSON)
    print("\n合成用例：%d 条，失败 %d 条。" % (total, failures))
    if failures == 0:
        print("SELFTEST: PASS")
        return 0
    print("SELFTEST: FAIL")
    return 2


# ── 主流程 ────────────────────────────────────────────────────────────────────
def print_result(res):
    print("\n### DB " + res["db"])
    if res.get("self_proof"):
        proof = res["self_proof"][0]
        print("自证：current_database=%s current_user=%s public_tables=%s" % (
            proof[0] if len(proof) > 0 else "?",
            proof[1] if len(proof) > 1 else "?",
            proof[2] if len(proof) > 2 else "?"))
    if res["error"]:
        print("**扫描失败**：" + res["error"])
        return
    print("\n| 来源 | 表行数 | 非 NULL 值数 | literal | special | legacy |")
    print("|---|---:|---:|---:|---:|---:|")
    for label, st in res["sources"].items():
        if st.get("absent"):
            print("| %s | - | - | - | - | - | （表/列不存在）" % label)
            continue
        c = st["classes"]
        print("| %s | %d | %d | %d | %d | %d |" % (
            label, st["rows"], st["nonnull"], c["literal"], c["special"], c["legacy"]))
    for label, st in res["sources"].items():
        if st.get("absent"):
            continue
        for raw, reason in st["examples"]["special"]:
            print("- special（needs_review，不自动生效）: %s: %r（%s）" % (label, raw, reason))
    for label, st in res["sources"].items():
        if st.get("absent"):
            continue
        for raw, reason in st["examples"]["legacy"]:
            print("- legacy: %s: %r（%s）" % (label, raw, reason))
    if res.get("json"):
        print("\nconditions_json 解析：%s" % json.dumps(res["json"], ensure_ascii=False))
    aux = res.get("aux")
    if aux and not aux.get("error") and not aux.get("absent"):
        print("\n补充证据（非 §4.4 四类来源）：unified_data.sensor_name 词表 "
              "distinct=%d rows=%d → literal=%d special=%d legacy=%d" % (
                  aux["distinct"], aux["rows_total"],
                  aux["classes"]["literal"], aux["classes"]["special"], aux["classes"]["legacy"]))
        print("（按行加权：literal=%d special=%d legacy=%d）" % (
            aux["rows_by_class"]["literal"], aux["rows_by_class"]["special"],
            aux["rows_by_class"]["legacy"]))
        for raw, reason in aux["examples"]["special"]:
            print("- 补充 special: %r（%s）" % (raw, reason))
        for raw, reason in aux["examples"]["legacy"]:
            print("- 补充 legacy: %r（%s）" % (raw, reason))


def main():
    ap = argparse.ArgumentParser(description="M0 存量字段值扫描（只读）")
    ap.add_argument("--selftest", action="store_true", help="合成数据自检")
    ap.add_argument("--container", default="ehome-postgres")
    ap.add_argument("--user", default=None, help="默认取容器 POSTGRES_USER")
    ap.add_argument("--db", default=None, help="默认取容器 POSTGRES_DB")
    ap.add_argument("--all-dbs", action="store_true",
                    help="枚举本 PG 集群所有非模板库（更完整的部署分母）")
    args = ap.parse_args()

    if args.selftest:
        return selftest()

    user = args.user or subprocess.run(
        ["docker", "exec", args.container, "printenv", "POSTGRES_USER"],
        capture_output=True, text=True).stdout.strip()
    db = args.db or subprocess.run(
        ["docker", "exec", args.container, "printenv", "POSTGRES_DB"],
        capture_output=True, text=True).stdout.strip()
    if not user or not db:
        print("无法从容器取得 POSTGRES_USER/POSTGRES_DB；请显式传 --user/--db", file=sys.stderr)
        return 1

    dbs = [db]
    if args.all_dbs:
        rows = psql(args.container, user, db,
                    "SELECT datname FROM pg_database WHERE NOT datistemplate "
                    "AND datname NOT LIKE 'ehome_sim_%' ORDER BY datname")
        dbs = [r[0] for r in rows]
        print("部署分母（PG 集群非模板库，排除 ehome_sim_* 仿真库）：" + ", ".join(dbs))

    results = [scan_db(args.container, user, d) for d in dbs]
    for res in results:
        print_result(res)

    totals = {c: 0 for c in CLASSES}
    scanned = 0
    for res in results:
        for st in res["sources"].values():
            if st.get("absent"):
                continue
            scanned += st["nonnull"]
            for c in CLASSES:
                totals[c] += st["classes"][c]
    print("\n## 汇总（分母 = %d 个库，%d 条非 NULL 值）" % (len(dbs), scanned))
    print("literal=%d special=%d legacy=%d" % (
        totals["literal"], totals["special"], totals["legacy"]))
    if scanned == 0:
        print("\n> WARNING：本次扫描为 0 行。0 行只证明『此刻这个库没有存量值』，"
              "不能证明其他部署/其他快照无风险；分类器的正确性由 --selftest 的合成用例负责。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
