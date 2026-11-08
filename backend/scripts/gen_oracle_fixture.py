#!/usr/bin/env python3
"""gen_oracle_fixture.py — 生成「帧表解码 == 生产 SQL」的冻结对拍基准（oracle fixture）。

============ 这个脚本做什么 ============

2026-11-08 决定 DROP unified_data。而在那之前，仓库里有两条**活的对拍**：

    TestFrameSourceEqualsUnifiedData        帧表解码 == queryHistoryPoints（SQL）
    TestRollupMatchesUnifiedForLongWindows  rollup 重建 == queryHistoryPoints（SQL）

它们把「SQL 路径」当独立基准。表一旦 DROP，这两条用例不是变红，而是**跑不了** ——
而「跑不了」在 CI 里通常表现为 skip，**skip 是绿的**。

    一个会静默变成「总是通过」的证明，比没有证明更危险。

所以把 SQL 路径的**输出**在这一刻冻结成 fixture。之后：代码全删，期望值以数据留下。

============ 只对 L1 生效前的数据有效 ============

    L1（值不变不入库）2026-11-07 生效。
    生效前：unified_data 每帧 29 行（全字段）    => 与帧表**结构上应当逐点相等**
    生效后：unified_data 只存变化字段            => **结构上不可能相等**，不是 bug

本 fixture 固定取 2026-11-05，即 L1 生效前。**不要用它去验 L1 之后的数据。**

============ 怎么重新生成 ============

脚本本身**不连数据库**（凭据不入库）。先在生产上导出三份 CSV（只读 SELECT），
放到一个目录，再跑本脚本：

    # ① 窗口行的帧（生产真实 payload）
    psql -c "COPY (
      SELECT to_char(ts AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'), seq, frame_kind, slot_id,
             encode(payload,'base64')
      FROM telemetry_frame
      WHERE device_id=14 AND ts >= '2026-11-05 00:00:00+00' AND ts < '2026-11-05 00:10:00+00'
      ORDER BY ts, seq) TO STDOUT WITH CSV" > frames.csv

    # ② avg 基准（D1 口径：桶内最后值，W = ceil(600/60) = 10s）
    psql -c "COPY (
      WITH src AS (SELECT sensor_name, timestamp, value FROM unified_data
                   WHERE device_id=14 AND timestamp BETWEEN '2026-11-05 00:00:00+00'
                                                     AND '2026-11-05 00:09:59.999999+00'),
           b AS (SELECT sensor_name,
                        to_timestamp(floor(extract(epoch FROM timestamp)/10)*10) AS ts,
                        (array_agg(value ORDER BY timestamp DESC))[1] AS val
                 FROM src GROUP BY 1,2)
      SELECT sensor_name, to_char(ts AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'), val::text
      FROM b WHERE ts >= '2026-11-05 00:00:00+00' ORDER BY 1,2) TO STDOUT WITH CSV" > oracle_avg.csv

    # ③ change 基准
    psql -c "COPY (
      WITH src AS (SELECT sensor_name, timestamp, value FROM unified_data
                   WHERE device_id=14 AND timestamp BETWEEN '2026-11-05 00:00:00+00'
                                                     AND '2026-11-05 00:09:59.999999+00'),
           s AS (SELECT sensor_name, timestamp, value,
                        lag(value) OVER (PARTITION BY sensor_name ORDER BY timestamp) AS prev FROM src)
      SELECT sensor_name,
             to_char(timestamp AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'), value::text
      FROM s WHERE s.prev IS DISTINCT FROM s.value ORDER BY 1,2) TO STDOUT WITH CSV" > oracle_change.csv

    # ④ 生产 rollup 行（独立基准：live collector 或 unified 回填写的，不是从帧算的）
    psql -c "COPY (
      SELECT device_id, to_char(bucket AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
             encode(payload,'base64')
      FROM telemetry_rollup_1m
      WHERE device_id=14 AND bucket >= '2026-11-05 00:00:00+00'
                        AND bucket <  '2026-11-05 00:10:00+00'
      ORDER BY bucket) TO STDOUT WITH CSV" > rollup_win.csv

    # ⑤ slot 定义（位流顺序 + 量化规格，缺它解不出帧）
    psql -Atc "SELECT fields_json FROM telemetry_slot WHERE slot_id=10" > slot10.json

    python3 backend/scripts/gen_oracle_fixture.py <csv目录> <输出.json>

脚本是**纯函数**：同样的输入永远产出同样的字节（键序、浮点格式都固定）。
"""
import csv
import json
import sys
from pathlib import Path


def read_csv(path):
    return [l for l in Path(path).read_text().strip().split("\n") if l]


def main():
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    src, dst = Path(sys.argv[1]), Path(sys.argv[2])

    frames = []
    for line in read_csv(src / "frames.csv"):
        ts, seq, kind, slot, payload = line.split(",")
        frames.append({
            "ts": ts, "seq": int(seq), "frame_kind": int(kind),
            "slot_id": int(slot), "payload_b64": payload,
        })

    avg = {}
    for line in read_csv(src / "oracle_avg.csv"):
        name, ts, val = line.split(",")
        avg.setdefault(name, []).append({"ts": ts, "value": float(val)})

    change = {}
    for line in read_csv(src / "oracle_change.csv"):
        name, ts, val = line.split(",")
        change.setdefault(name, []).append({"ts": ts, "value": float(val)})

    # rollup 行：可选（没有就不带，但用例会明确失败而不是跳过）。
    #
    # ⚠ 这里**必须用 csv 模块**而不是逐行 split：payload 是长 base64，
    # psql COPY 会把它折进带引号的多行 CSV 字段里。逐行 split 会把一条记录
    # 拆成多条（实测报 "not enough values to unpack (expected 3, got 1)"）。
    # frames.csv 没暴露这个问题，只是因为它的列都短、没触发折行。
    rollup = []
    rp = src / "rollup_win.csv"
    if rp.exists():
        with rp.open(newline="") as fh:
            for row in csv.reader(fh):
                if len(row) != 3:
                    continue
                _dev, bucket, payload = row
                rollup.append({"bucket": bucket, "payload_b64": payload})

    slot = json.loads((src / "slot10.json").read_text().strip())

    out = {
        "_note": [
            "冻结于 2026-11-08，来源为生产库 device_id=14 / 2026-11-05 00:00~00:10 UTC。",
            "用途：替代已删除的 queryHistoryPoints（生产 SQL 路径）作为独立对拍基准。",
            "",
            "为什么是数据而不是代码：活的对拍依赖 unified_data 存在，那张表 DROP 之后",
            "用例只能 skip —— 而 skip 在 CI 里是绿的。一个会静默变成「总是通过」的",
            "证明，比没有证明更危险。固化成数据之后，它永远跑得起来，也就永远有约束力。",
            "",
            "⚠ 只对 L1 生效前（2026-11-07 之前）的数据形态有效。",
            "   L1 之后 unified_data 只存变化字段，两条路结构上不可能相等 —— 那不是 bug。",
        ],
        "device_id": 14,
        "start": "2026-11-05T00:00:00Z",
        "end": "2026-11-05T00:10:00Z",
        "max_points": 60,
        "slot": slot,
        "frames": frames,
        "oracle_avg": avg,
        "oracle_change": change,
        # ============ rollup 的独立基准 ============
        #
        # 这些是生产 telemetry_rollup_1m 的真实 payload，由 live collector
        # （内存累加）或 unified_data 回填写入 —— 不是从帧表算出来的。
        #
        # 所以拿它当期望值去验「从帧表重建 rollup」，是真正的跨路径对拍：
        #   live collector  不读帧表
        #   回填            不读帧表
        #   帧表重建        不读前两者
        # 三条路径互不依赖。
        #
        # 为什么按日期筛而不按 telemetry_rollup_coverage.source 筛：
        # 那张表的 source 列大部分是空串（MarkRollupCoverage 不写它，
        # 只有 MarkRollupCoverageWithSource 写），实测 complete 的 55 行里
        # 只有 2 行有 source ⇒ source 列不可信。
        # 而 2026-11-05 早于 L1 生效（11-07），那天两条来源的口径相同。
        "oracle_rollup_1m": rollup,
    }

    dst.parent.mkdir(parents=True, exist_ok=True)
    dst.write_text(json.dumps(out, ensure_ascii=False, indent=1, sort_keys=True) + "\n")
    print(f"wrote {dst} ({dst.stat().st_size} bytes): "
          f"{len(frames)} frames, {len(avg)} avg categories, {len(change)} change categories, " +
          f"{len(rollup)} rollup minutes")
    return 0


if __name__ == "__main__":
    sys.exit(main())
