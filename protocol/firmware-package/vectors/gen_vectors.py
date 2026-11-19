#!/usr/bin/env python3
"""生成 `.ehfw` golden vectors（Go 与 Python 两侧共同消费）。

规格真源：docs/计划/固件单文件包与构建打包规范-2026-11-19.md §8.2（向量清单）与
§4（格式定义）。**这些文件是二进制且进仓库**，所以必须能用一条命令重建、且重建
结果逐字节相同（`--check` 就是这条断言）。

## 为什么本脚本不复用 tools/pack_firmware.py
同一份实现只能证明「它和自己一致」。本仓在帧协议上吃过这个亏，因此照
`tools/check_golden_vectors.py` 的「第三份独立实现」做法：**这里用最朴素的
zipfile 直接拼字节**，不 import `fwimage` / `pack_firmware`。这样「打包器漂了」
与「向量写错了」才会被区分开。

## 合成镜像（不是真实构建产物）
真实 `.bin` 有 1.4 MB，没必要为了验证解析规则把它塞进仓库；镜像头只有前 0xD0
字节有意义。所以这里按 `firmwareid/identity.go` 的偏移表造一个最小可用镜像：

  0x00  0xE9                image magic
  0x03  0x30                spi_size 高半字节 3 => 8MB
  0x0C  0x0D 0x00           chip_id u16 LE 0x000D => esp32c6
  0x20  32 54 CD AB         app desc magic 0xABCD5432
  0x30  "2.9.0"             version[32]
  0x50  "ehome_collector"   project[32]
  0x70  "00:00:00"          time[16]
  0x80  "2026-11-19"        date[16]
  0x90  "v6.1.0"            idf_ver[32]
  0xB0  sha256(seed)[:32]   app_elf_sha256[32]
  0xD0  + 2048 B 确定性填充  （让 size/sha256 有非平凡取值）

## 用法
    python3 protocol/firmware-package/vectors/gen_vectors.py          # 重建全部向量
    python3 protocol/firmware-package/vectors/gen_vectors.py --check  # 只比对，不写
"""
import argparse
import hashlib
import io
import json
import os
import struct
import sys
import zipfile

HERE = os.path.dirname(os.path.abspath(__file__))

# ---- 与 §4 对齐的格式常量（故意不复用 pack_firmware.py，见文件头）----
FORMAT_NAME = "ehome-firmware"
FORMAT_VERSION = 1
MANIFEST_ENTRY = "fw-manifest.json"
ARTIFACT_NAME = "ehome_collector.bin"
PACKER_NAME = "pack_firmware.py"
PACKER_VERSION = "1.0.0"
PROFILE = "c6-n8"
VERSION = "2.9.0"
IDF_VERSION = "v6.1.0"
PROJECT = "ehome_collector"
ZIP_DATE_TIME = (1980, 1, 1, 0, 0, 0)
# 高 16 位是 Unix mode，低 16 位是 DOS 属性。必须带 S_IFREG（0o100000），
# 否则 `zipinfo -l` 显示成 `?rw-r--r--`（缺文件类型位）。与
# esp32-collector/tools/pack_firmware.py 的 ZIP_ENTRY_ATTR **必须同值**。
ZIP_ENTRY_ATTR = 0o100644 << 16

# ---- 合成镜像参数（镜像头偏移表照抄 firmwareid/identity.go）----
OFF_MAGIC = 0x00
OFF_SPI_SIZE = 0x03
OFF_CHIP_ID = 0x0C
OFF_DESC_MAGIC = 0x20
OFF_VERSION = 0x30
OFF_PROJECT = 0x50
OFF_TIME = 0x70
OFF_DATE = 0x80
OFF_IDF_VER = 0x90
OFF_ELF_SHA256 = 0xB0
HEADER_SIZE = 0xD0
IMAGE_MAGIC = 0xE9
DESC_MAGIC = 0xABCD5432
CHIP_C6_ID = 0x000D
SPI_SIZE_8MB = 3
BODY_SIZE = 2048
ELF_SEED = b"ehome-collector-synthetic-image"


def put_cstr(header, offset, width, text):
    """往定宽字段写 C 字符串（NUL 填充）。

    ⚠ 必须**原地**写定宽，不能写成 `header[off:off+width] = text.encode()`：
    对 bytearray 的切片赋值会**改变长度**（短值收缩、长值扩张），后面所有偏移
    会整体错位。2026-11-19 就踩过一次：0x50 之后的字段全部移位 26 B，
    而 0xD0 之前都能解析成功（chip/flash/version 在前 0x50），错得很安静。
    """
    raw = text.encode("ascii")
    if len(raw) > width:
        raise ValueError("field at 0x%02x: %r is longer than %d bytes" % (offset, text, width))
    header[offset:offset + width] = raw + b"\x00" * (width - len(raw))


def build_image(version=VERSION, chip_id=CHIP_C6_ID, spi_size=SPI_SIZE_8MB,
                project=PROJECT, idf_version=IDF_VERSION, body_size=BODY_SIZE):
    """造一个最小可用 app image（0xD0 头 + 确定性填充）。"""
    header = bytearray(HEADER_SIZE)
    header[OFF_MAGIC] = IMAGE_MAGIC
    header[OFF_SPI_SIZE] = (spi_size << 4) & 0xF0
    struct.pack_into("<H", header, OFF_CHIP_ID, chip_id)
    struct.pack_into("<I", header, OFF_DESC_MAGIC, DESC_MAGIC)
    put_cstr(header, OFF_VERSION, 32, version)
    put_cstr(header, OFF_PROJECT, 32, project)
    put_cstr(header, OFF_TIME, 16, "00:00:00")
    put_cstr(header, OFF_DATE, 16, "2026-11-19")
    put_cstr(header, OFF_IDF_VER, 32, idf_version)
    header[OFF_ELF_SHA256:OFF_ELF_SHA256 + 32] = hashlib.sha256(ELF_SEED).digest()
    assert len(header) == HEADER_SIZE
    body = bytes((i * 7 + 13) & 0xFF for i in range(body_size))
    return bytes(header) + body


def build_manifest(artifact_name, artifact_bytes, profile=PROFILE, psram=False,
                   format_version=FORMAT_VERSION, sha256_override=None):
    """拼清单。**不含 `build` 块**：向量是固定输入的纯函数（INV-6），
    而 `build.*` 里是打包机的 git 状态，不属于「输入」。
    """
    digest = (hashlib.sha256(artifact_bytes).hexdigest()
              if sha256_override is None else sha256_override)
    return {
        "format": FORMAT_NAME,
        "format_version": format_version,
        "packer": {"name": PACKER_NAME, "version": PACKER_VERSION},
        "artifact": {"name": artifact_name, "size": len(artifact_bytes), "sha256": digest},
        "identity": {"psram": psram, "profile": profile},
    }


def manifest_bytes(manifest):
    return (json.dumps(manifest, indent=2, ensure_ascii=False) + "\n").encode("utf-8")


def render_package(entries):
    """把 [(name, data, compress_type)] 渲染成 ZIP 字节；条目按名排序（§7.5 确定性）。"""
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w", compression=zipfile.ZIP_STORED, allowZip64=False) as zf:
        for name, data, compress in sorted(entries, key=lambda item: item[0]):
            info = zipfile.ZipInfo(name, date_time=ZIP_DATE_TIME)
            info.compress_type = compress
            info.create_system = 3
            info.external_attr = ZIP_ENTRY_ATTR
            zf.writestr(info, data)
    return buffer.getvalue()


def flip_digest(digest):
    """改一位 sha256：让「摘要不符」成为唯一的问题，其余字段仍然自洽。"""
    return digest[:-1] + ("1" if digest[-1] != "1" else "2")


# 每条用例必须**显式**带这两个键（空串 = 该阶段必须成功）。缺键会逼消费方写
# `.get()` 兜底，而兜底分支没人测；Go 侧的 struct 也分不清「缺键」与「空串」。
REQUIRED_CASE_KEYS = ("file", "open_error", "handler_error")


def _validate_cases(cases):
    """--check 与生成两条路径都先过这里：加用例忘了写键，立刻红。"""
    problems = []
    for case in cases:
        missing = [key for key in REQUIRED_CASE_KEYS if key not in case]
        if missing:
            problems.append("%r missing %s" % (case.get("file", "<no file>"), ", ".join(missing)))
    if problems:
        raise SystemExit("ERROR: cases.json is malformed: " + "; ".join(problems))


def build_outputs():
    """返回 {文件名: 字节}。所有向量都由这一处数据推导，避免手写不一致。"""
    image = build_image()
    manifest_ok = build_manifest(ARTIFACT_NAME, image)
    outputs = {}
    cases = []

    def add(file_name, entries, case):
        # 每条用例都**显式**带 open_error / handler_error（空串 = 该阶段必须成功）。
        # 缺键会让消费方写 .get() 兜底，而兜底的分支没人测 —— 契约要无歧义。
        normalized = {"file": file_name,
                      "open_error": case.pop("open_error", ""),
                      "handler_error": case.pop("handler_error", "")}
        normalized.update(case)
        outputs[file_name] = render_package(entries)
        cases.append(normalized)

    two_entries = lambda manifest: [(MANIFEST_ENTRY, manifest_bytes(manifest), zipfile.ZIP_STORED),
                                    (ARTIFACT_NAME, image, zipfile.ZIP_STORED)]

    # 1) 正例：两条目、摘要一致、profile 与镜像相符。
    add("ok_c6-n8.ehfw", two_entries(manifest_ok),
        {"open_error": "", "handler_error": "", "expected_file": "ok_c6-n8.expected.json",
         "note": "两条目正例；ExtractArtifactTo 的 (size, sha256) 与清单一致"})

    # 2) 摘要不符：包结构完全合法，只有 artifact.sha256 错一位 → handler 第 5 步（INV-2）。
    manifest_bad_digest = build_manifest(
        ARTIFACT_NAME, image,
        sha256_override=flip_digest(manifest_ok["artifact"]["sha256"]))
    add("bad_digest.ehfw", two_entries(manifest_bad_digest),
        {"open_error": "", "handler_error": "FW_PKG_DIGEST_MISMATCH",
         "note": "结构合法但 artifact.sha256 与实测不符 → handler 第 5 步拒绝"
                 "（Open 只解出不比较，见评审 B-2）"})

    # 3) 条目数 3 → INV-1。
    add("bad_entry_count.ehfw",
        two_entries(manifest_ok) + [("extra.txt", b"third entry\n", zipfile.ZIP_STORED)],
        {"open_error": "FW_PKG_INVALID",
         "note": "3 个条目（多一个 extra.txt）→ INV-1「恰好两条」"})

    # 4) 条目名 ../evil.bin → INV-4。清单里写同一个名字，让「名字非法」成为唯一问题。
    manifest_bad_name = build_manifest("../evil.bin", image)
    add("bad_entry_name.ehfw",
        [(MANIFEST_ENTRY, manifest_bytes(manifest_bad_name), zipfile.ZIP_STORED),
         ("../evil.bin", image, zipfile.ZIP_STORED)],
        {"open_error": "FW_PKG_INVALID",
         "note": "条目名 ../evil.bin → INV-4 ^[a-z0-9][a-z0-9._-]{0,63}$（无目录、全小写）"})

    # 5) format_version 99 → 升版规则（§4.6）。
    manifest_future = build_manifest(ARTIFACT_NAME, image, format_version=99)
    add("bad_format_version.ehfw", two_entries(manifest_future),
        {"open_error": "FW_PKG_UNSUPPORTED",
         "note": "format_version 99 > 1 → FW_PKG_UNSUPPORTED（§4.6 升版规则）"})

    # 6) 压缩比超限：200 KiB 全零用 Deflate 压到约 200 B → 远超 100:1。
    bomb = bytes(200 * 1024)
    manifest_bomb = build_manifest(ARTIFACT_NAME, bomb)
    add("bad_compression_ratio.ehfw",
        [(MANIFEST_ENTRY, manifest_bytes(manifest_bomb), zipfile.ZIP_DEFLATED),
         (ARTIFACT_NAME, bomb, zipfile.ZIP_DEFLATED)],
        {"open_error": "FW_PKG_TOO_LARGE",
         "note": "200 KiB 全零 → Deflate 约 200 B，压缩比 > 100:1 → INV-5（解压前拒绝）"})

    # ok 的期望值：清单 + 镜像身份（Go 侧 vectors_test.go 直接断言这些字段）。
    outputs["ok_c6-n8.expected.json"] = (json.dumps({
        "manifest": manifest_ok,
        "image": {
            "chip": "esp32c6",
            "chip_id": CHIP_C6_ID,
            "flash_mb": 8,
            "version": VERSION,
            "project": PROJECT,
            "idf_version": IDF_VERSION,
            "elf_sha256": hashlib.sha256(ELF_SEED).digest().hex(),
            "size_bytes": len(image),
            "sha256": hashlib.sha256(image).hexdigest(),
        },
    }, indent=2, ensure_ascii=False) + "\n").encode("utf-8")
    outputs["cases.json"] = (json.dumps({
        "generator": "protocol/firmware-package/vectors/gen_vectors.py",
        "format": FORMAT_NAME,
        "format_version": FORMAT_VERSION,
        "note": ("open_error = firmwarepkg.Open 期望的错误码（空串 = 必须成功）；"
                 "handler_error = 摘要比对那一步（§5.2 步骤 5）期望的错误码。"),
        "cases": cases,
    }, indent=2, ensure_ascii=False) + "\n").encode("utf-8")
    return outputs


def main(argv):
    parser = argparse.ArgumentParser(description="regenerate .ehfw golden vectors")
    parser.add_argument("--check", action="store_true",
                        help="compare against the files on disk instead of writing")
    args = parser.parse_args(argv)

    outputs = build_outputs()
    _validate_cases(json.loads(outputs["cases.json"].decode("utf-8"))["cases"])
    stale = []
    for name in sorted(outputs):
        path = os.path.join(HERE, name)
        current = None
        if os.path.isfile(path):
            with open(path, "rb") as fp:
                current = fp.read()
        if current == outputs[name]:
            continue
        if args.check:
            stale.append(name)
            continue
        with open(path, "wb") as fp:
            fp.write(outputs[name])
        print("wrote %s (%d B)" % (name, len(outputs[name])))

    if not args.check:
        return 0
    if stale:
        print("ERROR: vectors are stale: %s (rerun without --check)" % ", ".join(stale),
              file=sys.stderr)
        return 1
    print("vectors are up to date: %s" % ", ".join(sorted(outputs)))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
