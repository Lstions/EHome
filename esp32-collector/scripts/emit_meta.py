#!/usr/bin/env python3
"""构建期产出固件 sidecar：<bin> -> 同名 .meta.json（方案 §4.2 / WBS T0.2）。

## 为什么必须由构建侧产出
后端上传**要求** sidecar（缺 -> 400 FW_META_REQUIRED），而镜像头里**没有**
PSRAM 这一位（方案 §3.4 判断 1：spi_size/chip_id 在头里，SPIRAM 不在）。
所以 sidecar 是「设备自证 + 构建自证」两半里的后一半，只有构建现场知道：
  - 这个 profile 到底是 s3-n16 还是 s3p-n16（同一 SoC、同一份分区表）；
  - 本机 IDF 版本与 PROJECT_VER。
在此脚本之前只有 ehomectl firmware meta-gen 能补，等于每个版本都要**手工补一次**
—— 手工那一步迟早会被忘记，而忘记的表现是"上传被 400 拒绝"，不是"固件坏了"。

## 判据（与后端 firmwareid 逐字对齐，勿在此处另立一份真源）
backend/internal/firmwareid/identity.go 是镜像偏移表的唯一真源，本文件只是它的
Python 复刻（偏移、magic、chip_id、spi_size 取值表全部照抄并注明出处）。
backend/internal/firmwareid/sidecar.go 定义 7 个键与类型：
    profile(str) chip(str) flash_mb(u16) psram(bool) version(str) idf_ver(str) sha256(str)
**恰好 7 个**：后端用 DisallowUnknownFields，多一个键就是 400。

## 取值来源与交叉校验（fail-closed，绝不产出半份 sidecar）
  - chip / flash_mb / version / idf_ver：取**镜像内**的值（后端 CheckSidecar 拿
    镜像与 sidecar 比对，镜像才是唯一真源，方案判断 5）；
  - 并与 sdkconfig 交叉校验（CONFIG_IDF_TARGET / CONFIG_ESPTOOLPY_FLASHSIZE），
    不一致直接报错 —— 这挡的是"sidecar 与 .bin 不是同一次构建产物"；
  - version 再与 PROJECT_VER 交叉校验（构建脚本传入 CMakeLists 路径）；
  - psram：来自 sdkconfig 的 CONFIG_SPIRAM（C6 没有该符号 => 恒 false），并与
    profile 名的 p 位互相校验（s3p-* 必须为 true，s3-* 必须为 false）；
  - sha256：对整个 .bin 计算（与后端的 fileSHA256 同口径）。
解析不出来（魔数不对 / chip_id 不支持 / spi_size 不是 8/16MB / 字段为空 /
交叉校验失败）→ 退出码 1，**不写文件**。写入用临时文件 + os.replace，避免
中断后留下半份 JSON 被当成合法 sidecar。

## 用法
    emit_meta.py --bin build61/c6-n16/ehome_collector.bin \
                 --sdkconfig build61/c6-n16/sdkconfig \
                 --profile c6-n16 \
                 [--project-cmakelists CMakeLists.txt] [--out <path>] [--psram yes|no]
"""
import argparse
import hashlib
import json
import os
import re
import struct
import sys

# 镜像内偏移表：**照抄** backend/internal/firmwareid/identity.go（唯一真源）。
OFF_MAGIC = 0x00
OFF_SPI_SIZE = 0x03
OFF_CHIP_ID = 0x0C
OFF_DESC_MAGIC = 0x20
OFF_VERSION = 0x30
OFF_PROJECT = 0x50
OFF_IDF_VER = 0x90
IMAGE_MAGIC = 0xE9
DESC_MAGIC = 0xABCD5432
HEADER_SIZE = 0xD0  # app_elf_sha256[32] 结束于 0xB0+0x20
PROJECT_COLLECTOR = "ehome_collector"

# chip_id -> 规范化芯片名（identity.go chipByID）
CHIP_BY_ID = {0x0009: "esp32s3", 0x000D: "esp32c6"}
# esp_image_header_t.spi_size 高半字节 -> MB（identity.go flashMBBySpiSize：
# 只接受 3/4；1/2/4MB 档位在本项目不存在，放进来只会多一条误刷通道）
FLASH_MB_BY_SPI_SIZE = {3: 8, 4: 16}
# 后端 Sidecar 的键集合与顺序（struct 字段序 = Go MarshalIndent 的输出序，
# 保持同序可让本脚本产物与 meta-gen 产物**逐字节相同**，比对时无需归一化）。
SIDE_KEYS = ["profile", "chip", "flash_mb", "psram", "version", "idf_ver", "sha256"]


class EmitError(Exception):
    """所有可预期的"解析不出来"都走这里，main() 统一打印并以 1 退出。"""


def read_c_string(buf):
    """C 字符串：NUL 截断 + strip（identity.go cString 同口径）。"""
    return buf.split(b"\x00", 1)[0].decode("utf-8", "replace").strip()


def parse_image_identity(bin_path):
    """从镜像头解析身份。任何不支持的情况都抛 EmitError，不做猜测。"""
    size = os.path.getsize(bin_path)
    if size < HEADER_SIZE:
        raise EmitError("image is %d bytes, need at least %d" % (size, HEADER_SIZE))
    with open(bin_path, "rb") as fp:
        head = fp.read(HEADER_SIZE)
    if head[OFF_MAGIC] != IMAGE_MAGIC:
        raise EmitError("magic 0x%02x, want 0x%02x (not an ESP app image)" % (head[OFF_MAGIC], IMAGE_MAGIC))
    desc_magic = struct.unpack_from("<I", head, OFF_DESC_MAGIC)[0]
    if desc_magic != DESC_MAGIC:
        raise EmitError("app descriptor magic 0x%08x, want 0x%08x" % (desc_magic, DESC_MAGIC))
    project = read_c_string(head[OFF_PROJECT:OFF_PROJECT + 32])
    if project != PROJECT_COLLECTOR:
        raise EmitError("project %r, want %r" % (project, PROJECT_COLLECTOR))
    chip_id = struct.unpack_from("<H", head, OFF_CHIP_ID)[0]
    chip = CHIP_BY_ID.get(chip_id)
    if chip is None:
        raise EmitError("chip_id 0x%04x is not supported" % chip_id)
    spi_size = head[OFF_SPI_SIZE] >> 4
    flash_mb = FLASH_MB_BY_SPI_SIZE.get(spi_size)
    if flash_mb is None:
        raise EmitError("spi_size 0x%x is not 3 (8MB) or 4 (16MB)" % spi_size)
    version = read_c_string(head[OFF_VERSION:OFF_VERSION + 32])
    idf_ver = read_c_string(head[OFF_IDF_VER:OFF_IDF_VER + 32])
    if not version:
        raise EmitError("image version field is empty")
    if not idf_ver:
        raise EmitError("image idf_ver field is empty")
    return {"chip": chip, "flash_mb": flash_mb, "version": version, "idf_ver": idf_ver}


def read_sdkconfig(path):
    """读 kconfig 文本。返回 dict：键名 -> 字符串值（n/y/数字/字符串）或 None（未设置）。"""
    if not os.path.isfile(path):
        raise EmitError("sdkconfig not found: %s" % path)
    values = {}
    with open(path, "r", encoding="utf-8", errors="replace") as fp:
        for line in fp:
            line = line.strip()
            m = re.match(r"^(CONFIG_[A-Za-z0-9_]+)=(.*)$", line)
            if m:
                v = m.group(2)
                if len(v) >= 2 and v.startswith('"') and v.endswith('"'):
                    v = v[1:-1]
                values[m.group(1)] = v
                continue
            m = re.match(r"^# (CONFIG_[A-Za-z0-9_]+) is not set$", line)
            if m:
                values[m.group(1)] = None
    return values


def cross_check_image_vs_config(ident, cfg):
    """镜像自证与 sdkconfig 必须一致；不一致说明 sidecar 与 .bin 不是同一次构建。"""
    target = cfg.get("CONFIG_IDF_TARGET")
    if not target:
        raise EmitError("sdkconfig has no CONFIG_IDF_TARGET")
    if target != ident["chip"]:
        raise EmitError("sdkconfig CONFIG_IDF_TARGET=%r != image chip %r" % (target, ident["chip"]))
    flash = cfg.get("CONFIG_ESPTOOLPY_FLASHSIZE")
    if not flash:
        raise EmitError("sdkconfig has no CONFIG_ESPTOOLPY_FLASHSIZE")
    m = re.match(r"^(\d+)MB$", flash)
    if not m:
        raise EmitError("CONFIG_ESPTOOLPY_FLASHSIZE=%r is not <N>MB" % flash)
    if int(m.group(1)) != ident["flash_mb"]:
        raise EmitError("sdkconfig flash size %r != image flash_mb %d"
                        % (flash, ident["flash_mb"]))


def resolve_psram(cfg, cli_value, chip):
    """psram 是本卡唯一"镜像头证明不了"的位：先问 sdkconfig，再问 CLI，再与 profile 互校。

    为什么要传 chip：C6 的 Kconfig 里**根本没有** CONFIG_SPIRAM 符号（该 SoC 无
    PSRAM 能力），所以"符号缺失"对 C6 是**正常的 false**，对 S3 才是异常
    （S3 一定有该符号，只是可能为 n）。不做这条区分，C6 会被误判成"解析不出来"。
    """
    raw = cfg.get("CONFIG_SPIRAM", "absent")
    if raw == "absent":
        if chip != "esp32c6":
            raise EmitError("sdkconfig has no CONFIG_SPIRAM for %s (S3 always defines it; "
                            "is this the right sdkconfig?)" % chip)
        from_cfg = False
    else:
        from_cfg = (raw == "y")
    from_cli = None if cli_value is None else (cli_value == "yes")

    if from_cli is not None and from_cli != from_cfg:
        raise EmitError("--psram %s contradicts sdkconfig CONFIG_SPIRAM=%s (chip=%s)"
                        % (cli_value, "y" if from_cfg else ("n" if raw != "absent" else "absent"), chip))
    return from_cfg if from_cli is None else from_cli


def build_profile_name(chip, flash_mb, psram):
    """与后端 firmwareid.BuildProfile 同算法：c6/s3 [+p] + '-n<MB>'。"""
    short = {"esp32c6": "c6", "esp32s3": "s3"}[chip]
    if psram:
        short += "p"
    return "%s-n%d" % (short, flash_mb)


def check_project_version(version, cmakelists_path):
    """镜像内 version 必须等于 PROJECT_VER（构建脚本传入 CMakeLists.txt 路径）。"""
    if not cmakelists_path:
        return
    if not os.path.isfile(cmakelists_path):
        raise EmitError("project CMakeLists not found: %s" % cmakelists_path)
    with open(cmakelists_path, "r", encoding="utf-8", errors="replace") as fp:
        text = fp.read()
    m = re.search(r'^\s*set\s*\(\s*PROJECT_VER\s+"([^"]+)"\s*\)', text, re.MULTILINE)
    if not m:
        raise EmitError("PROJECT_VER not found in %s" % cmakelists_path)
    declared = m.group(1).strip()
    if declared != version:
        raise EmitError("image version %r != PROJECT_VER %r in %s "
                        "(stale .bin? rebuild before emitting the sidecar)"
                        % (version, declared, cmakelists_path))


def sha256_file(path):
    hasher = hashlib.sha256()
    with open(path, "rb") as fp:
        for chunk in iter(lambda: fp.read(1 << 20), b""):
            hasher.update(chunk)
    return hasher.hexdigest()


def validate_sidecar(side):
    """写盘前最后一道：7 个键、类型、取值域。任一不满足即抛错（不写文件）。"""
    if list(side.keys()) != SIDE_KEYS:
        raise EmitError("sidecar keys %r != %r" % (list(side.keys()), SIDE_KEYS))
    if not isinstance(side["psram"], bool):
        raise EmitError("psram must be a JSON boolean, got %r" % (side["psram"],))
    if not isinstance(side["flash_mb"], int) or side["flash_mb"] not in (8, 16):
        raise EmitError("flash_mb must be 8 or 16, got %r" % (side["flash_mb"],))
    for key in ("profile", "chip", "version", "idf_ver", "sha256"):
        if not isinstance(side[key], str) or not side[key].strip():
            raise EmitError("%s must be a non-empty string, got %r" % (key, side[key]))
    if not re.match(r"^[0-9a-f]{64}$", side["sha256"]):
        raise EmitError("sha256 must be 64 lowercase hex chars, got %r" % (side["sha256"],))


def main(argv):
    parser = argparse.ArgumentParser(add_help=True, description="emit <bin> sidecar (.meta.json)")
    parser.add_argument("--bin", required=True, help="path to ehome_collector.bin")
    parser.add_argument("--sdkconfig", required=True, help="path to the generated sdkconfig")
    parser.add_argument("--profile", default="", help="build profile (e.g. c6-n16); cross-checked")
    parser.add_argument("--project-cmakelists", default="", help="path to project CMakeLists.txt (PROJECT_VER cross-check)")
    parser.add_argument("--out", default="", help="output path (default: strip .bin + .meta.json)")
    parser.add_argument("--psram", choices=["yes", "no"], default=None,
                        help="override PSRAM bit; cross-checked against sdkconfig/profile")
    args = parser.parse_args(argv)

    bin_path = os.path.abspath(args.bin)
    if not os.path.isfile(bin_path):
        raise EmitError("bin not found: %s" % args.bin)
    cfg = read_sdkconfig(args.sdkconfig)
    ident = parse_image_identity(bin_path)
    cross_check_image_vs_config(ident, cfg)
    check_project_version(ident["version"], args.project_cmakelists)
    psram = resolve_psram(cfg, args.psram, ident["chip"])

    profile = build_profile_name(ident["chip"], ident["flash_mb"], psram)
    if args.profile and args.profile.strip().lower() != profile:
        raise EmitError("--profile %r != profile implied by image+psram %r "
                        "(chip=%s flash_mb=%d psram=%s)"
                        % (args.profile, profile, ident["chip"], ident["flash_mb"], "yes" if psram else "no"))

    side = {
        "profile": profile,
        "chip": ident["chip"],
        "flash_mb": ident["flash_mb"],
        "psram": psram,
        "version": ident["version"],
        "idf_ver": ident["idf_ver"],
        "sha256": sha256_file(bin_path),
    }
    validate_sidecar(side)

    out_path = args.out or (os.path.splitext(bin_path)[0] + ".meta.json")
    os.makedirs(os.path.dirname(os.path.abspath(out_path)), exist_ok=True)
    payload = json.dumps(side, indent=2, ensure_ascii=False) + "\n"
    tmp_path = out_path + ".tmp"
    try:
        with open(tmp_path, "w", encoding="utf-8") as fp:
            fp.write(payload)
            fp.flush()
            os.fsync(fp.fileno())
        os.replace(tmp_path, out_path)
    except OSError as exc:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
        raise EmitError("write %s: %s" % (out_path, exc))

    print("wrote %s" % out_path)
    print("  profile=%s chip=%s flash_mb=%d psram=%s version=%s idf_ver=%s sha256=%s..."
          % (side["profile"], side["chip"], side["flash_mb"],
             "yes" if side["psram"] else "no", side["version"], side["idf_ver"], side["sha256"][:12]))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except EmitError as err:
        print("ERROR: emit_meta: %s" % err, file=sys.stderr)
        sys.exit(1)
