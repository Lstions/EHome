#!/usr/bin/env python3
"""EHomeSystem 后端压测资源采样器.
每 2s 采样一次: 后端 CPU/RSS/线程数 + PG 写入吞吐 + 表行数 + 后端日志速率.
用法: python3 sampler.py <out_csv> <duration_s>
"""
import csv
import os
import subprocess
import sys
import time

BACKEND_LOG = "/home/sun/workspace/EHomeSystem/.logs/backend.log"


def sh(cmd: str, timeout=5) -> str:
    try:
        r = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=timeout)
        return (r.stdout or "").strip()
    except Exception:
        return ""


def backend_pid() -> str:
    return sh("lsof -ti :8082 2>/dev/null | head -1")


def sample_proc(pid: str):
    """返回 (cpu_pct, rss_mb, threads). cpu 用 /proc/<pid>/stat utime+stime 差分由调用方算."""
    if not pid:
        return None
    try:
        with open(f"/proc/{pid}/stat") as f:
            parts = f.read().split()
        # utime(14) stime(15) — fields after comm with parens; 用 rfind(')')
        idx = f.closed or 0
        return None
    except Exception:
        return None


class ProcSampler:
    def __init__(self, pid: str):
        self.pid = pid
        self.last_ticks = None
        self.last_t = None

    def read_ticks(self):
        try:
            with open(f"/proc/{self.pid}/stat") as f:
                data = f.read()
            rp = data.rfind(")")
            rest = data[rp + 2:].split()
            # rest[11]=utime rest[12]=stime (从 comm 后第一个字段算起: state=0, ppid=1, ... utime=11, stime=12)
            utime, stime = int(rest[11]), int(rest[12])
            return utime + stime
        except Exception:
            return None

    def rss_mb(self):
        try:
            with open(f"/proc/{self.pid}/status") as f:
                for line in f:
                    if line.startswith("VmRSS"):
                        return int(line.split()[1]) / 1024.0
        except Exception:
            pass
        return 0.0

    def threads(self):
        try:
            with open(f"/proc/{self.pid}/status") as f:
                for line in f:
                    if line.startswith("Threads"):
                        return int(line.split()[1])
        except Exception:
            pass
        return 0

    def cpu_pct(self):
        """返回单核百分比 (100 = 1 核满载).
        /proc/<pid>/stat utime+stime 单位 = CLK_TCK (通常 100Hz), delta_ticks/dt 即 CPU 秒/墙钟秒.
        """
        now = time.time()
        t = self.read_ticks()
        if t is None:
            return -1.0
        if self.last_ticks is None:
            self.last_ticks, self.last_t = t, now
            return 0.0
        dt = now - self.last_t
        clk = float(os.sysconf("SC_CLK_TCK")) if hasattr(os, "sysconf") else 100.0
        pct = (t - self.last_ticks) / dt / clk * 100.0
        self.last_ticks, self.last_t = t, now
        return pct


PG_COUNTERS = {
    "tup_inserted": "sum(tup_inserted)::bigint",
    "tup_updated": "sum(tup_updated)::bigint",
    "xact_commit": "sum(xact_commit)::bigint",
    "blks_read": "sum(blks_read)::bigint",
    "blks_hit": "sum(blks_hit)::bigint",
}


def pg_stats():
    cols = ", ".join(f"{expr} AS {name}" for name, expr in PG_COUNTERS.items())
    out = sh(f"docker exec ehome-postgres psql -U ehome -d ehome -t -A -c \"SELECT {cols} FROM pg_stat_database;\"")
    if not out:
        return None
    v = out.split("|")
    return {name: int(v[i]) for i, name in enumerate(PG_COUNTERS)}


def pg_rows():
    out = sh("docker exec ehome-postgres psql -U ehome -d ehome -t -A -c \""
             "SELECT relname, n_live_tup FROM pg_stat_user_tables WHERE relname IN ('unified_data','device_data');\"")
    rows = {}
    if out:
        for line in out.splitlines():
            parts = line.split("|")
            if len(parts) == 2:
                rows[parts[0]] = int(parts[1])
    return rows


def emqx_msgs():
    out = sh("docker exec ehome-emqx emqx ctl broker stats 2>/dev/null | grep -E 'messages.received|messages.sent' | awk '{print $2}' | tr '\\n' ','")
    return out


def log_lines():
    try:
        with open(BACKEND_LOG, "rb") as f:
            return sum(1 for _ in f)
    except Exception:
        return 0


def main():
    out_csv = sys.argv[1] if len(sys.argv) > 1 else "/tmp/ehload/sample.csv"
    duration = int(sys.argv[2]) if len(sys.argv) > 2 else 300
    pid = backend_pid()
    print(f"backend pid={pid} 采样 {duration}s -> {out_csv}", file=sys.stderr)
    ps = ProcSampler(pid)
    ps.cpu_pct()  # 初始化基线

    last_pg = pg_stats()
    last_rows = pg_rows()
    last_log = log_lines()
    last_emqx = emqx_msgs()
    last_t = time.time()

    with open(out_csv, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["t", "cpu_pct_total", "rss_mb", "threads",
                    "pg_ins/s", "pg_xact/s", "pg_blks_read/s", "pg_blks_hit/s",
                    "unified_rows", "device_rows", "log_lines/s", "emqx_recv/s"])
        while time.time() - last_t < duration:
            time.sleep(2)
            now = time.time()
            cpu = ps.cpu_pct()
            rss = ps.rss_mb()
            thr = ps.threads()
            pg = pg_stats()
            rows = pg_rows()
            lg = log_lines()
            emqx = emqx_msgs()
            dt = now - last_t
            if pg and last_pg:
                ins = (pg["tup_inserted"] - last_pg["tup_inserted"]) / dt
                xact = (pg["xact_commit"] - last_pg["xact_commit"]) / dt
                rd = (pg["blks_read"] - last_pg["blks_read"]) / dt
                hit = (pg["blks_hit"] - last_pg["blks_hit"]) / dt
            else:
                ins = xact = rd = hit = -1
            w.writerow([f"{now:.1f}", f"{cpu:.1f}", f"{rss:.1f}", thr,
                        f"{ins:.1f}", f"{xact:.1f}", f"{rd:.1f}", f"{hit:.1f}",
                        rows.get("unified_data", -1), rows.get("device_data", -1),
                        f"{(lg-last_log)/dt:.1f}", emqx])
            f.flush()
            last_pg, last_rows, last_log, last_emqx, last_t = pg, rows, lg, emqx, now
            print(f"[{now:.0f}] cpu={cpu:.0f}% rss={rss:.0f}MB thr={thr} ins/s={ins:.0f} "
                  f"unified={rows.get('unified_data')} device={rows.get('device_data')} log/s={(lg-last_log)/dt:.0f}", flush=True)


if __name__ == "__main__":
    main()
