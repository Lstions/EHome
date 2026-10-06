#!/usr/bin/env python3
"""门禁：tls_guard.h 里镜像的 IDF/mbedTLS 常量必须与 IDF 头文件一致。

## 为什么需要
tls_guard 为了让**宿主机可测**，把 IDF 的枚举/位掩码镜像成了本组件的宏
（组件不依赖 IDF）。镜像就有漂移风险：
  - ESP_TLS_ERR_TYPE_* 的顺序改了 => 归约表整体错位；
  - MBEDTLS_X509_BADCERT_* 的值改了 => "时间相关位"判错 => 自愈路径失效。
两者都不会崩，只会让**没校时的设备被判成永久失败**（K11 全站失联）。

同 check_stub_enum_sync 的思路：不靠人记，直接读源头核对。

## 用法
  python3 tools/check_tls_constants.py            # 自动找 IDF_PATH
  IDF_PATH=/path/to/esp-idf python3 ...           # 显式指定
  （找不到 IDF 时退出码 0 并打印 SKIP —— 不阻塞无 IDF 的环境）
"""
import os
import re
import sys

HEADER = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                      "..", "components", "tls_guard", "include", "tls_guard.h")


def find_idf():
    p = os.environ.get("IDF_PATH")
    if p and os.path.isdir(p):
        return p
    for cand in ("/home/sun/env/esp-idf", os.path.expanduser("~/esp/esp-idf")):
        if os.path.isdir(cand):
            return cand
    return None


def read(path):
    try:
        return open(path, encoding="utf-8", errors="replace").read()
    except OSError:
        return None


RE_ENUM = re.compile(r"typedef enum\s*\{(.*?)\}\s*esp_tls_error_type_t", re.S)
RE_ERTYPE = re.compile(r"ESP_TLS_ERR_TYPE_([A-Z_]+)")
RE_X509 = re.compile(r"#define\s+(MBEDTLS_X509_BADCERT_[A-Z_]+)\s+(0x[0-9A-Fa-f]+|\d+)")
RE_TIME_REL = re.compile(r"#define\s+TLS_CERTFLAG_TIME_RELATED\s+\(([^)]*)\)")


def main():
    idf = find_idf()
    if idf is None:
        print("SKIP check_tls_constants: 找不到 IDF_PATH（无 IDF 环境下不阻塞）")
        return 0

    hdr = read(HEADER)
    if hdr is None:
        print("FAIL 读不到 " + HEADER)
        return 1

    bad = []
    checked = 0

    # --- 1. esp_tls_error_type_t 的顺序 ---
    err_h = read(os.path.join(idf, "components", "esp-tls", "esp_tls_errors.h"))
    if err_h is None:
        print("FAIL 读不到 esp_tls_errors.h")
        return 1
    m = RE_ENUM.search(err_h)
    if not m:
        print("FAIL esp_tls_errors.h 里找不到 esp_tls_error_type_t")
        return 1
    names = [n for n in RE_ERTYPE.findall(m.group(1)) if not n.endswith("MAX")]
    for idx, short in enumerate(names):
        key = "TLS_ERGTYPE_" + short
        found = re.search(r"#define\s+" + re.escape(key) + r"\s+(\d+)", hdr)
        if not found:
            bad.append("缺少 %s（应为 %d）" % (key, idx))
            continue
        checked += 1
        if int(found.group(1)) != idx:
            bad.append("%s = %s，但 IDF 里 ESP_TLS_ERR_TYPE_%s 位于第 %d 位"
                       % (key, found.group(1), short, idx))

    # --- 2. MBEDTLS_X509_BADCERT_* 的值 ---
    x509 = read(os.path.join(idf, "components", "mbedtls", "mbedtls",
                             "include", "mbedtls", "x509.h"))
    if x509 is None:
        print("FAIL 读不到 mbedtls/x509.h")
        return 1
    for mm in RE_X509.finditer(x509):
        name, val = mm.group(1), int(mm.group(2), 0)
        key = "TLS_CERTFLAG_" + name.replace("MBEDTLS_X509_BADCERT_", "")
        found = re.search(r"#define\s+" + re.escape(key) + r"\s+(0x[0-9A-Fa-f]+|\d+)", hdr)
        if not found:
            continue      # 本组件只镜像用到的那几个
        checked += 1
        if int(found.group(1), 0) != val:
            bad.append("%s = %s，但 IDF 里 %s = 0x%X"
                       % (key, found.group(1), name, val))

    # --- 3. 时效位掩码必须恰好含 EXPIRED 与 FUTURE ---
    m3 = RE_TIME_REL.search(hdr)
    if not m3:
        bad.append("找不到 TLS_CERTFLAG_TIME_RELATED 定义")
    else:
        expr = m3.group(1)
        for need in ("TLS_CERTFLAG_EXPIRED", "TLS_CERTFLAG_FUTURE"):
            if need not in expr:
                bad.append("TLS_CERTFLAG_TIME_RELATED 必须包含 %s" % need)

    print("核对 %d 个镜像常量（IDF: %s）" % (checked, idf))
    if bad:
        for b in bad:
            print("FAIL " + b)
        return 1
    print("PASS check_tls_constants: 镜像常量与 IDF 一致，时效位掩码正确")
    return 0


if __name__ == "__main__":
    sys.exit(main())
