#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""门禁 11 / 回滚盘点：列出库中所有「非字面量」selector 取值（只读）。

为什么它是代码回退的前置条件（方案 §14.3 / §12.3#11）：
  升级后用户可能已保存 @soc / temperature_* / virtual:... ；
  把旧二进制回退后，这些值会被当作物理名精确匹配 —— 规则静默变成死规则。
  因此「无本脚本盘点结果，不得执行代码回退」。

只读保证：所有查询都带 PGOPTIONS=-c default_transaction_read_only=on，
  由 PostgreSQL 在事务层强制只读（不是「我以为我只读」）。

数据来源（M0：四类来源，与方案 §4.4 一致）：
  1) alert_rules.sensor_name
  2) automation_rules.trigger_sensor_name
  3) automation_rules.conditions_json[].sensor_name
  4) data_sources.category

分类（与 §4.4 / §5.1 一致）：
  literal  匹配 ^[a-z][a-z0-9_]*$ 且不含特殊标记（行为不变，可回退）
  special  含 @ / _* / virtual: 形态（含 near-miss：@、virtual:、Temperature_*）
  legacy   其余（大写/中文/空格/标点/超长）—— 旧代码按精确字面量匹配，需人工处置
  invalid_json  conditions_json 无法解析 / 数组元素缺 sensor_name（单列，不得混入 legacy）

用法：
  ./scripts/check_field_selectors.sh              # 等价 --check
  ./scripts/check_field_selectors.sh --json       # 机器可读
  ./scripts/check_field_selectors.sh --selftest   # 分类器自检（不连库）
退出码：0=无非字面量（可回退）；1=存在非字面量（禁止回退）；77=DB 不可达（SKIP）；2=用法错。
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys

MAX_SELECTOR_CHARS = 64
PHYSICAL_RE = re.compile(r"^[a-z][a-z0-9_]*$")
FAMILY_RE = re.compile(
    r"^(?P<prefix>[a-z][a-z0-9_]*[a-z0-9])_\*(?::(?P<agg>[A-Za-z][A-Za-z0-9_]*))?$"
)
AGGREGATES = {"min", "max", "avg", "sum", "any", "all"}


def detect_special(s):
    """返回 (is_special, syntax_ok, subkind)。只判形态族，不判业务可解析性。"""
    if s.startswith("virtual:"):
        return True, bool(PHYSICAL_RE.match(s[len("virtual:"):])), "virtual"
    if "@" in s:
        name = s[1:] if s.startswith("@") else ""
        return True, bool(PHYSICAL_RE.match(name)), "semantic"
    if "_*" in s:
        m = FAMILY_RE.match(s)
        if not m:
            return True, False, "family"
        agg = m.group("agg")
        return True, (agg is None or agg in AGGREGATES), "family"
    return False, False, ""


def classify_value(raw):
    """把一条存量字符串分类为 literal/special/legacy（顺序见方案 §4.4）。"""
    if raw is None:
        return {"class": "skip", "reason": "null"}
    s = raw.strip()
    n = len(s)
    if n == 0:
        return {"class": "legacy", "reason": "empty"}
    is_special, syntax_ok, subkind = detect_special(s)
    if is_special:
        if n > MAX_SELECTOR_CHARS:
            return {"class": "special", "reason": subkind + "_too_long", "syntax_ok": False}
        if not syntax_ok:
            return {"class": "special", "reason": subkind + "_malformed", "syntax_ok": False}
        return {"class": "special", "reason": subkind, "syntax_ok": True}
    if n > MAX_SELECTOR_CHARS:
        return {"class": "legacy", "reason": "too_long"}
    if not PHYSICAL_RE.match(s):
        return {"class": "legacy", "reason": "charset_or_shape"}
    return {"class": "literal", "reason": ""}


def extract_condition_names(raw_json):
    """从 conditions_json 提取 [].sensor_name；返回 (names, status)。"""
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
    for elem in payload:
        if not isinstance(elem, dict):
            continue
        v = elem.get("sensor_name")
        if isinstance(v, str) and v.strip():
            names.append(v)
    return names, "ok"


def psql_rows(container, user, db, sql):
    """执行只读查询，返回文本行列表。PGOPTIONS 由数据库强制只读。"""
    env = dict(os.environ)
    env["PGOPTIONS"] = "-c default_transaction_read_only=on"
    if container:
        cmd = ["docker", "exec", "-e", "PGOPTIONS=-c default_transaction_read_only=on",
               container, "psql", "-X", "-U", user, "-d", db,
               "-At", "-F", "\x1f", "-v", "ON_ERROR_STOP=1", "-c", sql]
    else:
        cmd = ["psql", "-X", "-U", user, "-d", db,
               "-At", "-F", "\x1f", "-v", "ON_ERROR_STOP=1", "-c", sql]
    p = subprocess.run(cmd, env=env, capture_output=True, text=True, timeout=60)
    if p.returncode != 0:
        raise RuntimeError((p.stderr or p.stdout).strip())
    return [ln for ln in p.stdout.splitlines() if ln != ""]


def table_column_exists(container, user, db, table, column):
    if not container:
        return True  # 本地模式交给 SQL 自身报错
    sql = ("SELECT 1 FROM information_schema.columns WHERE table_schema='public' "
           "AND table_name='%s' AND column_name='%s' LIMIT 1" % (table, column))
    try:
        return len(psql_rows(container, user, db, sql)) > 0
    except RuntimeError:
        return False


def scan(container, user, db):
    sources = []
    items = []

    def add_source(name, values):
        classified = [(v, classify_value(v)) for v in values]
        counts = {"literal": 0, "special": 0, "legacy": 0}
        rows = []
        for value, cls in classified:
            if cls["class"] in counts:
                counts[cls["class"]] += 1
            rows.append({"source": name, "value": value, "class": cls["class"],
                         "reason": cls["reason"]})
        sources.append({"name": name, "values": len(values), "counts": counts})
        items.extend(rows)

    # 1) alert_rules.sensor_name
    try:
        rows = psql_rows(container, user, db,
                         "SELECT id, sensor_name FROM alert_rules WHERE sensor_name IS NOT NULL")
        vals = [r.split("\x1f", 1)[1] for r in rows]
        add_source("alert_rules.sensor_name", vals)
    except RuntimeError as e:
        sources.append({"name": "alert_rules.sensor_name", "error": str(e)})

    # 2) automation_rules.trigger_sensor_name
    try:
        rows = psql_rows(container, user, db,
                         "SELECT id, trigger_sensor_name FROM automation_rules "
                         "WHERE trigger_sensor_name IS NOT NULL")
        vals = [r.split("\x1f", 1)[1] for r in rows]
        add_source("automation_rules.trigger_sensor_name", vals)
    except RuntimeError as e:
        sources.append({"name": "automation_rules.trigger_sensor_name", "error": str(e)})

    # 3) automation_rules.conditions_json[].sensor_name
    try:
        rows = psql_rows(container, user, db,
                         "SELECT id, conditions_json FROM automation_rules "
                         "WHERE conditions_json IS NOT NULL AND btrim(conditions_json) <> ''")
        vals, invalid = [], 0
        for r in rows:
            raw = r.split("\x1f", 1)[1]
            names, status = extract_condition_names(raw)
            if status == "invalid_json":
                invalid += 1
                items.append({"source": "automation_rules.conditions_json",
                              "value": raw[:120], "class": "invalid_json", "reason": status})
                continue
            vals.extend(names)
        add_source("automation_rules.conditions_json[].sensor_name", vals)
        sources[-1]["invalid_json"] = invalid
    except RuntimeError as e:
        sources.append({"name": "automation_rules.conditions_json[].sensor_name",
                        "error": str(e)})

    # 4) data_sources.category
    try:
        rows = psql_rows(container, user, db,
                         "SELECT id, category FROM data_sources WHERE category IS NOT NULL")
        vals = [r.split("\x1f", 1)[1] for r in rows]
        add_source("data_sources.category", vals)
    except RuntimeError as e:
        sources.append({"name": "data_sources.category", "error": str(e)})

    return sources, items


def summarize(sources, items):
    total = len(items)
    special = sum(1 for i in items if i["class"] == "special")
    legacy = sum(1 for i in items if i["class"] == "legacy")
    invalid = sum(1 for i in items if i["class"] == "invalid_json")
    errs = [s for s in sources if "error" in s]
    return {"total": total, "literal": total - special - legacy - invalid,
            "special": special, "legacy": legacy, "invalid_json": invalid,
            "sources": sources, "source_errors": len(errs),
            "items": items, "non_literal": special + legacy + invalid}


def selftest():
    cases = [
        ("rsoc", "literal"), (" temperature_* ", "special"), ("@soc", "special"),
        ("virtual:cell_delta", "special"), ("temperature_*:max", "special"),
        ("Temperature_*", "special"), ("@", "special"), ("virtual:", "special"),
        ("RSOC", "legacy"), ("温度", "legacy"), ("a" * 65, "legacy"), ("", "legacy"),
    ]
    bad = 0
    for value, want in cases:
        got = classify_value(value)["class"]
        if got != want:
            print("SELFTEST FAIL: %r => %s（期望 %s）" % (value, got, want))
            bad += 1
    names, status = extract_condition_names('[{"sensor_name":"@soc"}]')
    if status != "ok" or names != ["@soc"]:
        print("SELFTEST FAIL: conditions_json 正常提取失败:", status, names)
        bad += 1
    if extract_condition_names("{bad json")[1] != "invalid_json":
        print("SELFTEST FAIL: invalid_json 未单列")
        bad += 1
    print("SELFTEST: %d 项失败" % bad)
    return 0 if bad == 0 else 1


def main():
    ap = argparse.ArgumentParser(description="回滚盘点：非字面量 selector 取值（只读）")
    ap.add_argument("--check", action="store_true", help="扫描并给出回退判定（默认）")
    ap.add_argument("--json", action="store_true", help="机器可读输出")
    ap.add_argument("--selftest", action="store_true", help="分类器自检（不连库）")
    ap.add_argument("--container", default=os.environ.get("EHOME_PG_CONTAINER", "ehome-postgres"))
    ap.add_argument("--user", default=os.environ.get("EHOME_DB_USER", "ehome"))
    ap.add_argument("--db", default=os.environ.get("EHOME_DB_NAME", "ehome"))
    ap.add_argument("--no-docker", action="store_true", help="不用 docker exec，改用本机 psql")
    args = ap.parse_args()

    if args.selftest:
        sys.exit(selftest())
    if not args.check and not args.json:
        pass  # 默认扫描

    container = ""
    if not args.no_docker and shutil.which("docker") and args.container:
        probe = subprocess.run(["docker", "inspect", "-f", "{{.State.Running}}", args.container],
                               capture_output=True, text=True)
        if probe.returncode == 0 and probe.stdout.strip() == "true":
            container = args.container
    if not container and not shutil.which("psql"):
        print("SKIP: DB 不可达（无运行中的容器 %s，也没有本机 psql）" % args.container)
        print("RESULT: SKIP(db-unavailable)")
        sys.exit(77)

    try:
        sources, items = scan(container, args.user, args.db)
    except Exception as e:  # noqa: BLE001 - 任何连接/查询失败都按 DB 不可达处理
        print("SKIP: DB 不可达：%s" % e)
        print("RESULT: SKIP(db-unavailable)")
        sys.exit(77)

    summary = summarize(sources, items)
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, indent=2))
    else:
        for s in summary["sources"]:
            if "error" in s:
                print("SOURCE %s ERROR %s" % (s["name"], s["error"]))
            else:
                print("SOURCE %s values=%d literal=%d special=%d legacy=%d" %
                      (s["name"], s["values"], s["counts"]["literal"],
                       s["counts"]["special"], s["counts"]["legacy"]))
        for it in summary["items"]:
            if it["class"] != "literal":
                print("NON_LITERAL source=%s class=%s reason=%s value=%r" %
                      (it["source"], it["class"], it["reason"], it["value"]))
        print("SUMMARY literal=%d special=%d legacy=%d invalid_json=%d" %
              (summary["literal"], summary["special"], summary["legacy"], summary["invalid_json"]))
        if summary["non_literal"] == 0:
            print("RESULT: NON_LITERAL=0（无非字面量；代码回退不被数据阻塞）")
        else:
            print("RESULT: NON_LITERAL=%d（禁止代码回退；先人工迁移以下取值）" % summary["non_literal"])
    sys.exit(0 if summary["non_literal"] == 0 else 1)


if __name__ == "__main__":
    main()
