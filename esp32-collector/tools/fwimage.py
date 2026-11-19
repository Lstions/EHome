#!/usr/bin/env python3
"""ESP 镜像头解析 + sdkconfig 读取（构建侧共享库，无打包逻辑）。

## 偏移表的唯一真源
backend/internal/firmwareid/identity.go 是镜像偏移表的**唯一真源**：本文件只是它的
Python 复刻（偏移、magic、chip_id、spi_size 取值表全部照抄并注明出处）。
两端一旦不一致，构建机打出的包会被服务端拒收（FW_NOT_IMAGE / FW_FLASH_UNSUPPORTED
之类），所以改任何一边都必须同时改另一边；`esp32-collector/tools/test_pack_firmware.py`
与 `backend/internal/firmwarepkg/vectors_test.go` 消费**同一批** vectors
（`protocol/firmware-package/vectors/`）来兜住这种漂移。

## 本文件只做「读」，不做「判断」
镜像头解析（identity.go Parse 的 Python 版）+ sdkconfig 文本读取。
版本比对、PSRAM 位、档位名这些**判断**留给调用方（pack_firmware.py），
避免同一条规则在两个文件里各写一遍。

## 为什么 PSRAM 不在这里
PSRAM **无法从镜像头导出**（s3-n16 与 s3p-n16 的前 0x100 字节除 elf_sha256 外完全相同）
—— 它是构建期才知道的事实，所以只能来自 sdkconfig + 档位名，不能来自镜像。
"""
import hashlib
import os
import re
import struct

# 镜像内偏移表：**照抄** backend/internal/firmwareid/identity.go（唯一真源）。
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
IMAGE_MAGIC = 0xE9
DESC_MAGIC = 0xABCD5432
# esp_app_desc_t 的最后一个字段 app_elf_sha256[32] 结束于 0xB0+0x20 = 0xD0，
# 也就是 Parse 需要的字节数（identity.go HeaderSize）。
HEADER_SIZE = 0xD0
PROJECT_COLLECTOR = "ehome_collector"

# chip_id -> 规范化芯片名（identity.go chipByID）。
CHIP_BY_ID = {0x0009: "esp32s3", 0x000D: "esp32c6"}
# 芯片名 -> 档位短名（firmwareid.BuildProfile 的 short）。
CHIP_SHORT = {"esp32c6": "c6", "esp32s3": "s3"}
# esp_image_header_t.spi_size 高半字节 -> MB（identity.go flashMBBySpiSize：
# 只接受 3/4；1/2/4MB 档位在本项目不存在，放进来只会多一条误刷通道）。
FLASH_MB_BY_SPI_SIZE = {3: 8, 4: 16}


class ImageError(Exception):
    """镜像头 / sdkconfig 读不出来。

    code 与后端 firmwareid 的机器码**逐字同名**（FW_NOT_IMAGE / FW_TRUNCATED /
    FW_PROJECT_MISMATCH / FW_CHIP_UNSUPPORTED / FW_FLASH_UNSUPPORTED），
    这样构建机的报错与上传时的 400 文案指同一件事，排障不必做两次翻译。
    sdkconfig 相关的失败在服务端没有对应码（服务端不读 sdkconfig），用 SDKCONFIG。
    """

    def __init__(self, code, detail):
        super().__init__("%s: %s" % (code, detail))
        self.code = code
        self.detail = detail


def c_string(buf):
    """C 字符串：NUL 截断 + strip（identity.go cString 同口径）。"""
    return buf.split(b"\x00", 1)[0].decode("utf-8", "replace").strip()


def parse_image_header(header, size_bytes):
    """解析镜像头。header 至少 HEADER_SIZE 字节；返回 identity dict。

    判定顺序与 identity.go Parse 一致（长度 -> magic -> 描述符 magic -> project
    -> chip -> 容量），这样「随便传个文件」得到的是「不是 ESP 镜像」而不是更具体的码。
    """
    if len(header) < HEADER_SIZE or size_bytes < HEADER_SIZE:
        raise ImageError("FW_TRUNCATED", "image is %d bytes, need at least %d"
                         % (min(len(header), size_bytes), HEADER_SIZE))
    if header[OFF_MAGIC] != IMAGE_MAGIC:
        raise ImageError("FW_NOT_IMAGE", "magic 0x%02x, want 0x%02x (not an ESP app image)"
                         % (header[OFF_MAGIC], IMAGE_MAGIC))
    desc_magic = struct.unpack_from("<I", header, OFF_DESC_MAGIC)[0]
    if desc_magic != DESC_MAGIC:
        raise ImageError("FW_TRUNCATED", "app descriptor magic 0x%08x, want 0x%08x"
                         % (desc_magic, DESC_MAGIC))
    project = c_string(header[OFF_PROJECT:OFF_PROJECT + 32])
    if project != PROJECT_COLLECTOR:
        raise ImageError("FW_PROJECT_MISMATCH", "project %r, want %r" % (project, PROJECT_COLLECTOR))
    chip_id = struct.unpack_from("<H", header, OFF_CHIP_ID)[0]
    chip = CHIP_BY_ID.get(chip_id)
    if chip is None:
        raise ImageError("FW_CHIP_UNSUPPORTED", "chip_id 0x%04x is not supported" % chip_id)
    spi_size = header[OFF_SPI_SIZE] >> 4
    flash_mb = FLASH_MB_BY_SPI_SIZE.get(spi_size)
    if flash_mb is None:
        raise ImageError("FW_FLASH_UNSUPPORTED", "spi_size 0x%x is not 3 (8MB) or 4 (16MB)" % spi_size)
    built_at = c_string(header[OFF_DATE:OFF_DATE + 16])
    clock = c_string(header[OFF_TIME:OFF_TIME + 16])
    if built_at and clock:
        built_at = built_at + " " + clock
    return {
        "chip_id": chip_id,
        "chip": chip,
        "flash_mb": flash_mb,
        "version": c_string(header[OFF_VERSION:OFF_VERSION + 32]),
        "project": project,
        "elf_sha256": header[OFF_ELF_SHA256:OFF_ELF_SHA256 + 32].hex(),
        "built_at": built_at,
        "idf_version": c_string(header[OFF_IDF_VER:OFF_IDF_VER + 32]),
        "size_bytes": size_bytes,
    }


def read_image_identity(path):
    """从镜像文件头解析身份（不读整文件）。任何不支持的情况都抛 ImageError。"""
    path = os.path.abspath(path)
    if not os.path.isfile(path):
        raise ImageError("FW_NOT_IMAGE", "image not found: %s" % path)
    size = os.path.getsize(path)
    if size < HEADER_SIZE:
        raise ImageError("FW_TRUNCATED", "image is %d bytes, need at least %d" % (size, HEADER_SIZE))
    with open(path, "rb") as fp:
        header = fp.read(HEADER_SIZE)
    return parse_image_header(header, size)


def read_sdkconfig(path):
    """读 kconfig 文本。返回 dict：键名 -> 字符串值（n/y/数字/字符串）或 None（未设置）。"""
    if not os.path.isfile(path):
        raise ImageError("SDKCONFIG", "sdkconfig not found: %s" % path)
    values = {}
    with open(path, "r", encoding="utf-8", errors="replace") as fp:
        for line in fp:
            line = line.strip()
            matched = re.match(r"^(CONFIG_[A-Za-z0-9_]+)=(.*)$", line)
            if matched:
                value = matched.group(2)
                if len(value) >= 2 and value.startswith('"') and value.endswith('"'):
                    value = value[1:-1]
                values[matched.group(1)] = value
                continue
            matched = re.match(r"^# (CONFIG_[A-Za-z0-9_]+) is not set$", line)
            if matched:
                values[matched.group(1)] = None
    return values


def cross_check_image_vs_config(identity, cfg):
    """镜像自证与 sdkconfig 必须一致；不一致说明 --bin 与 --sdkconfig 不是同一次构建。"""
    target = cfg.get("CONFIG_IDF_TARGET")
    if not target:
        raise ImageError("SDKCONFIG", "sdkconfig has no CONFIG_IDF_TARGET")
    if target != identity["chip"]:
        raise ImageError("SDKCONFIG", "sdkconfig CONFIG_IDF_TARGET=%r != image chip %r"
                         % (target, identity["chip"]))
    flash = cfg.get("CONFIG_ESPTOOLPY_FLASHSIZE")
    if not flash:
        raise ImageError("SDKCONFIG", "sdkconfig has no CONFIG_ESPTOOLPY_FLASHSIZE")
    matched = re.match(r"^(\d+)MB$", flash)
    if not matched:
        raise ImageError("SDKCONFIG", "CONFIG_ESPTOOLPY_FLASHSIZE=%r is not <N>MB" % flash)
    if int(matched.group(1)) != identity["flash_mb"]:
        raise ImageError("SDKCONFIG", "sdkconfig flash size %r != image flash_mb %d"
                         % (flash, identity["flash_mb"]))


def resolve_psram(cfg, override, chip):
    """PSRAM 位：镜像头证明不了，只能问 sdkconfig（可被 --psram 覆盖，但不许矛盾）。

    为什么要传 chip：C6 的 Kconfig 里**根本没有** CONFIG_SPIRAM 符号（该 SoC 无 PSRAM
    能力），所以「符号缺失」对 C6 是**正常的 false**，对 S3 才是异常（S3 一定有该符号，
    只是可能为 n）。不做这条区分，C6 会被误判成「解析不出来」。
    """
    raw = cfg.get("CONFIG_SPIRAM", "absent")
    if raw == "absent":
        if chip != "esp32c6":
            raise ImageError("SDKCONFIG", "sdkconfig has no CONFIG_SPIRAM for %s (S3 always "
                             "defines it; is this the right sdkconfig?)" % chip)
        from_cfg = False
    else:
        from_cfg = (raw == "y")
    from_cli = None if override is None else (override == "yes")
    if from_cli is not None and from_cli != from_cfg:
        raise ImageError("SDKCONFIG", "--psram %s contradicts sdkconfig CONFIG_SPIRAM=%s (chip=%s)"
                         % (override, "y" if from_cfg else ("n" if raw != "absent" else "absent"), chip))
    return from_cfg if from_cli is None else from_cli


def build_profile(chip, flash_mb, psram):
    """与 firmwareid.BuildProfile 同算法：c6/s3 [+p] + '-n<MB>'。"""
    short = CHIP_SHORT.get(chip)
    if not short or not flash_mb:
        return ""
    if psram:
        short += "p"
    return "%s-n%d" % (short, flash_mb)


def sha256_file(path):
    """整文件 sha256（与后端 fileSHA256 同口径）。"""
    hasher = hashlib.sha256()
    with open(path, "rb") as fp:
        for chunk in iter(lambda: fp.read(1 << 20), b""):
            hasher.update(chunk)
    return hasher.hexdigest()


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()

