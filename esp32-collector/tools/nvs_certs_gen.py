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

## ⭐ 两道校验，缺一不可（2026-10-07 补齐第二道）

**第一道：生成后读回校验（搬运）**
生成完立刻用官方 nvs_tool.py 把镜像**解析回来**，逐字节比对 ca/cert/key。
没验证的生成器等于没有 —— 它可能产出一个"看起来成功了但设备读不到"的镜像。

**第二道：材料内容校验（能不能用）** ← 此前**完全缺失**
读回一致只证明"字节搬对了"，**完全不证明"这三份材料能建立 mTLS 会话"**。
缺这一道的后果，本工具自己的 selftest 就是活证据：它一直用
`openssl req -x509` 生成"设备证书"，那其实是**自签证书**（issuer == subject），
由它签发的镜像在真机上**永远握不上手**，而两道错都没有报 ——
因为当时只有第一道。现在 generate 会先跑 validate_material() 并**拒绝**：
  · 设备证书未由该 CA 签发（自签是典型）  · 已过期 / 尚未生效
  · 私钥与证书不配对                      · 私钥有口令（固件读裸 PEM）
  · 证书里没有 CN/SAN（后端取不到 node_id，能握手但会被拒绝注册）
`--force` 可**显式**跳过（默认不给过）。

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


def _ssl(*args, **kw):
    """跑一条 openssl 子命令，返回 (rc, stdout+stderr)。"""
    try:
        p = subprocess.run(["openssl"] + list(args), capture_output=True, text=True, **kw)
    except FileNotFoundError:
        return 127, "openssl not found"
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def _ssl_text(*args):
    """取 openssl 的单值输出（去首尾空白）；失败返回 None。"""
    rc, out = _ssl(*args)
    return None if rc != 0 else out.strip()


def validate_material(ca, cert, key, warn_days=30, quiet=False):
    """校验 ca/cert/key 三件套是不是真的能用。返回 (errors, warnings)。

    ## 为什么必须做（2026-10-07）
    此前的校验是「镜像能不能读回三份字节」—— 那只证明搬运没丢字节，
    **完全不证明这三份材料能建立 mTLS 会话**。
    最刺眼的证据：本工具自己的 selftest 用 openssl req -x509 生成「设备证书」，
    那其实是**自签证书**（issuer == subject），拿本工具生成的 CA 去验**必然失败**。
    也就是说：工具一直在用一份**永远握不上手的材料**证明自己没问题。

    ## 逐条判据 + 漏了会怎样
    | 判据 | 漏了会怎样 |
    |---|---|
    | 三份都能 PEM 解析 | 设备端 mbedTLS 解析失败 ⇒ SESSION_FATAL |
    | 设备证书由该 CA 签发 | 后端 RequireAndVerifyClientCert 拒绝 ⇒ 永远连不上 |
    | 设备证书当前在有效期内 | 现场才过期 ⇒ 突然连不上，且不像配置错误 |
    | 设备证书与私钥配对 | 握手签名失败 ⇒ 连不上，且报错离根因很远 |
    | 私钥未加密 | 固件读裸 PEM、没有口令 ⇒ 必然失败 |
    | 证书有 CN/SAN 可作身份 | 后端取不到 node_id ⇒ 能握手但被拒绝注册 |
    """
    errors, warnings = [], []

    for label, fp in (("CA", ca), ("设备证书", cert), ("设备私钥", key)):
        if not os.path.exists(fp):
            errors.append("%s 不存在: %s" % (label, fp))
        elif os.path.getsize(fp) == 0:
            errors.append("%s 是空文件: %s" % (label, fp))
    if errors:
        return errors, warnings

    if not shutil.which("openssl"):
        # 不静默跳过：本函数的意义就是别拿没验过的材料去烧。
        return (["找不到 openssl —— 无法校验证书内容（不静默跳过："
                 "宁可失败，也不要放行未验证的材料）"], warnings)

    # 1) 三份都能解析
    rc, out = _ssl("x509", "-in", ca, "-noout")
    if rc != 0:
        errors.append("CA 证书无法解析为 PEM/X.509: %s" % out.strip()[:160])
    rc, out = _ssl("x509", "-in", cert, "-noout")
    if rc != 0:
        errors.append("设备证书无法解析为 PEM/X.509: %s" % out.strip()[:160])
    rc, out = _ssl("pkey", "-in", key, "-noout")
    if rc != 0:
        low = out.lower()
        if "encrypted" in low or "pass phrase" in low or "bad decrypt" in low:
            errors.append("设备私钥有口令（加密的）：固件读的是裸 PEM、没有口令 ⇒ 必然握手失败。"
                          "请去掉口令：openssl pkey -in KEY -out KEY.plain")
        else:
            errors.append("设备私钥无法解析为 PEM: %s" % out.strip()[:160])
    if errors:
        return errors, warnings

    # 2) 设备证书由该 CA 签发（最容易被自签证书骗过的一条）
    rc, out = _ssl("verify", "-CAfile", ca, cert)
    if rc != 0:
        issues = [l.strip() for l in out.splitlines() if l.strip() and not l.startswith(cert)]
        hint = ""
        iss = _ssl_text("x509", "-in", cert, "-noout", "-issuer") or ""
        sub = _ssl_text("x509", "-in", cert, "-noout", "-subject") or ""
        if iss and sub and iss.split("=", 1)[-1] == sub.split("=", 1)[-1]:
            hint = ("（设备证书是自签的：issuer 与 subject 相同 ⇒ 它不是由给定 CA 签发的。"
                    "openssl req -x509 会产生这种证书，它永远通不过 mTLS 服务端校验）")
        errors.append("设备证书未通过该 CA 的验签：%s %s" % ("; ".join(issues[:3]), hint))

    # 3) 有效期
    import datetime
    now = datetime.datetime.now(datetime.timezone.utc)
    end = _ssl_text("x509", "-in", cert, "-noout", "-enddate")
    if not end:
        errors.append("取不到设备证书的 notAfter")
    else:
        try:
            raw_end = end.split("=", 1)[1].strip()
            end_dt = datetime.datetime.strptime(raw_end, "%b %d %H:%M:%S %Y %Z").replace(
                tzinfo=datetime.timezone.utc)
            if end_dt <= now:
                errors.append("设备证书已过期：notAfter=%s（现在 %s）⇒ 设备连不上，"
                              "且现场看起来像网络故障、而不是证书到期"
                              % (raw_end, now.strftime("%Y-%m-%d %H:%M:%S UTC")))
            elif (end_dt - now).days < warn_days:
                warnings.append("设备证书将于 %s 过期（不足 %d 天）—— 现场过期会突然断连"
                                % (raw_end, warn_days))
        except ValueError:
            warnings.append("无法解析 notAfter=%r（跳过有效期判定，不静默当作有效）" % end)

    begin = _ssl_text("x509", "-in", cert, "-noout", "-startdate")
    if begin:
        try:
            raw_beg = begin.split("=", 1)[1].strip()
            beg_dt = datetime.datetime.strptime(raw_beg, "%b %d %H:%M:%S %Y %Z").replace(
                tzinfo=datetime.timezone.utc)
            if beg_dt > now:
                errors.append("设备证书尚未生效：notBefore=%s" % raw_beg)
        except ValueError:
            pass

    # 4) 私钥与证书配对
    cpub = _ssl_text("x509", "-in", cert, "-noout", "-pubkey")
    kpub = _ssl_text("pkey", "-in", key, "-pubout")
    if cpub and kpub and cpub.strip() != kpub.strip():
        errors.append("设备私钥与设备证书不配对（公钥不同）⇒ 握手签名失败，且报错离根因很远")
    elif not cpub or not kpub:
        warnings.append("取不到公钥，跳过配对检查（不静默当作配对）")

    # 5) 身份：后端要从证书里取 node_id
    # backend/internal/transport/server.go:311 nodeIDFromCert()：
    # 只信任已验签证书里的身份，取 (CN, SAN DNS, SAN email) 第一个非空者，
    # 取不到就拒绝连接。⇒ 没有身份标识的证书能握手但会被拒绝注册。
    # ⚠ openssl 打印的是 "subject=CN=xxx" —— **带 "subject=" 前缀**。
    # 我第一版直接对整串按 "," 切再 startswith("CN=")，
    # 于是第一个元素是 "subject=CN=node-self"，startswith("CN=") 恒为 False
    # ⇒ 对**明明有 CN** 的证书报"没有 CN 也没有 SAN"（假阳性）。
    # 实测输出（第一版就是被它骗过）：
    #   $ openssl x509 -in dev.crt -noout -subject -nameopt RFC2253
    #   subject=CN=node-self
    # ⇒ 必须先去前缀再切分。
    subj = _ssl_text("x509", "-in", cert, "-noout", "-subject", "-nameopt", "RFC2253")
    has_cn = False
    if subj:
        body = subj.split("=", 1)[1] if subj.lower().startswith("subject=") else subj
        has_cn = any(pp.strip().startswith("CN=") and pp.strip() != "CN="
                     for pp in body.split(","))
    # SAN：-ext subjectAltName 对没有 SAN 的证书会打印 "No extensions in certificate"
    # 或直接报错。用"是否出现 DNS:/email:/URI:/IP Address:"来判断更稳，
    # 因为有的 openssl 版本对空 SAN 会输出 "X509v3 Subject Alternative Name:" 头。
    san = _ssl_text("x509", "-in", cert, "-noout", "-ext", "subjectAltName") or ""
    has_san = any(tok in san for tok in ("DNS:", "email:", "URI:", "IP Address:", "othername:"))
    if not has_cn and not has_san:
        errors.append("设备证书里没有 CN 也没有 SAN —— 后端 nodeIDFromCert()"
                      " (backend/internal/transport/server.go:311) 只从已验签证书的"
                      " (CN, SAN DNS, SAN email) 取 node_id，取不到就拒绝连接。"
                      " ⇒ 这种证书能握手但会被拒绝注册，设备侧只看到连接被关。")

    if not quiet:
        for e in errors:
            print("  X %s" % e)
        for w in warnings:
            print("  ! %s" % w)
        if not errors and not warnings:
            print("  OK 三件套校验通过（签发链 / 有效期 / 配对 / 身份 / 私钥无口令）")
    return errors, warnings


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

    # ⭐ 先校验材料**能不能用**，再谈搬运（2026-10-07 补）。
    #
    # 在此之前这一步只看"文件在不在/空不空/超没超上限" ——
    # 于是一份**自签的**设备证书、一份**已过期**的证书、一把**与证书不配对的**私钥
    # 都能一路通过，生成镜像、读回校验全绿，最后在真机上表现为"连不上"。
    # 本工具自己的 selftest 就一直在用 openssl req -x509 生成这种自签材料
    # （工具用一份永远握不上手的材料证明自己没问题）。
    #
    # --force 允许显式跳过：给"我就是要拿这份材料去试试"留一条路，
    # 但必须是**显式**的，不能是默认的。
    errors, warnings = validate_material(args.ca, args.cert, args.key)
    if errors:
        if getattr(args, "force", False):
            print("⚠ 材料校验未通过，但 --force 已指定 ⇒ 继续生成（生成的镜像很可能连不上）")
            for e in errors:
                print("    X %s" % e)
        else:
            print("材料校验未通过 ⇒ 拒绝生成（用 --force 可显式跳过，但请先确认真要这么做）：")
            for e in errors:
                print("    X %s" % e)
            return die("证书材料不可用（见上）", code=3)

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
    """自检用：现场生成一次性 CA + **由该 CA 签发**的设备证书 + 私钥。

    ## ⚠ 这里原来生成的是**自签**设备证书（2026-10-07 修）
    原实现两条命令**都是** openssl req -x509：
    `-x509` 表示"直接签发一张自签证书"，于是设备证书的 issuer == subject，
    **不由 ca.crt 签发**。拿这份材料去建 mTLS 会必然失败
    （后端 RequireAndVerifyClientCert 直接拒绝）。

    后果比"自检少测一条"更糟：**自检一直在用一份永远握不上手的材料证明自己没问题**，
    而它测的只是"镜像能不能读回字节" —— 那件事与"证书能不能用"完全无关。

    正确流程是三段：CA 自签 → 生成设备 CSR → 用 CA 签 CSR。
    """
    return [
        # 1) CA（自签，这一条用 -x509 是对的）
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
         "-keyout", "ca.key", "-out", "ca.crt", "-days", "3650",
         "-subj", "/CN=nvs-selftest-ca"],
        # 2) 设备私钥 + CSR（-new，**不是** -x509）
        ["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes",
         "-keyout", "dev.key", "-out", "dev.csr", "-subj", "/CN=nvs-selftest-dev"],
        # 3) 用 CA 签 CSR ⇒ 设备证书的 issuer = CA，subject = 设备身份
        ["openssl", "x509", "-req", "-in", "dev.csr", "-CA", "ca.crt",
         "-CAkey", "ca.key", "-CAcreateserial", "-out", "dev.crt", "-days", "365"],
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

        # ── 3.5) 材料内容校验的负向对照（2026-10-07 补）──────────────
        #
        # 为什么必须补：上面 2)/3) 两条只证明"镜像里字节搬对没有"。
        # 而"材料本身能不能握手"是**另一件事** —— 本工具此前**只**测了前者，
        # 而它自己生成的自签材料又恰好证明了它没测后者有多危险。
        # ⇒ 这里对 validate_material() 逐条喂坏材料，每一条都必须报错。
        banner("3.5) 材料内容校验：逐条喂坏材料，每条都必须被拒")
        # 先造一张"别的 CA" 以便测"未由该 CA 签发"
        for c in ([["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                    "-keyout", "other.key", "-out", "other.crt", "-days", "365",
                    "-subj", "/CN=other-ca"],
                   ["openssl", "pkey", "-in", "dev.key", "-aes256",
                    "-passout", "pass:secret", "-out", "enc.key"]]):
            if subprocess.run(c, capture_output=True).returncode != 0:
                print("  (准备负向材料失败，跳过其中一两条)")
        open("junk.crt", "w").write("not a certificate\n")

        mat_cases = [
            ("私钥与证书不配对", "dev.crt", "other.key", "配对"),
            ("私钥有口令", "dev.crt", "enc.key", "口令"),
            ("证书不是 PEM", "junk.crt", "dev.key", "无法解析"),
            ("证书不由该 CA 签发", "other.crt", "other.key", "未通过该 CA 的验签"),
        ]
        for label, c, k, expect in mat_cases:
            errs, _ = validate_material("ca.crt", c, k, quiet=True)
            if errs and any(expect in e for e in errs):
                print("  PASS: %s ⇒ 正确报错" % label)
            else:
                failures += 1
                print("  FAIL: %s **没被拒**（期望报出含 %r 的错，实际 %r）"
                      % (label, expect, errs))

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
    g.add_argument("--force", action="store_true",
                   help="材料校验未通过时仍继续生成（**显式**跳过，默认不给过）")
    g.add_argument("--warn-days", type=int, default=30,
                   help="证书剩余有效期少于该天数时给出警告（默认 30）")
    g.set_defaults(func=cmd_generate)

    s = sub.add_parser("selftest", help="现场生成测试材料跑全流程 + 负向对照")
    s.set_defaults(func=cmd_selftest)

    args = ap.parse_args()
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
