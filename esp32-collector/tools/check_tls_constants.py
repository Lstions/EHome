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

    # --- 2b. tls_io.h 镜像的软等待常量（esp_tls_errors.h / mbedtls ssl.h） ---
    # 这些值判错会让"健康连接"被当成故障重连，或反之（见 §56 / §58）。
    # dirname(HEADER) 是 components/tls_guard/include ⇒ 要上两级到 components
    io_h = read(os.path.join(os.path.dirname(HEADER), "..", "..",
                             "tls_io", "include", "tls_io.h"))
    ssl_h = read(os.path.join(idf, "components", "mbedtls", "mbedtls",
                              "include", "mbedtls", "ssl.h"))
    if io_h is None or ssl_h is None:
        bad.append("读不到 tls_io.h 或 mbedtls/ssl.h（无法核对软等待常量）")
    else:
        for macro, key in (("MBEDTLS_ERR_SSL_WANT_READ", "TLS_IO_WANT_READ"),
                           ("MBEDTLS_ERR_SSL_WANT_WRITE", "TLS_IO_WANT_WRITE"),
                           ("MBEDTLS_ERR_SSL_TIMEOUT", "TLS_IO_TIMEOUT"),
                           ("MBEDTLS_ERR_SSL_PEER_CLOSE_NOTIFY", "TLS_IO_PEER_CLOSE")):
            m4 = re.search(r"#define\s+" + macro + r"\s+\(?(-?0x[0-9A-Fa-f]+|-?\d+)", ssl_h)
            m5 = re.search(r"#define\s+" + key + r"\s+\(?(-?0x[0-9A-Fa-f]+|-?\d+)", io_h)
            if not m4 or not m5:
                bad.append("找不到 %s 或 %s" % (macro, key))
                continue
            checked += 1
            if int(m4.group(1), 0) != int(m5.group(1), 0):
                bad.append("%s = %s，但 IDF 里 %s = %s" % (key, m5.group(1), macro, m4.group(1)))

    # --- 2c. tls_guard.h 镜像的 ESP_ERR_ESP_TLS_* 码段 ---
    # 这些值判错会让"网络问题"与"证书问题"互换分类（见 §54/§60）。
    m_net = re.findall(r"#define\s+ESP_ERR_ESP_TLS_([A-Z_]+)\s+\s*\(ESP_ERR_ESP_TLS_BASE\s*\+\s*(0x[0-9A-Fa-f]+)\)", err_h)
    if not m_net:
        bad.append("esp_tls_errors.h 里找不到 ESP_ERR_ESP_TLS_* 码段")
    else:
        base_m = re.search(r"#define\s+ESP_ERR_ESP_TLS_BASE\s+(0x[0-9A-Fa-f]+)", err_h)
        base_g = re.search(r"#define\s+TLS_ESP_ERR_BASE\s+(0x[0-9A-Fa-f]+)", hdr)
        if not base_m or not base_g:
            bad.append("找不到 ESP_ERR_ESP_TLS_BASE 或 TLS_ESP_ERR_BASE")
        elif int(base_m.group(1), 0) != int(base_g.group(1), 0):
            bad.append("TLS_ESP_ERR_BASE = %s，但 IDF 里是 %s"
                       % (base_g.group(1), base_m.group(1)))
        else:
            checked += 1
            for name, off in m_net:
                key = "TLS_ESP_ERR_" + name
                f2 = re.search(r"#define\s+" + re.escape(key) + r"\s+\(TLS_ESP_ERR_BASE\s*\+\s*(0x[0-9A-Fa-f]+)\)", hdr)
                if not f2:
                    # 本组件只镜像用到的那几个，缺的跳过
                    continue
                checked += 1
                if int(f2.group(1), 0) != int(off, 0):
                    bad.append("%s 偏移 %s，但 IDF 里 ESP_ERR_ESP_TLS_%s 偏移 %s"
                               % (key, f2.group(1), name, off))

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
