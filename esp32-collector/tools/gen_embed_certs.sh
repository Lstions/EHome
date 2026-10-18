#!/usr/bin/env bash
# 把设备证书编译成 .S 源文件，供 EHOME_EMBED_CERT_DIR 构建使用。
#
# 为什么需要单独一步（2026-10-18 实测得出）：
#   OTA 只写 app 分区，碰不到 NVS；要给"还跑 MQTT 固件、NVS 里没有服务端
#   当前 PKI"的现场设备做远程迁移，证书必须随固件走。做法是把该设备自己的
#   ca/cert/key 编进 app 分区。
#
# 为什么要生成 .S 而不用 IDF 的 target_add_binary_data：
#   ① 指向 COMPONENT_LIB 时，.S 只是静态库成员，不被显式引用就不链接 ⇒
#      nm 看得到符号、最终链接 undefined reference（实测踩到）。
#   ② 另建 OBJECT 库再 link，IDF 的 ldgen 会对它求 $<TARGET_FILE:...> ⇒
#      "Target is not an executable or library"，配置期失败（实测踩到）。
#   ⇒ 唯一可靠路径：预生成 .S，作为普通源文件交给 target_sources。
#
# ⚠ 生成的 .S 必须**以 NUL 字节结尾**：esp-tls 要求 cacert_bytes 含终止符，
#   而长度按 (end - start) 计算。少了这个 0x00，设备会读到 .rodata 里紧邻
#   下一个对象的字节，mbedtls 解析不可预期 —— 最坏情况是设备永久失联。
#
# 用法:
#   tools/gen_embed_certs.sh <证书目录>
#   目录内需有 ca.pem / cert.pem / key.pem，产物写到 <目录>/gen/*.S

set -euo pipefail

DIR="${1:?用法: $0 <证书目录>}"
GEN="$DIR/gen"

for f in ca.pem cert.pem key.pem; do
    [[ -f "$DIR/$f" ]] || { echo "ERROR: 缺少 $DIR/$f" >&2; exit 1; }
done

mkdir -p "$GEN"

IDF_PY="${IDF_PYTHON_ENV_PATH:+$IDF_PYTHON_ENV_PATH/bin/}python3"
command -v "$IDF_PY" >/dev/null 2>&1 || IDF_PY=python3

"$IDF_PY" - "$DIR" "$GEN" <<'PY'
import sys, os
src_dir, gen_dir = sys.argv[1], sys.argv[2]
items = [("ca.pem", "ca_pem"), ("cert.pem", "cert_pem"), ("key.pem", "key_pem")]

for fname, sym in items:
    raw = open(os.path.join(src_dir, fname), "rb").read()
    if raw.rstrip(b"\x00") == b"":
        sys.exit("ERROR: %s 为空" % fname)
    if raw[-1:] != b"\x00":
        raw += b"\x00"          # esp-tls 要求含 NUL 终止符
    out = ["/* embedded %s -- 由 tools/gen_embed_certs.sh 生成，勿手改 */" % fname,
           ".section .rodata.embedded,\"a\",@progbits",
           ".global _binary_%s_start" % sym,
           ".align 4",
           "_binary_%s_start:" % sym]
    for i in range(0, len(raw), 16):
        out.append("    .byte " + ",".join("0x%02x" % b for b in raw[i:i+16]))
    out += [".global _binary_%s_end" % sym,
            "_binary_%s_end:" % sym, ""]
    path = os.path.join(gen_dir, sym + ".S")
    open(path, "w").write("\n".join(out))
    print("  %-24s %5d B (含 NUL)" % (sym + ".S", len(raw)))
PY

echo "OK: $GEN"
