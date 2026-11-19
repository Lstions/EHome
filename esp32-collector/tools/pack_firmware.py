#!/usr/bin/env python3
"""固件单文件包 `.ehfw` 的打包器与离线校验器。

规格真源：docs/计划/固件单文件包与构建打包规范-2026-11-19.md
  - §4   包格式 v1（两条目 / 扁平命名 / STORE / 1980 时间戳 / 上限）
  - §7.5 本工具的行为（复用 fwimage、确定性、fail-closed、自检）

## 为什么打包发生在构建机
包内只有镜像**说不了**的两件事（`psram` 与 `profile`）；其余身份（chip / flash_mb /
version / elf_sha256 / idf_ver）由服务端从镜像现读。所以打包器只做三件事：
把镜像放进去、把这两项写对、把两边对不上的一切拒掉。

## 确定性（AC-P11 / INV-6：清单是输入的纯函数）
  - 条目按名排序；`date_time=(1980,1,1,0,0,0)`；`ZIP_STORED`；
  - **必须 writestr(ZipInfo)，禁止 zf.write(path)**：后者从 os.stat 取
    external_attr / create_system，Linux 与 Windows 打出来的字节不同；
  - `external_attr = 0o644 << 16`（只写 0o644 会落进 DOS 属性位，unzip -l 显示错权限）；
  - 清单里不含时间戳、随机数、绝对路径（`build.*` 全部来自 git/镜像/sdkconfig）。
  ⇒ 同一输入连续打包两次，`.ehfw` 字节相同。

## fail-closed
先写 `<out>.tmp` → 立即 `--verify` 一遍（与服务端同规则）→ 通过才 `os.replace`。
任何一步失败都不留包：半份能上传的东西比没有更糟（上传时才 400，且更难查）。

## 用法
  # 打包（build_firmware.sh 的唯一调用方式）
  pack_firmware.py --bin build/c6-n8/ehome_collector.bin \
                   --sdkconfig build/c6-n8/sdkconfig \
                   --profile c6-n8 --version 2.9.0 \
                   --out dist/2.9.0/c6-n8/ehome_collector-2.9.0-c6-n8.ehfw

  # 离线校验（== 服务端规则；失败打印第一个错误并以 1 退出）
  pack_firmware.py --verify dist/2.9.0/c6-n8/ehome_collector-2.9.0-c6-n8.ehfw

## 版本校验只看镜像（评审 A-2）
旧 emit_meta.py 从 CMakeLists 正则抓 `PROJECT_VER` 字面量 —— 而那条字面量正是本次
改成 `-D FW_VERSION` 注入的东西，正则只会抓到 `${FW_VERSION}`。现在唯一的判据是
**镜像内 version[32] == --version**：对不上就是陈旧 .bin，重新构建。
"""
import argparse
import json
import os
import re
import subprocess
import sys
import zipfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import fwimage  # noqa: E402  (同目录共享库；脚本方式运行时 sys.path[0] 本来就是这里)

# ---- 包格式常量（§4.1 / §4.3；服务端 firmwarepkg 有同名常量，改动两侧同步）----
FORMAT_NAME = "ehome-firmware"
FORMAT_VERSION = 1
MANIFEST_ENTRY = "fw-manifest.json"
ARTIFACT_NAME = "ehome_collector.bin"  # §4.2 固定布局：包内只放这一个 artifact
PACKER_NAME = "pack_firmware.py"
PACKER_VERSION = "1.0.0"

# ---- 不变量与上限（§4.4 / §4.5）----
ENTRY_NAME_RE = re.compile(r"^[a-z0-9][a-z0-9._-]{0,63}$")  # INV-4：扁平、全小写
LIMIT_MAX_ENTRIES = 16
LIMIT_MAX_UNCOMPRESSED = 16 * 1024 * 1024  # 16 MiB
LIMIT_MAX_MANIFEST = 64 * 1024            # 64 KiB
LIMIT_MAX_RATIO = 100                     # 单条目 100:1
ALLOWED_COMPRESS = (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED)

# 固定时间戳：1980-01-01 00:00 是 ZIP 纪元，也是「这份产物不携带构建时刻」的声明。
ZIP_DATE_TIME = (1980, 1, 1, 0, 0, 0)
# 高 16 位是 Unix mode，低 16 位是 DOS 属性。**必须带 S_IFREG（0o100000）**：
# 只写 0o644 << 16 时文件类型位缺失，`zipinfo -l` 显示成 `?rw-r--r--`；
# 写 0o100644 << 16 才是 `-rw-r--r--`。（评审 A-13 定的是「写 Unix mode + 别落进
# DOS 位」，2026-11-19 复核时补上了 S_IFREG —— 与 vectors/gen_vectors.py 必须同值，
# 两边不一致会让「打包器 ↔ 向量」的交叉比对暴露出来。）
ZIP_ENTRY_ATTR = 0o100644 << 16
ZIP_CREATE_SYSTEM = 3  # 3 = Unix；显式写死，避免 Windows 上打出的字节不同

VERSION_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")


class PackError(Exception):
    """打包 / 校验失败。首段是机器码（FW_PKG_* / FW_*），与后端附录 A 同名。"""


# ============================================================================
# 打包
# ============================================================================
def _git(toplevel, *args):
    """在仓库根跑一条 git 命令；失败返回 None（构建目录可能是 tarball 解出来的）。"""
    try:
        out = subprocess.run(["git", "-C", toplevel] + list(args),
                             stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                             check=False)
    except OSError:
        return None
    if out.returncode != 0:
        return None
    return out.stdout.decode("utf-8", "replace").strip()


def collect_build_info(bin_path, sdkconfig_path, image):
    """§4.3 的 `build.*`：只做溯源，服务端不据此拒绝、也不入库（随 dist/ 归档）。

    刻意不含任何时间戳/绝对路径 —— 清单必须是输入的纯函数（INV-6），
    否则同一输入两次打包字节不同，AC-P11 立刻红。
    """
    info = {}
    toplevel = _git(os.path.dirname(os.path.abspath(bin_path)), "rev-parse", "--show-toplevel")
    if toplevel:
        commit = _git(toplevel, "rev-parse", "HEAD")
        branch = _git(toplevel, "rev-parse", "--abbrev-ref", "HEAD")
        # --untracked-files=no：未跟踪文件（dist/、构建日志）不代表源码被改过。
        dirty = _git(toplevel, "status", "--porcelain", "--untracked-files=no")
        tag = _git(toplevel, "describe", "--tags", "--exact-match")
        if commit:
            info["source_commit"] = commit
        if branch:
            info["source_branch"] = branch
        if dirty is not None:
            info["source_dirty"] = bool(dirty)
        if tag:
            info["git_tag"] = tag
    # idf_version 取**镜像内**的值：这是「这份二进制由哪版 IDF 编出来」的自证，
    # 比打包机当时的 IDF_PATH 更可信，也让 `--bin` 单独可用（RK-4）。
    if image.get("idf_version"):
        info["idf_version"] = image["idf_version"]
    info["sdkconfig_sha256"] = fwimage.sha256_file(sdkconfig_path)
    return info


def build_manifest(image, psram, profile, build_info):
    """拼清单。字段顺序与 §4.3 的例子一致（人读 diff 时不必重新对齐）。

    **清单里没有 version**：版本在镜像里（version[32]），服务端从镜像读；
    再写一遍就是双真源（§1.2 批判过的那件事）。
    """
    return {
        "format": FORMAT_NAME,
        "format_version": FORMAT_VERSION,
        "packer": {"name": PACKER_NAME, "version": PACKER_VERSION},
        "artifact": {
            "name": ARTIFACT_NAME,
            "size": image["size_bytes"],
            "sha256": image["sha256"],
        },
        "identity": {
            # psram 是**唯一**无法从镜像导出的位；profile 编码 (chip, flash, psram)，
            # 服务端用它与镜像交叉核对（INV-3）。
            "psram": psram,
            "profile": profile,
        },
        "build": build_info,
    }


def _zip_write_entry(zf, name, data):
    """写一个条目。**不要**换成 zf.write(path)（见文件头「确定性」）。"""
    info = zipfile.ZipInfo(name, date_time=ZIP_DATE_TIME)
    info.compress_type = zipfile.ZIP_STORED
    info.create_system = ZIP_CREATE_SYSTEM
    info.external_attr = ZIP_ENTRY_ATTR
    zf.writestr(info, data)


def write_package(out_path, manifest, artifact_bytes):
    """写包（临时文件）。条目按名排序 —— 排序本身就是确定性的一部分。"""
    out_path = os.path.abspath(out_path)
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    manifest_bytes = (json.dumps(manifest, indent=2, ensure_ascii=False) + "\n").encode("utf-8")
    entries = sorted([(MANIFEST_ENTRY, manifest_bytes),
                      (manifest["artifact"]["name"], artifact_bytes)])
    tmp_path = out_path + ".tmp"
    try:
        with zipfile.ZipFile(tmp_path, "w", compression=zipfile.ZIP_STORED,
                             allowZip64=False) as zf:  # §4.1：PKZIP 2.0，不启用 ZIP64
            for name, data in entries:
                _zip_write_entry(zf, name, data)
    except (OSError, zipfile.BadZipFile) as exc:
        _unlink_quiet(tmp_path)
        raise PackError("FW_PKG_INVALID: write %s: %s" % (tmp_path, exc))
    return tmp_path


def _unlink_quiet(path):
    try:
        os.unlink(path)
    except OSError:
        pass


def pack_firmware(bin_path, sdkconfig_path, profile, version, psram_override, out_path):
    """打包 + 自检。返回 (manifest, verify_summary)。任一校验失败都不留包。"""
    profile = (profile or "").strip().lower()
    version = (version or "").strip()
    if not VERSION_RE.match(version):
        raise PackError("version %r is not X.Y.Z (0.0.0-dev is idf.py's default and is not "
                        "releasable; pass --version from build_firmware.sh)" % version)
    if not ENTRY_NAME_RE.match(profile):
        raise PackError("--profile %r is not a valid build profile name (e.g. c6-n8)" % profile)

    image = fwimage.read_image_identity(bin_path)
    if image["version"] != version:
        raise PackError("stale .bin: image version %r != --version %r "
                        "(the .bin was built for another version; rebuild before packing)"
                        % (image["version"], version))
    cfg = fwimage.read_sdkconfig(sdkconfig_path)
    fwimage.cross_check_image_vs_config(image, cfg)
    psram = fwimage.resolve_psram(cfg, psram_override, image["chip"])
    implied = fwimage.build_profile(image["chip"], image["flash_mb"], psram)
    if implied != profile:
        raise PackError("--profile %r != profile implied by image+sdkconfig %r "
                        "(chip=%s flash_mb=%d psram=%s)"
                        % (profile, implied, image["chip"], image["flash_mb"],
                           "yes" if psram else "no"))

    with open(bin_path, "rb") as fp:
        artifact_bytes = fp.read()
    if len(artifact_bytes) != image["size_bytes"]:
        raise PackError("FW_TRUNCATED: read %d bytes of %r, stat said %d"
                        % (len(artifact_bytes), bin_path, image["size_bytes"]))
    image["sha256"] = fwimage.sha256_bytes(artifact_bytes)

    build_info = collect_build_info(bin_path, sdkconfig_path, image)
    manifest = build_manifest(image, psram, implied, build_info)

    tmp_path = write_package(out_path, manifest, artifact_bytes)
    try:
        summary = verify_package(tmp_path)  # §7.5 自检：与服务端同规则
    except PackError:
        _unlink_quiet(tmp_path)
        raise
    except (fwimage.ImageError, OSError) as exc:
        _unlink_quiet(tmp_path)
        raise PackError("FW_PKG_INVALID: self-check %s: %s" % (tmp_path, exc))
    try:
        os.replace(tmp_path, os.path.abspath(out_path))
    except OSError as exc:
        _unlink_quiet(tmp_path)
        raise PackError("FW_PKG_INVALID: replace %s: %s" % (out_path, exc))
    return manifest, summary


# ============================================================================
# 离线校验（== 服务端 firmwarepkg.Open + 摘要/身份核对）
# ============================================================================
def _check_manifest(manifest):
    """清单结构与必需字段（§4.3）。未知键忽略（§4.6：长期格式允许加可选字段）。"""
    if not isinstance(manifest, dict):
        raise PackError("FW_PKG_INVALID: manifest is %s, want object" % type(manifest).__name__)
    if manifest.get("format") != FORMAT_NAME:
        raise PackError("FW_PKG_UNSUPPORTED: format %r, want %r"
                        % (manifest.get("format"), FORMAT_NAME))
    format_version = manifest.get("format_version")
    if isinstance(format_version, bool) or not isinstance(format_version, int):
        raise PackError("FW_PKG_INVALID: format_version %r is not an integer" % (format_version,))
    if format_version != FORMAT_VERSION:
        raise PackError("FW_PKG_UNSUPPORTED: format_version %d, this verifier speaks %d"
                        % (format_version, FORMAT_VERSION))

    def _field(obj, key, where):
        if not isinstance(obj, dict) or key not in obj:
            path = "manifest.%s.%s" % (where, key) if where else "manifest.%s" % key
            raise PackError("FW_PKG_INVALID: %s is missing" % path)
        return obj[key]

    packer = _field(manifest, "packer", "")
    for key in ("name", "version"):
        value = _field(packer, key, "packer")
        if not isinstance(value, str) or not value.strip():
            raise PackError("FW_PKG_INVALID: manifest.packer.%s is not a non-empty string" % key)

    artifact = _field(manifest, "artifact", "")
    name = _field(artifact, "name", "artifact")
    if not isinstance(name, str) or not name.endswith(".bin"):
        raise PackError("FW_PKG_INVALID: manifest.artifact.name %r must end with .bin" % (name,))
    if not ENTRY_NAME_RE.match(name):
        raise PackError("FW_PKG_INVALID: manifest.artifact.name %r violates INV-4" % (name,))
    size = _field(artifact, "size", "artifact")
    if isinstance(size, bool) or not isinstance(size, int) or size <= 0:
        raise PackError("FW_PKG_INVALID: manifest.artifact.size %r is not a positive integer" % (size,))
    digest = _field(artifact, "sha256", "artifact")
    if not isinstance(digest, str) or not SHA256_RE.match(digest):
        raise PackError("FW_PKG_INVALID: manifest.artifact.sha256 %r is not 64 lowercase hex" % (digest,))

    identity = _field(manifest, "identity", "")
    psram = _field(identity, "psram", "identity")
    if not isinstance(psram, bool):
        raise PackError("FW_PKG_INVALID: manifest.identity.psram %r is not a boolean" % (psram,))
    profile = _field(identity, "profile", "identity")
    if not isinstance(profile, str) or not ENTRY_NAME_RE.match(profile):
        raise PackError("FW_PKG_INVALID: manifest.identity.profile %r is not a profile name" % (profile,))


def _entry_exceeds_ratio(info):
    """压缩比 > 100:1。用整数比较（file_size > 100 * compress_size），不用浮点。"""
    if info.file_size == 0:
        return False
    if info.compress_size <= 0:
        return True
    return info.file_size > LIMIT_MAX_RATIO * info.compress_size


def verify_package(pkg_path):
    """离线校验一个 .ehfw。返回摘要 dict；失败抛 PackError（首段是机器码）。"""
    pkg_path = os.path.abspath(pkg_path)
    if not os.path.isfile(pkg_path):
        raise PackError("FW_PKG_INVALID: package not found: %s" % pkg_path)
    with open(pkg_path, "rb") as fp:
        magic = fp.read(4)
    if magic != b"PK\x03\x04":
        raise PackError("FW_PKG_REQUIRED: first 4 bytes are %s, want 504b0304 — this is not a "
                        "firmware package (a raw .bin?); pack it with pack_firmware.py"
                        % magic.hex())
    try:
        zf = zipfile.ZipFile(pkg_path, "r")
    except (zipfile.BadZipFile, OSError) as exc:
        raise PackError("FW_PKG_INVALID: open zip %s: %s" % (pkg_path, exc))
    with zf:
        infos = zf.infolist()
        if len(infos) > LIMIT_MAX_ENTRIES:
            raise PackError("FW_PKG_TOO_LARGE: %d entries, limit %d"
                            % (len(infos), LIMIT_MAX_ENTRIES))
        # INV-1：恰好两条。先去重再判数目，避免「重复条目 + 缺条目」被算成 2。
        seen = {}
        for info in infos:
            name = info.filename
            if name in seen:
                raise PackError("FW_PKG_INVALID: duplicate entry %r" % name)
            if not ENTRY_NAME_RE.match(name):
                raise PackError("FW_PKG_INVALID: entry name %r violates INV-4 "
                                r"^[a-z0-9][a-z0-9._-]{0,63}$ (no directories, no uppercase)"
                                % name)
            seen[name] = info
        if len(infos) != 2:
            raise PackError("FW_PKG_INVALID: %d entries %r, want exactly 2 (%s + artifact)"
                            % (len(infos), sorted(seen), MANIFEST_ENTRY))
        if MANIFEST_ENTRY not in seen:
            raise PackError("FW_PKG_INVALID: manifest entry %r is missing" % MANIFEST_ENTRY)

        # 上限全部在**解压前**用中央目录的 size 字段判定（§4.5）。
        total = 0
        for info in infos:
            if info.compress_type not in ALLOWED_COMPRESS:
                raise PackError("FW_PKG_INVALID: entry %r uses compression method %d "
                                "(only 0=STORE / 8=DEFLATE)" % (info.filename, info.compress_type))
            if _entry_exceeds_ratio(info):
                raise PackError("FW_PKG_TOO_LARGE: entry %r ratio %d B -> %d B exceeds %d:1"
                                % (info.filename, info.compress_size, info.file_size,
                                   LIMIT_MAX_RATIO))
            total += info.file_size
        if total > LIMIT_MAX_UNCOMPRESSED:
            raise PackError("FW_PKG_TOO_LARGE: uncompressed total %d B exceeds %d B"
                            % (total, LIMIT_MAX_UNCOMPRESSED))
        manifest_info = seen[MANIFEST_ENTRY]
        if manifest_info.file_size > LIMIT_MAX_MANIFEST:
            raise PackError("FW_PKG_TOO_LARGE: manifest %d B exceeds %d B"
                            % (manifest_info.file_size, LIMIT_MAX_MANIFEST))

        try:
            raw = zf.read(MANIFEST_ENTRY)
        except (zipfile.BadZipFile, OSError) as exc:
            raise PackError("FW_PKG_INVALID: read manifest: %s" % exc)
        try:
            manifest = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, ValueError) as exc:
            raise PackError("FW_PKG_INVALID: manifest is not valid UTF-8 JSON: %s" % exc)
        _check_manifest(manifest)

        artifact_name = manifest["artifact"]["name"]
        if artifact_name not in seen:
            raise PackError("FW_PKG_INVALID: manifest.artifact.name %r has no matching entry "
                            "(entries: %r)" % (artifact_name, sorted(seen)))
        try:
            artifact_bytes = zf.read(artifact_name)
        except (zipfile.BadZipFile, OSError) as exc:
            raise PackError("FW_PKG_INVALID: read artifact: %s" % exc)

    size = len(artifact_bytes)
    digest = fwimage.sha256_bytes(artifact_bytes)
    # INV-2：摘要/大小与实测一致。这是唯一「handler 才算」的不变量（评审 B-2），
    # 离线工具在这里一并做掉，等价于服务端第 5 步。
    if size != manifest["artifact"]["size"]:
        raise PackError("FW_PKG_DIGEST_MISMATCH: artifact size %d B != manifest %d B"
                        % (size, manifest["artifact"]["size"]))
    if digest != manifest["artifact"]["sha256"]:
        raise PackError("FW_PKG_DIGEST_MISMATCH: artifact sha256 %s != manifest %s"
                        % (digest, manifest["artifact"]["sha256"]))
    try:
        image = fwimage.parse_image_header(artifact_bytes[:fwimage.HEADER_SIZE], size)
    except fwimage.ImageError as exc:
        raise PackError("%s (artifact %r is not a usable ESP app image)" % (exc, artifact_name))
    # INV-3：profile 必须能被（镜像 chip + 镜像 flash_mb + 清单 psram）还原出来。
    implied = fwimage.build_profile(image["chip"], image["flash_mb"], manifest["identity"]["psram"])
    if implied != manifest["identity"]["profile"]:
        raise PackError("FW_PKG_CONFLICT: manifest.identity.profile %r but image+manifest "
                        "imply %r (image chip=%s flash_mb=%d psram=%s)"
                        % (manifest["identity"]["profile"], implied, image["chip"],
                           image["flash_mb"], "yes" if manifest["identity"]["psram"] else "no"))
    return {
        "path": pkg_path,
        "entries": sorted(seen),
        "manifest_size": manifest_info.file_size,
        "format_version": manifest["format_version"],
        "artifact_name": artifact_name,
        "artifact_size": size,
        "artifact_sha256": digest,
        "chip": image["chip"],
        "flash_mb": image["flash_mb"],
        "version": image["version"],
        "profile": manifest["identity"]["profile"],
        "psram": manifest["identity"]["psram"],
        "package_sha256": fwimage.sha256_file(pkg_path),
    }


# ============================================================================
# CLI
# ============================================================================
def _format_summary(summary):
    return ("entries=%s artifact=%s %d B sha256=%s chip=%s flash=%dMB version=%s "
            "profile=%s psram=%s"
            % (",".join(summary["entries"]), summary["artifact_name"],
               summary["artifact_size"], summary["artifact_sha256"][:16],
               summary["chip"], summary["flash_mb"], summary["version"],
               summary["profile"], "yes" if summary["psram"] else "no"))


def main(argv):
    parser = argparse.ArgumentParser(
        description="pack / verify an EHome firmware package (.ehfw)",
        formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--bin", default="", help="path to ehome_collector.bin (app image only)")
    parser.add_argument("--sdkconfig", default="", help="path to the sdkconfig that built that bin")
    parser.add_argument("--profile", default="", help="build profile (e.g. c6-n8); must match image")
    parser.add_argument("--version", default="", help="X.Y.Z; must equal the version inside the image")
    parser.add_argument("--out", default="", help="output .ehfw path")
    parser.add_argument("--psram", choices=["yes", "no"], default=None,
                        help="override the PSRAM bit; must agree with sdkconfig/profile")
    parser.add_argument("--verify", default="", metavar="PKG",
                        help="offline-verify an existing .ehfw (same rules as the server)")
    args = parser.parse_args(argv)

    try:
        if args.verify:
            extra = [name for name in ("bin", "sdkconfig", "profile", "version", "out", "psram")
                     if getattr(args, name)]
            if extra:
                raise PackError("FW_PKG_INVALID: --verify is exclusive, but got %s"
                                % ", ".join("--" + name for name in extra))
            summary = verify_package(args.verify)
            print("OK %s: %s" % (os.path.relpath(summary["path"]), _format_summary(summary)))
            return 0

        missing = [name for name in ("bin", "sdkconfig", "profile", "version", "out")
                   if not getattr(args, name).strip()]
        if missing:
            raise PackError("missing required argument(s): %s"
                            % ", ".join("--" + name for name in missing))
        manifest, summary = pack_firmware(args.bin, args.sdkconfig, args.profile, args.version,
                                          args.psram, args.out)
    except (PackError, fwimage.ImageError) as exc:
        print("ERROR: pack_firmware: %s" % exc, file=sys.stderr)
        return 1

    print("wrote %s" % os.path.abspath(args.out))
    print("  artifact: %s %d B sha256=%s"
          % (manifest["artifact"]["name"], manifest["artifact"]["size"],
             manifest["artifact"]["sha256"]))
    print("  identity: profile=%s psram=%s  (chip=%s flash_mb=%d version=%s)  build=%s"
          % (manifest["identity"]["profile"],
             "yes" if manifest["identity"]["psram"] else "no",
             summary["chip"], summary["flash_mb"], summary["version"],
             ",".join(sorted(manifest["build"]))))
    print("  self-check: %s" % _format_summary(summary))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
