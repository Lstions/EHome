#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""nvs_certs_gen.py —— 把 CA/设备证书/设备私钥铺进设备 NVS 分区（task-26）

## 这个工具解决什么

固件会**读**证书：main/device_link_wiring.c:349-354 用 nvs_open("eh_tls") 读三个 blob
ca / cert / key；读不到时**不假装成功**，而是让 tls_guard 判 hard_fatal ⇒ SESSION_FATAL
（不重试）。这是**有意**的（不把"没铺开"伪装成"网络抖动"）。

但仓库里**没有任何代码会写它们**（grep -rn nvs_set_blob main/ components/ | grep -iv crash == 空）。
⇒ 真机上这条链路永远停在 SESSION_FATAL。
⇒ 本工具补的正是这一块：**产出一份可直接烧写的 NVS 镜像**。

## 为什么用 IDF 官方工具而不是自己写

  · 生成：components/nvs_flash/nvs_partition_generator/nvs_partition_gen.py（官方）
  · 读回：components/nvs_flash/nvs_partition_tool/nvs_tool.py（官方解析器）
自己实现 NVS 二进制格式 = 重复实现官方格式，且**必然**随 IDF 版本漂移。

## ⭐ 生成后**读回校验**（本工具的核心，不是可选步骤）

生成完立刻用官方 nvs_tool.py 把镜像**解析回来**，逐字节比对 ca/cert/key。
没验证的生成器等于没有 —— 它可能产出一个"看起来成功了但设备读不到"的镜像。

## ⚠⚠ 安全现状（如实，不要读成"安全"）

本项目**当前没有** NVS 加密：
  · 所有分区表里都**没有** nvs_keys 分区（partitions*.csv 只有 nvs/otadata/phy_init/ota_*）；
  · sdkconfig.defaults 里**没有** CONFIG_NVS_ENCRYPTION；
  · ⇒ 本工具默认产出的镜像里，**私钥是明文**，可由任何能读 flash 的人取出
    （物理接触、或一次 esptool read_flash / 调试口）。
NVS 加密**能**做（本工具 --encrypted 会调用官方 encrypt 子命令），但它**要求设备侧**
同时具备 nvs_keys 分区 + NVS 加密配置 + 已烧入的密钥分区 —— 缺一样设备就**读不了**。
详见 tools/README-nvs-certs.md。

## 用法

  # 生成 + 自动读回校验（默认，输出到 /tmp，绝不进工作树）
  python3 nvs_certs_gen.py generate --ca ca.crt --cert dev.crt --key dev.key \
          --out /tmp/eh_tls_nvs.bin

  # 自检：现场生成一次性材料（openssl）→ 生成 → 校验 → 负向对照
  python3 nvs_certs_gen.py selftest
"""

import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile

DEFAULT_NS = "eh_tls"
# 设备 NVS 分区：partitions*.csv 里全是 `nvs, data, nvs, 0x9000, 0x4000`。
DEFAULT_SIZE = 0x4000
DEFAULT_OFFSET = 0x9000


def die(msg, code=2):
    print("ERROR: " + msg, file=sys.stderr)
    return code


def find_idf():
    """定位 IDF 路径。优先 IDF_PATH，其次 IDF_TOOLS_PATH 的常见布局。"""
    cands = []
    if os.environ.get("IDF_PATH"):
        cands.append(os.environ["IDF_PATH"])
    for base in ("/home/sun/env/esp-idf", os.path.expanduser("~/esp/esp-idf")):
        cands.append(base)
    for c in cands:
        if os.path.isfile(os.path.join(c, "components/nvs_flash/nvs_partition_generator/nvs_partition_gen.py")):
            return c
    return None


def find_idf_python():
    """IDF 的 python 环境（nvs_partition_gen 需要 esp_idf_nvs_partition_gen 模块）。"""
    env = os.environ.get("IDF_PYTHON_ENV_PATH")
    if env:
        cand = os.path.join(env, "bin", "python3")
        if os.path.isfile(cand):
            return cand
    # 退回常见位置
    home = os.path.expanduser("~/.espressif/python_env")
    if os.path.isdir(home):
        for name in sorted(os.listdir(home), reverse=True):
            cand = os.path.join(home, name, "bin", "python3")
            if os.path.isfile(cand):
                return cand
    return sys.executable


def run(cmd, **kw):
    print("  $ " + " ".join(cmd))
    return subprocess.run(cmd, **kw)


def write_csv(path, ns, items):
    """items: [(key, filepath)]，全部按 binary blob 写。"""
    with open(path, "w", encoding="utf-8") as f:
        f.write("key,type,encoding,value\n")
        f.write(ns + ",namespace,,\n")
        for key, fp in items:
            # ⚠ 用绝对路径：nvs_partition_gen 解析 CSV 里的相对路径时以**它自己的 cwd**
            #   为准，调用方换个 cwd 就会静默找不到文件（实测踩过）。
            f.write("%s,file,binary,%s\n" % (key, os.path.abspath(fp)))


def gen_image(py, gen, csv, out, size, encrypted, keyfile, capture=False):
    cmd = [py, gen]
    if encrypted:
        cmd += ["encrypt"]
        if keyfile:
            cmd += ["--keyfile", keyfile]
        else:
            cmd += ["--keygen"]
    else:
        cmd += ["generate"]
    cmd += [csv, out, hex(size) if isinstance(size, int) else str(size)]
    if not capture:
        return run(cmd).returncode
    r = subprocess.run(cmd, capture_output=True, text=True)
    print("  $ " + " ".join(cmd))
    return r.returncode, (r.stdout or "") + (r.stderr or "")


# ── 读回解析 ───────────────────────────────────────────────────────────────
#
# nvs_tool.py -d blobs 每行形状（来源：nvs_parser.py:233-243 dump_raw()）：
#
#     hex_bytes = 每字节 "%02x " ，并在正中间**多插一个空格**；
#     整行 = "  " + 偏移 + "  " + hex_bytes + " " + decoded
#   ⇒ 于是 hex 与 ASCII 之间、以及 hex 的两组之间都恰好是**两个空格**。
#
# ⚠⚠ 为什么必须按"2+ 空格"切分，而不是"把所有 2 位十六进制 token 都当成字节"：
#   decoded（ASCII 列）里**可能出现**形如 "ab" / "be" / "de" 的两字符词，
#   它们**每个字符都是十六进制** ⇒ 会被误当成数据字节插进 blob 中间。
#   实测后果（本工具第一版真的踩了）：读回的字节数**看似正确**（因为截到 Size），
#   但内容整体错位 1 字节 ⇒ 校验**对合法镜像报红**。
#   这正是"校验器自己也会坏"的实例 —— 所以下面还加了长度硬校验。
HDR_RE = re.compile(r"^([A-Za-z0-9_.]+):([A-Za-z0-9_.]+) - Type: (\w+).*Size: (\d+)")
OFFSET_RE = re.compile(r"^0x[0-9a-fA-F]+$")
BYTE_RE = re.compile(r"^[0-9a-fA-F]{2}$")


def _hex_field_to_bytes(field):
    """把一个"纯十六进制列"转成字节；只要有一个 token 不是 2 位十六进制就返回 None。
    返回 None 表示这不是 hex 列（很可能是 ASCII 列）⇒ 调用方必须忽略它。"""
    toks = field.split()
    if not toks:
        return None
    out = bytearray()
    for t in toks:
        if not BYTE_RE.match(t):
            return None
        out.append(int(t, 16))
    return bytes(out)


def parse_blobs(text):
    """解析成 {key: (bytes, declared_size)}。declared_size 来自 header 行。"""
    out = {}
    cur = None
    buf = bytearray()
    want = 0
    for line in text.splitlines():
        m = HDR_RE.match(line)
        if m:
            if cur is not None:
                out[cur] = (bytes(buf[:want]), want)
            cur = m.group(2)
            want = int(m.group(4))
            buf = bytearray()
            continue
        if cur is None:
            continue
        parts = re.split(r"\s{2,}", line.strip())
        if parts and OFFSET_RE.match(parts[0]):
            parts = parts[1:]          # 去掉行首偏移
        else:
            continue                   # 不是数据行（标题/空行等）
        for field in parts:
            bs = _hex_field_to_bytes(field)
            if bs is not None:
                buf += bs
            # 非 hex 列（ASCII 渲染）⇒ 直接跳过，绝不把它当数据
    if cur is not None:
        out[cur] = (bytes(buf[:want]), want)
    return out


def read_back(py, tool_dir, image):
    """用官方 nvs_tool.py 把镜像解析回来。返回 (rc, stdout+stderr)。"""
    r = subprocess.run([py, os.path.join(tool_dir, "nvs_tool.py"), image, "-d", "blobs",
                        "--color", "never"], capture_output=True, text=True)
    return r.returncode, (r.stdout or "") + (r.stderr or "")


def verify(py, tool_dir, image, ns, items, quiet=False):
    """⭐ 核心：读回镜像，逐字节比对三个 blob。返回 (ok, lines)。"""
    lines = []
    rc, text = read_back(py, tool_dir, image)
    if rc != 0:
        return False, ["nvs_tool.py rc=%d（解析器本身失败）" % rc]
    got = parse_blobs(text)
    ok = True
    for key, fp in items:
        want = open(fp, "rb").read()
        if key not in got:
            lines.append("  缺失: %s:%s" % (ns, key))
            ok = False
            continue
        blob, declared = got[key]
        # ⭐ 长度硬校验（两道）：
        #   · declared 必须等于原文件长度（header 行自称的大小）；
        #   · blob 的实际长度也必须等于它。
        # 为什么两道都要：本工具第一版的解析器会把 ASCII 列里"恰好是 2 位
        # 十六进制"的词当数据字节插进来 —— 那时 `blob` 会比 declared **长**，
        # 而下面还要比内容。少任何一道，这类解析器缺陷都可能被掩盖。
        if declared != len(want) or len(blob) != declared:
            lines.append("  长度不符: %s:%s  镜像自称 %d B / 实际解析 %d B / 原文件 %d B"
                         % (ns, key, declared, len(blob), len(want)))
            ok = False
            continue
        if blob != want:
            # 找出第一个不同的字节位置 —— 比"不一致"有用得多。
            bad = next(i for i in range(len(want)) if blob[i] != want[i])
            lines.append("  不一致: %s:%s  首个差异在第 %d 字节 (读回 0x%02X / 原 0x%02X)"
                         % (ns, key, bad, blob[bad], want[bad]))
            ok = False
            continue
        lines.append("  OK: %s:%s  %d B 逐字节一致" % (ns, key, len(want)))
    # 下界断言：必须**恰好**解析出这些 key —— 多出来的说明镜像里有别的东西。
    extra = set(got) - set(k for k, _ in items)
    if extra:
        lines.append("  意外多出的 key: %s" % sorted(extra))
        ok = False
    n_total = sum(os.path.getsize(fp) for _, fp in items)
    lines.append("  合计 %d B 材料（%d 个 blob）全部读回一致" % (n_total, len(items)) if ok
                 else "  校验失败")
    return ok, lines


def banner(t):
    print("");
    print("=== " + t + " ===");

    print("")


def cmd_generate(args):
    idf = find_idf()
    if idf is None:
        return die("找不到 ESP-IDF（设 IDF_PATH 或确认 /home/sun/env/esp-idf 存在）")
    py = find_idf_python()
    gen = os.path.join(idf, "components/nvs_flash/nvs_partition_generator/nvs_partition_gen.py")
    tool_dir = os.path.join(idf, "components/nvs_flash/nvs_partition_tool")

    for label, fp in (("ca", args.ca), ("cert", args.cert), ("key", args.key)):
        if not os.path.isfile(fp):
            return die("找不到 %s 文件: %s" % (label, fp))
        if os.path.getsize(fp) == 0:
            return die("%s 文件是空的: %s" % (label, fp))

    # 固件侧上限（main/Kconfig.projbuild:181 CONFIG_EHOME_DEVICE_LINK_CERT_BYTES，默认 4096）。
    # 这里**提前**拦住超限材料：镜像能生成但设备会判 INVALID_SIZE 拒收，
    # 那种失败在设备上只表现为 SESSION_FATAL，很难查。
    cap = args.cert_bytes
    for label, fp in (("ca", args.ca), ("cert", args.cert), ("key", args.key)):
        n = os.path.getsize(fp)
        if n > cap:
            return die("%s 有 %d B，超过固件上限 %d B（CONFIG_EHOME_DEVICE_LINK_CERT_BYTES）"
                       % (label, n, cap))

    out = args.out
    if os.path.abspath(out).startswith(os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))):
        print("WARNING: 输出路径在**工作树内** —— 镜像含私钥，必须 gitignore（见 .gitignore）");

    tmp = tempfile.mkdtemp(prefix="nvs_certs_")
    try:
        items = [("ca", args.ca), ("cert", args.cert), ("key", args.key)]
        csv = os.path.join(tmp, "nvs_certs.csv")
        write_csv(csv, args.namespace, items)

        banner("1) 生成镜像（IDF 官方 nvs_partition_gen.py）")
        print("  命名空间: %s   分区大小: %d B (0x%X)" % (args.namespace, args.size, args.size))
        print("  python:   %s" % py)
        rc, gen_out = gen_image(py, gen, csv, out, args.size, args.encrypted,
                                args.keyfile, capture=True)
        if rc != 0:
            # ⭐ 把"逐项上限"与"合计容量"区分开 —— 这是实测踩到的一个陷阱：
            #    CONFIG_EHOME_DEVICE_LINK_CERT_BYTES(4096) 是**逐项**上限，
            #    不代表三项同时取满还装得下 0x4000 分区（实测 3x3900 可以、3x4000 不行）。
            #    生成器报的是 InsufficientSizeError，那句话没有点出这个区别。
            if "InsufficientSizeError" in gen_out or "Insufficient" in gen_out:
                total = sum(os.path.getsize(fp) for _, fp in items)
                print(gen_out.rstrip());
                print("");
                print("  ⚠ 材料合计 %d B 装不进 %d B(0x%X) 的 NVS 分区。"
                      % (total, args.size, args.size));
                print("     注意 CONFIG_EHOME_DEVICE_LINK_CERT_BYTES(%d) 是逐项上限，"
                      % args.cert_bytes);
                print("     不是三项之和的上限 —— 三项同时取满会超容量。");
                print("     实测：三个各 3900 B 可以，各 4000 B 不行（本机 IDF 6.1）。");
                print("     办法：换更短的证书链（去掉中间 CA）、或放大 nvs 分区");
                print("     （改 partitions*.csv 的 nvs 行，注意同步 --size）。");
                return die("材料合计超出 NVS 分区容量", 1);
            print(gen_out.rstrip());
            return die("nvs_partition_gen 失败 (rc=%d)" % rc)
        if not os.path.isfile(out):
            return die("生成器返回 0 但没产出文件: %s" % out)
        print("  产出: %s  (%d B)" % (out, os.path.getsize(out)))

        if args.encrypted:
            banner("2) ⚠ 已生成**加密**镜像 —— 读回校验跳过")
            print("  加密镜像的内容是密文，官方 nvs_tool.py 无法直接解析明文比对。");
            print("  这不代表没问题：它要求设备侧同时具备 nvs_keys 分区 + NVS 加密配置，");
            print("  缺一样设备就**读不了**（见 README-nvs-certs.md 的安全一节）。");
            print("  所以本工具默认**不**加密：默认产出可校验的明文镜像。");
            return 0

        banner("2) ⭐ 读回校验（官方 nvs_tool.py -d blobs，逐字节比对）")
        ok, lines = verify(py, tool_dir, out, args.namespace, items)
        for ln in lines:
            print(ln)
        if not ok:
            return die("读回校验失败 —— 镜像不可信，不要烧它", 1)

        banner("结果")
        print("  PASS：镜像 %s 内含 %s:{ca,cert,key}，逐字节与原文件一致"
              % (out, args.namespace))
        print("  烧写命令（本工具**不会**替你烧）:");
        print("    esptool.py --chip <chip> -p <PORT> write_flash 0x%X %s"
              % (args.offset, out))
        return 0
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def _openssl_free() -> list:
    """自检用：现场生成一次性 CA + 设备证书 + 私钥（全部落在临时目录）。"""
    return [
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
         "-keyout", "ca.key", "-out", "ca.crt", "-days", "1", "-subj", "/CN=nvs-selftest-ca"],
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
         "-keyout", "dev.key", "-out", "dev.crt", "-days", "1", "-subj", "/CN=nvs-selftest-dev"],
    ]


def cmd_selftest(args):
    idf = find_idf()
    if idf is None:
        return die("找不到 ESP-IDF")
    py = find_idf_python()
    gen = os.path.join(idf, "components/nvs_flash/nvs_partition_generator/nvs_partition_gen.py")
    tool_dir = os.path.join(idf, "components/nvs_flash/nvs_partition_tool")

    failures = 0
    tmp = tempfile.mkdtemp(prefix="nvs_certs_selftest_")
    try:
        os.chdir(tmp)   # 材料只落在 temp 目录，绝不进工作树
        banner("0) 现场生成一次性测试材料（openssl，落在 %s）" % tmp)
        if shutil.which("openssl") is None:
            return die("没有 openssl —— 无法现场生成测试材料")
        for c in _openssl_free():
            if subprocess.run(c, capture_output=True).returncode != 0:
                return die("openssl 失败: %s" % " ".join(c))
        print("  ca.crt %d B / dev.crt %d B / dev.key %d B"
              % tuple(os.path.getsize(f) for f in ("ca.crt", "dev.crt", "dev.key")))

        items = [("ca", "ca.crt"), ("cert", "dev.crt"), ("key", "dev.key")]
        img = os.path.join(tmp, "partition.bin")
        csv = os.path.join(tmp, "nvs.csv")
        write_csv(csv, DEFAULT_NS, items)

        banner("1) 正向：生成 + 读回校验 ⇒ 必须成功")
        if gen_image(py, gen, csv, img, DEFAULT_SIZE, False, None) != 0:
            return die("生成失败", 1)
        ok, lines = verify(py, tool_dir, img, DEFAULT_NS, items)
        for ln in lines:
            print(ln)
        if not ok:
            failures += 1
            print("  FAIL: 正向用例没通过")
        else:
            print("  PASS");

        # ⭐⭐ 为什么正向用例必须跑**多组不同尺寸**（不是一组）：
        #
        # 本工具第一版的解析器有个缺陷：它把 ASCII 列里"恰好是 2 位十六进制"的词
        # （如 be / de / ab）当成了数据字节插进 blob 中间。而**单组材料**恰好没触发它
        # ⇒ selftest 当时**通过**了，我把校验器当成可信的。
        # 换一组真实证书后立刻报红 —— 才暴露出来。
        # ⇒ 教训：负向对照只能证明"会报红"，证明不了"该绿时真的绿"。
        #   正向必须覆盖多个尺寸，尤其跨过 NVS 的单条目/多条目边界。
        banner("1b) 正向多组尺寸（含 NVS 条目边界附近）")
        size_sets = [(1, 1, 1), (32, 33, 64), (200, 1984, 1985),
                     (1000, 2000, 3000), (3900, 3900, 3900)]
        for si, sizes in enumerate(size_sets):
            paths = []
            for j, n in enumerate(sizes):
                pj = os.path.join(tmp, "syn_%d_%d" % (si, j))
                with open(pj, "wb") as f:
                    # 用**确定性**内容，且带可辨识前缀 —— 便于人工核对。
                    f.write(("BLOB%d-%d:" % (si, j)).encode() + bytes(
                        ((si * 37 + j * 11 + k) & 0xFF) for k in range(max(0, n - 12))))
                paths.append(pj)
            sitems = [("ca", paths[0]), ("cert", paths[1]), ("key", paths[2])]
            scsv = os.path.join(tmp, "nvs_s%d.csv" % si)
            simg = os.path.join(tmp, "part_s%d.bin" % si)
            write_csv(scsv, DEFAULT_NS, sitems)
            rc = gen_image(py, gen, scsv, simg, DEFAULT_SIZE, False, None)
            if rc != 0:
                failures += 1
                print("  FAIL: sizes=%s 生成失败" % (sizes,))
                continue
            ok_s, lines_s = verify(py, tool_dir, simg, DEFAULT_NS, sitems)
            if ok_s:
                print("  PASS: sizes=%s（合计 %d B）读回逐字节一致"
                      % (sizes, sum(sizes)))
            else:
                failures += 1
                print("  FAIL: sizes=%s" % (sizes,))
                for ln in lines_s:
                    print("    " + ln)

        banner("2) 负向对照 A：把期望值改成**另一个文件的字节** ⇒ 校验必须报红")
        # 用一个内容不同的文件冒充 ca：校验必须发现不一致。
        with open("other.bin", "wb") as f:
            f.write(b"\x00" * os.path.getsize("ca.crt"))
        ok2, _ = verify(py, tool_dir, img, DEFAULT_NS, [("ca", "other.bin"),
                                                     ("cert", "dev.crt"), ("key", "dev.key")])
        if ok2:
            failures += 1
            print("  FAIL: 校验器没认出**内容不符** —— 它没有分辨力，等于没有");
        else:
            print("  PASS: 校验器正确报红（有分辨力）");

        banner("3) 负向对照 B：key 名换掉 ⇒ 校验必须报**缺失**")
        csv2 = os.path.join(tmp, "nvs2.csv")
        write_csv(csv2, DEFAULT_NS, [("ca", "ca.crt"), ("cert", "dev.crt"), ("wrongkey", "dev.key")])
        img2 = os.path.join(tmp, "partition2.bin")
        if gen_image(py, gen, csv2, img2, DEFAULT_SIZE, False, None) != 0:
            failures += 1
            print("  FAIL: 第二张镜像生成失败")
        else:
            ok3, lines3 = verify(py, tool_dir, img2, DEFAULT_NS, items)
            if ok3:
                failures += 1
                print("  FAIL: 缺 key 也通过了 —— 校验器漏检")
            else:
                print("  PASS: 校验器正确报缺失");
                for ln in lines3:
                    print("    " + ln)

        banner("4) 大小边界：3x4096 B（固件上限）能否装进 0x4000 的分区？")
        big = os.path.join(tmp, "big.bin")
        for nm in ("b1", "b2", "b3"):
            with open(os.path.join(tmp, nm), "wb") as f:
                f.write(b"A" * 4096)
        csv3 = os.path.join(tmp, "nvs3.csv")
        write_csv(csv3, DEFAULT_NS, [("ca", os.path.join(tmp, "b1")),
                                     ("cert", os.path.join(tmp, "b2")),
                                     ("key", os.path.join(tmp, "b3"))])
        bigimg = os.path.join(tmp, "big.bin")
        rcb = subprocess.run([py, gen, "generate", csv3, bigimg, hex(DEFAULT_SIZE)],
                             capture_output=True, text=True)
        if rcb.returncode != 0:
            print("  3x4096 B **装不进** 0x4000 分区（生成器 rc=%d）" % rcb.returncode)
            print("  ⇒ 这是**真实约束**：请用更小的证书或放大 nvs 分区。");
            print("  固件上限 CONFIG_EHOME_DEVICE_LINK_CERT_BYTES 是**逐项**上限，");
            print("  不代表三项同时取满还装得下 —— 详见 README-nvs-certs.md。");
            # 这**不是**失败：如实报告约束即可。
        else:
            print("  3x4096 B 装得下");

        banner("自检结果")
        if failures:
            print("  %d 项失败" % failures);
            return 1
        print("  selftest PASS（正向 + 两条负向对照都符合预期）");
        return 0
    finally:
        os.chdir("/")
        shutil.rmtree(tmp, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser(description="把证书材料铺进设备 NVS 分区（生成 + 读回校验）")
    sub = ap.add_subparsers(dest="cmd", required=True)

    g = sub.add_parser("generate", help="生成 NVS 镜像并**读回校验**")
    g.add_argument("--ca", required=True, help="CA 证书（PEM）")
    g.add_argument("--cert", required=True, help="设备证书（PEM）")
    g.add_argument("--key", required=True, help="设备私钥（PEM）")
    g.add_argument("--namespace", default=DEFAULT_NS,
                   help="NVS 命名空间（默认 %s，须与固件 CONFIG_EHOME_DEVICE_LINK_NVS_NS 一致）" % DEFAULT_NS)
    g.add_argument("--out", default="/tmp/eh_tls_nvs.bin", help="输出镜像路径")
    g.add_argument("--size", type=lambda s: int(s, 0), default=DEFAULT_SIZE,
                   help="NVS 分区大小（默认 0x4000，与 partitions*.csv 一致）")
    g.add_argument("--offset", type=lambda s: int(s, 0), default=DEFAULT_OFFSET,
                   help="NVS 分区偏移（默认 0x9000，仅用于打印烧写命令）")
    g.add_argument("--cert-bytes", type=int, default=4096,
                   help="逐项上限，对应 CONFIG_EHOME_DEVICE_LINK_CERT_BYTES（默认 4096）")
    g.add_argument("--encrypted", action="store_true",
                   help="生成**加密**镜像（⚠ 要求设备侧具备 nvs_keys 分区+NVS 加密配置；默认**不**加密）")
    g.add_argument("--keyfile", default=None, help="加密密钥文件（配合 --encrypted）")
    g.set_defaults(func=cmd_generate)

    s = sub.add_parser("selftest", help="现场生成测试材料跑全流程 + 负向对照")
    s.set_defaults(func=cmd_selftest)

    args = ap.parse_args()
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
