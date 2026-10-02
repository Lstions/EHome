#!/usr/bin/env python3
# -*- coding: utf-8 -*-
r"""EHome 采集器固件烧录工具（ESP32-C6 / ESP32-S3，Windows + PowerShell）。

用途
----
把 esp32-collector/build/<profile>/ 下的固件烧到开发板，并在烧录前后做
"防刷错"校验：芯片型号必须与 profile 匹配、flash 物理容量必须与 n8/n16 匹配，
不匹配就拒绝烧录（避免把 16MB 的分区表刷进 8MB 的板子，或者把 C6 的固件刷进 S3）。

典型用法
--------
    # 1) 只看将要执行什么，不碰串口
    python esp32-collector/tools/flash_firmware.py --profile c6-n8 --dry-run

    # 2) 烧录 + 抓 20 秒启动日志（自动探测串口）
    python esp32-collector/tools/flash_firmware.py --profile c6-n8 --monitor 20

    # 3) 显式指定串口、擦除整片 flash 后再烧
    python esp32-collector/tools/flash_firmware.py --profile s3-n16 --port COM5 --erase

设计要点
--------
* 不依赖 PATH 里的 esptool：始终用"选定的 python 解释器 + -m esptool"调用。
  解释器优先级：$env:IDF_PYTHON_ENV_PATH -> C:\Espressif\tools\python\v6.1\venv
  -> sys.executable；因此本脚本可以用 Windows 原生 python.exe 直接运行。
* 唯一使用的第三方库是 pyserial（仅 --monitor / 串口枚举需要），而且只在
  选定解释器的子进程里使用，本脚本自身不 import 它。
* 镜像清单优先取自 build/<profile>/flash_args（IDF 生成），否则用标准兜底表。

退出码
------
    0  成功
    1  运行期错误（esptool 执行失败、串口打开失败等）
    2  参数或校验错误（profile 非法、镜像缺失、芯片/容量不匹配、串口多义）
    3  --monitor 判定 BOOT_FAIL（有启动横幅但疑似持续重启，或完全没有启动日志）
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path

# --------------------------------------------------------------------------
# 常量
# --------------------------------------------------------------------------

SCRIPT_PATH = Path(__file__).resolve()
PROJECT_DIR = SCRIPT_PATH.parent.parent              # esp32-collector/
DEFAULT_BUILD_ROOT = PROJECT_DIR / "build"

# IDF 6.1 的 venv（esptool + pyserial 都在里面）
DEFAULT_IDF_PYTHON = Path(r"C:\Espressif\tools\python\v6.1\venv\Scripts\python.exe")

ESPRESSIF_VID = 0x303A      # ESP32 USB-Serial-JTAG / USB-CDC
CP210X_VID = 0x10C4         # Silicon Labs CP210x

DEFAULT_BAUD = 460800

MB = 1024 * 1024

# flash_args 缺失时的兜底镜像表：(地址, 相对 build/<profile>/ 的路径)
FALLBACK_IMAGES = (
    (0x0, "bootloader/bootloader.bin"),
    (0x8000, "partition_table/partition-table.bin"),
    (0xD000, "ota_data_initial.bin"),
    (0x10000, "ehome_collector.bin"),
)


@dataclass(frozen=True)
class Profile:
    name: str
    target: str          # esptool 的 --chip 取值：esp32c6 / esp32s3
    chip_name: str       # esptool 打印的芯片名：ESP32-C6 / ESP32-S3
    flash_size: str      # esptool 的 --flash-size 取值：8MB / 16MB
    flash_bytes: int     # 期望的物理 flash 容量（字节）


PROFILES = {
    "c6-n8": Profile("c6-n8", "esp32c6", "ESP32-C6", "8MB", 8 * MB),
    "c6-n16": Profile("c6-n16", "esp32c6", "ESP32-C6", "16MB", 16 * MB),
    "s3-n8": Profile("s3-n8", "esp32s3", "ESP32-S3", "8MB", 8 * MB),
    "s3-n16": Profile("s3-n16", "esp32s3", "ESP32-S3", "16MB", 16 * MB),
}

PROFILE_LIST = "|".join(PROFILES)


# --------------------------------------------------------------------------
# 中文输出 + 编码
# --------------------------------------------------------------------------

def setup_stdio() -> None:
    """强制 UTF-8 输出。

    Windows 上 Python 的 stdout 默认跟随 ANSI 代码页（本机 cp936），直接
    重定向到文件或管道时会写出 GBK 字节，日志/证据里就是乱码。这里强制
    UTF-8；写真实控制台时 CPython 内部仍走 WriteConsoleW，不受影响。
    """
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")  # type: ignore[union-attr]
        except Exception:
            pass


def info(msg: str = "") -> None:
    print(msg, flush=True)


def warn(msg: str) -> None:
    print("警告: " + msg, flush=True)


def fail(msg: str, code: int = 1) -> int:
    print("错误: " + msg, file=sys.stderr, flush=True)
    return code


class ToolError(Exception):
    """带退出码的中文错误。"""

    def __init__(self, message: str, code: int = 2) -> None:
        super().__init__(message)
        self.code = code


def human_size(num: int) -> str:
    if num >= MB:
        return "%.1f MiB" % (num / MB)
    if num >= 1024:
        return "%.1f KiB" % (num / 1024)
    return "%d B" % num


# --------------------------------------------------------------------------
# 解释器选择（优先 IDF venv，不依赖 PATH 里的 esptool）
# --------------------------------------------------------------------------

@dataclass
class PythonPick:
    path: str
    source: str
    has_esptool: bool
    has_pyserial: bool


_MODULE_PROBE_CODE = (
    "import json\n"
    "result = {}\n"
    "for name in ('esptool', 'serial'):\n"
    "    try:\n"
    "        __import__(name)\n"
    "        result[name] = True\n"
    "    except Exception:\n"
    "        result[name] = False\n"
    "print(json.dumps(result))\n"
)

_PORT_PROBE_CODE = (
    "import json\n"
    "try:\n"
    "    from serial.tools import list_ports\n"
    "except Exception as exc:\n"
    "    print(json.dumps({'error': str(exc)}))\n"
    "    raise SystemExit(0)\n"
    "ports = []\n"
    "for p in list_ports.comports():\n"
    "    ports.append({\n"
    "        'device': p.device, 'vid': p.vid, 'pid': p.pid,\n"
    "        'description': p.description or '',\n"
    "        'manufacturer': p.manufacturer or '',\n"
    "        'serial_number': p.serial_number or '',\n"
    "        'hwid': p.hwid or '',\n"
    "    })\n"
    "print(json.dumps({'ports': ports}))\n"
)

_module_cache: dict[str, tuple[bool, bool]] = {}


def child_env() -> dict[str, str]:
    """子进程环境：强制 UTF-8，避免中文/路径在 GBK 代码页下乱码。"""
    env = dict(os.environ)
    env["PYTHONIOENCODING"] = "utf-8"
    env["PYTHONUTF8"] = "1"
    return env


def probe_modules(python_path: str) -> tuple[bool, bool]:
    """返回 (有 esptool, 有 pyserial)；探测失败按"没有"处理。"""
    if python_path in _module_cache:
        return _module_cache[python_path]
    has_esptool = has_pyserial = False
    if os.path.exists(python_path) or os.path.sep not in python_path:
        try:
            cp = subprocess.run(
                [python_path, "-c", _MODULE_PROBE_CODE],
                capture_output=True, text=True, encoding="utf-8",
                errors="replace", timeout=60, env=child_env(),
            )
            if cp.returncode == 0 and cp.stdout.strip():
                data = json.loads(cp.stdout.strip().splitlines()[-1])
                has_esptool = bool(data.get("esptool"))
                has_pyserial = bool(data.get("serial"))
        except Exception:
            pass
    _module_cache[python_path] = (has_esptool, has_pyserial)
    return _module_cache[python_path]


def candidate_pythons() -> list[tuple[str, str]]:
    """按优先级返回候选解释器 (路径, 来源说明)。"""
    cands: list[tuple[str, str]] = []

    env_path = os.environ.get("IDF_PYTHON_ENV_PATH", "").strip().strip('"')
    if env_path:
        base = Path(env_path)
        for rel in (Path("Scripts") / "python.exe", Path("bin") / "python",
                    Path("python.exe")):
            cand = base / rel
            if cand.is_file():
                cands.append((str(cand), "$env:IDF_PYTHON_ENV_PATH"))
                break
        else:
            warn("$env:IDF_PYTHON_ENV_PATH=%s 下没有找到 python，忽略该候选" % env_path)

    if DEFAULT_IDF_PYTHON.is_file():
        cands.append((str(DEFAULT_IDF_PYTHON), "IDF 6.1 默认 venv"))

    cands.append((sys.executable, "当前解释器 sys.executable"))

    # 去重且保持顺序
    seen: set[str] = set()
    unique: list[tuple[str, str]] = []
    for path, source in cands:
        key = os.path.normcase(os.path.abspath(path))
        if key in seen:
            continue
        seen.add(key)
        unique.append((path, source))
    return unique


def resolve_python(require_esptool: bool) -> PythonPick:
    """选定用于 -m esptool / pyserial 的解释器。"""
    candidates = candidate_pythons()
    fallback: PythonPick | None = None
    for path, source in candidates:
        has_esptool, has_pyserial = probe_modules(path)
        pick = PythonPick(path, source, has_esptool, has_pyserial)
        if fallback is None:
            fallback = pick
        if has_esptool:
            return pick
    assert fallback is not None
    if require_esptool:
        tried = "\n".join("    - %s (%s)" % (p, s) for p, s in candidates)
        raise ToolError(
            "找不到装了 esptool 的 python 解释器，已尝试：\n%s\n"
            "请安装 esptool，或设置 IDF_PYTHON_ENV_PATH 指向 IDF 的 python 环境。"
            % tried, 2,
        )
    return fallback


# --------------------------------------------------------------------------
# 串口探测
# --------------------------------------------------------------------------

def probe_ports(python_path: str) -> list[dict]:
    """通过选定解释器的 pyserial 枚举串口；失败返回空列表。"""
    try:
        cp = subprocess.run(
            [python_path, "-c", _PORT_PROBE_CODE],
            capture_output=True, text=True, encoding="utf-8",
            errors="replace", timeout=60, env=child_env(),
        )
    except Exception as exc:
        warn("枚举串口失败（%s）" % exc)
        return []
    out = (cp.stdout or "").strip()
    if not out:
        return []
    try:
        data = json.loads(out.splitlines()[-1])
    except Exception:
        return []
    if "error" in data:
        warn("pyserial 不可用，无法自动枚举串口：%s" % data["error"])
        return []
    ports = data.get("ports") or []
    return [p for p in ports if isinstance(p, dict)]


def port_rank(port: dict) -> tuple[int, str]:
    vid = port.get("vid")
    if vid == ESPRESSIF_VID:
        return 0, "Espressif USB-Serial-JTAG (VID_303A)"
    if vid == CP210X_VID:
        return 1, "Silicon Labs CP210x (VID_10C4)"
    return 9, "其它 USB 串口"


def describe_port(port: dict) -> str:
    vid = port.get("vid")
    pid = port.get("pid")
    vidpid = "VID_%04X&PID_%04X" % (vid, pid) if vid is not None else "-"
    return "%-8s %-18s %s" % (port.get("device", "?"), vidpid,
                              (port.get("description") or "").strip())


def resolve_port(explicit: str | None, python_path: str,
                 dry_run: bool) -> tuple[str, list[str]]:
    """决定要用的串口，返回 (端口, 额外打印的说明行)。"""
    if explicit:
        return explicit, []

    notes: list[str] = []
    ports = probe_ports(python_path)
    if not ports:
        msg = ("没有探测到任何串口。请插好开发板后重试，或用 --port COMx 显式指定。")
        if dry_run:
            notes.append("警告: " + msg + "（dry-run 不连接串口，继续）")
            return "<PORT>", notes
        raise ToolError(msg, 2)

    ranked = sorted(ports, key=lambda p: port_rank(p)[0])
    targets = [p for p in ranked if port_rank(p)[0] < 9]

    lines = ["探测到的串口："]
    for p in ranked:
        mark = "  <- 候选" if port_rank(p)[0] < 9 else ""
        lines.append("    " + describe_port(p) + mark)
    notes.extend(lines)

    if len(targets) == 1:
        chosen = targets[0].get("device", "")
        notes.append("自动选择串口：%s（%s）" % (chosen, port_rank(targets[0])[1]))
        return chosen, notes

    if len(targets) > 1:
        head = ("探测到多个候选串口，无法判断该用哪一个：\n"
                + "\n".join("    " + describe_port(p) for p in targets))
        if dry_run:
            notes.append("警告: " + head + "\n（dry-run 不连接串口，继续；真实烧录请显式指定 --port）")
            return "<PORT>", notes
        raise ToolError(head + "\n请用 --port COMx 显式指定要烧录的串口。", 2)

    msg = ("没有找到 Espressif(VID_303A) 或 CP210x(VID_10C4) 的串口。")
    if dry_run:
        notes.append("警告: " + msg + "（dry-run 不连接串口，继续）")
        return "<PORT>", notes
    raise ToolError(msg + "请确认开发板已连接，或用 --port COMx 显式指定。", 2)


# --------------------------------------------------------------------------
# 镜像清单
# --------------------------------------------------------------------------

@dataclass
class ImageEntry:
    addr: int
    rel: str
    path: Path
    exists: bool
    size: int = 0


@dataclass
class Manifest:
    entries: list[ImageEntry]
    source: str
    flash_mode: str | None = None
    flash_freq: str | None = None
    flash_size: str | None = None


def parse_flash_args(path: Path) -> Manifest:
    """解析 IDF 生成的 flash_args。

    形如：
        --flash-mode dio --flash-freq 80m --flash-size 16MB
        0x0 bootloader/bootloader.bin
        0x8000 partition_table/partition-table.bin
    """
    entries: list[ImageEntry] = []
    mode = freq = size = None
    for raw in path.read_text(encoding="utf-8", errors="replace").splitlines():
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        tokens = line.split()
        if line.startswith("-"):
            i = 0
            while i < len(tokens):
                tok = tokens[i]
                if tok == "--flash-mode" and i + 1 < len(tokens):
                    mode = tokens[i + 1]; i += 2
                elif tok == "--flash-freq" and i + 1 < len(tokens):
                    freq = tokens[i + 1]; i += 2
                elif tok == "--flash-size" and i + 1 < len(tokens):
                    size = tokens[i + 1]; i += 2
                else:
                    i += 1
            continue
        if len(tokens) < 2:
            continue
        try:
            addr = int(tokens[0], 0)
        except ValueError:
            continue
        rel = tokens[1]
        full = (path.parent / rel).resolve()
        info_stat = full.stat() if full.is_file() else None
        entries.append(ImageEntry(
            addr=addr, rel=rel, path=full,
            exists=info_stat is not None,
            size=info_stat.st_size if info_stat else 0,
        ))
    return Manifest(entries=entries, source=str(path), flash_mode=mode,
                    flash_freq=freq, flash_size=size)


def fallback_manifest(build_dir: Path) -> Manifest:
    entries: list[ImageEntry] = []
    for addr, rel in FALLBACK_IMAGES:
        full = (build_dir / rel).resolve()
        st = full.stat() if full.is_file() else None
        entries.append(ImageEntry(addr=addr, rel=rel, path=full,
                                  exists=st is not None,
                                  size=st.st_size if st else 0))
    return Manifest(entries=entries, source="内置兜底清单（flash_args 不存在）")


def load_manifest(build_dir: Path) -> Manifest:
    flash_args = build_dir / "flash_args"
    if flash_args.is_file():
        manifest = parse_flash_args(flash_args)
        if manifest.entries:
            return manifest
        warn("%s 存在但没有解析出任何镜像，改用内置兜底清单" % flash_args)
    return fallback_manifest(build_dir)


# --------------------------------------------------------------------------
# 命令行
# --------------------------------------------------------------------------

class ChineseArgumentParser(argparse.ArgumentParser):
    def error(self, message: str):  # noqa: D102 - 保持 argparse 的行为，只是换成中文
        self.print_usage(sys.stderr)
        print("错误: 参数不合法：%s" % message, file=sys.stderr, flush=True)
        print("提示: 用 --help 查看完整用法。", file=sys.stderr, flush=True)
        raise SystemExit(2)


def baud_type(value: str) -> int:
    try:
        baud = int(value)
    except ValueError:
        raise argparse.ArgumentTypeError("波特率必须是整数（例如 460800），收到 %r" % value)
    if baud <= 0:
        raise argparse.ArgumentTypeError("波特率必须大于 0，收到 %d" % baud)
    return baud


def monitor_type(value: str) -> int:
    try:
        secs = int(value)
    except ValueError:
        raise argparse.ArgumentTypeError("监控秒数必须是整数（例如 20），收到 %r" % value)
    if secs <= 0:
        raise argparse.ArgumentTypeError("监控秒数必须大于 0，收到 %d" % secs)
    return secs


def build_parser() -> argparse.ArgumentParser:
    epilog = """\
示例
----
  # 只看命令与镜像清单，不碰串口
  python esp32-collector/tools/flash_firmware.py --profile c6-n8 --dry-run

  # 烧录并抓 20 秒启动日志（串口自动探测；探测到多个候选时会要求显式 --port）
  python esp32-collector/tools/flash_firmware.py --profile c6-n8 --port COM9 --monitor 20

  # 先整片擦除再烧（默认不擦）
  python esp32-collector/tools/flash_firmware.py --profile s3-n16 --port COM5 --erase

规则
----
  * profile 决定目标芯片与 flash 容量；烧录前会用 esptool 实测芯片型号与
    物理 flash 容量，不匹配就拒绝烧录（防刷错变砖）。
  * 镜像清单优先取 build/<profile>/flash_args；文件缺失时会列出缺哪个，
    并提示先运行 ./build_firmware.sh <profile>。
  * 串口省略时自动探测：优先 Espressif VID_303A（USB-Serial-JTAG），
    其次 CP210x VID_10C4；有多个候选时打印候选并要求显式指定 --port。
  * --monitor 抓到日志后判定 BOOT_OK / BOOT_FAIL：
    成功 = 出现 ESP-IDF 启动横幅（ESP-ROM: 或 I ( 形式）且没有持续重启。

退出码
------
  0 成功 / 1 运行期错误 / 2 参数或校验错误 / 3 --monitor 判定 BOOT_FAIL
"""
    parser = ChineseArgumentParser(
        prog="flash_firmware.py",
        description="EHome 采集器固件烧录工具（ESP32-C6 / ESP32-S3，Windows + PowerShell）",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=epilog,
    )
    parser.add_argument(
        "--profile", default="c6-n8", metavar="PROFILE",
        help="固件 profile，可选：%s（默认 c6-n8）；c6-* -> esp32c6，s3-* -> esp32s3"
             % PROFILE_LIST,
    )
    parser.add_argument(
        "--port", default=None, metavar="COMx",
        help="串口名（如 COM9）。省略时自动探测 VID_303A / VID_10C4 的串口。",
    )
    parser.add_argument(
        "--baud", type=baud_type, default=DEFAULT_BAUD, metavar="N",
        help="烧录波特率（默认 %d）。" % DEFAULT_BAUD,
    )
    parser.add_argument(
        "--dry-run", action="store_true",
        help="只打印将要执行的 esptool 命令与镜像清单，完全不碰串口。",
    )
    parser.add_argument(
        "--monitor", type=monitor_type, default=None, metavar="N",
        help="烧录后用同一串口抓 N 秒启动日志并打印，最后给出 BOOT_OK / BOOT_FAIL。",
    )
    parser.add_argument(
        "--erase", action="store_true",
        help="烧录前先执行 erase-flash 整片擦除（默认不擦）。",
    )
    parser.add_argument(
        "--no-reset", action="store_true",
        help="--monitor 时不要先复位开发板（默认会先发一次复位，以便抓全启动横幅）。",
    )
    parser.add_argument(
        "--build-root", default=None, metavar="DIR",
        help="构建产物根目录（默认 esp32-collector/build，可用环境变量 BUILD_ROOT 覆盖）。",
    )
    return parser


# --------------------------------------------------------------------------
# 子进程执行
# --------------------------------------------------------------------------

def display_cmd(cmd: list[str]) -> str:
    return subprocess.list2cmdline(cmd)


def run_stream(cmd: list[str], title: str) -> tuple[int, str]:
    """执行命令并实时回显输出，返回 (退出码, 全部输出)。"""
    info("")
    info("==> %s" % title)
    info("    " + display_cmd(cmd))
    collected: list[str] = []
    try:
        proc = subprocess.Popen(
            cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, encoding="utf-8", errors="replace", bufsize=1,
            env=child_env(),
        )
    except Exception as exc:
        raise ToolError("无法启动命令：%s" % exc, 1)

    assert proc.stdout is not None
    try:
        for line in proc.stdout:
            collected.append(line)
            sys.stdout.write(line)
            sys.stdout.flush()
    finally:
        proc.wait()
    return proc.returncode, "".join(collected)


# --------------------------------------------------------------------------
# 目标校验
# --------------------------------------------------------------------------

# esptool 5.x 的连接横幅形如：
#     Connected to ESP32-C6 on COM9:
#     Chip type:            ESP32-C6 (QFN40) (revision v0.1)
# 前者用的是芯片类常量 CHIP_NAME，最可靠；后者带封装信息，作为后备。
CHIP_CONNECTED_RE = re.compile(r"Connected to\s+(ESP32-[A-Za-z0-9]+)\b")
CHIP_TYPE_RE = re.compile(r"Chip type:\s*(ESP32-[A-Za-z0-9]+)\b")
# flash-id / write-flash 会打印 "Detected flash size: 8MB"（无法识别时为 Unknown）
FLASH_SIZE_RE = re.compile(
    r"(?:Auto-detected|Detected)\s+flash size:\s*([0-9]+\s*(?:KB|MB))",
    re.IGNORECASE)
SIZE_TOKEN_RE = re.compile(r"^([0-9]+)\s*(KB|MB)$", re.IGNORECASE)


def parse_size_token(token: str) -> int | None:
    m = SIZE_TOKEN_RE.match(token.strip())
    if not m:
        return None
    value = int(m.group(1))
    unit = m.group(2).upper()
    return value * 1024 if unit == "KB" else value * MB


def parse_probe_output(text: str) -> tuple[str | None, str | None]:
    """从 esptool flash-id 输出里解析 (芯片名, flash 容量字符串)。"""
    chip = None
    for pattern in (CHIP_CONNECTED_RE, CHIP_TYPE_RE):
        m = pattern.search(text)
        if m:
            chip = m.group(1)
            break
    flash = None
    m = FLASH_SIZE_RE.search(text)
    if m:
        flash = m.group(1).replace(" ", "").upper()
    return chip, flash


def probe_device(python_path: str, port: str, baud: int) -> tuple[int, str]:
    """用 --chip auto 实测开发板，返回 (退出码, 输出)。"""
    return run_stream(probe_cmd(python_path, port, baud),
                      "校验目标：读取芯片型号与 flash 容量")


def verify_target(profile: Profile, probe_out: str) -> None:
    """校验实测芯片/容量与 profile 是否一致，不一致直接拒绝烧录。"""
    chip, flash = parse_probe_output(probe_out)
    info("")
    info("目标校验：")
    info("    期望芯片    : %s" % profile.chip_name)
    info("    实测芯片    : %s" % (chip or "无法识别"))
    info("    期望容量    : %s (%d 字节)" % (profile.flash_size, profile.flash_bytes))
    info("    实测容量    : %s" % (flash or "无法识别"))

    if chip is None:
        raise ToolError(
            "esptool 没有报告芯片型号，无法确认目标是否与 profile %s 匹配，拒绝烧录。"
            % profile.name, 2)
    if chip.upper() != profile.chip_name.upper():
        raise ToolError(
            "芯片型号不匹配：profile %s 期望 %s，实测 %s。拒绝烧录（防刷错变砖）。"
            % (profile.name, profile.chip_name, chip), 2)
    if flash is None:
        raise ToolError(
            "esptool 没有报告 flash 容量，无法确认 %s 是否匹配，拒绝烧录。"
            % profile.flash_size, 2)
    measured = parse_size_token(flash)
    if measured is None:
        raise ToolError("无法解析实测 flash 容量 %r，拒绝烧录。" % flash, 2)
    if measured != profile.flash_bytes:
        raise ToolError(
            "flash 容量不匹配：profile %s 期望 %s (%d 字节)，实测 %s (%d 字节)。"
            "拒绝烧录（防刷错变砖）。"
            % (profile.name, profile.flash_size, profile.flash_bytes,
               flash, measured), 2)
    info("    结论        : 匹配，可以烧录")


# --------------------------------------------------------------------------
# 镜像清单校验与打印
# --------------------------------------------------------------------------

def check_profile_match(profile: Profile, manifest: Manifest) -> None:
    """build 目录声明的容量必须与 profile 一致，否则拒绝烧录。"""
    if not manifest.flash_size:
        return
    declared = parse_size_token(manifest.flash_size)
    if declared is None:
        return
    if declared != profile.flash_bytes:
        raise ToolError(
            "构建目录声明的 flash 容量与 profile 不一致：\n"
            "    %s 里写的是 --flash-size %s\n"
            "    但 profile %s 期望 %s\n"
            "    build/<profile> 与 profile 不配套，拒绝烧录。\n"
            "    请确认 BUILD_ROOT 是否正确，或用 ./build_firmware.sh %s 重新构建。"
            % (manifest.source, manifest.flash_size, profile.name,
               profile.flash_size, profile.name), 2)


def print_manifest(profile: Profile, build_dir: Path, manifest: Manifest) -> list[ImageEntry]:
    info("")
    info("==> 镜像清单（profile=%s，构建目录=%s）" % (profile.name, build_dir))
    info("    来源: %s" % manifest.source)
    header = "    %-12s %-38s %-10s %s" % ("地址", "镜像", "大小", "状态")
    info(header)
    info("    " + "-" * (len(header) - 4))
    for entry in manifest.entries:
        if entry.exists:
            status = "OK"
            size = human_size(entry.size)
        else:
            status = "缺失"
            size = "-"
        info("    %-12s %-38s %-10s %s"
             % ("0x%x" % entry.addr, entry.rel, size, status))
    return [e for e in manifest.entries if not e.exists]


def ensure_images_present(profile: Profile, manifest: Manifest,
                          build_dir: Path) -> None:
    missing = [e for e in manifest.entries if not e.exists]
    if not missing:
        empty = [e for e in manifest.entries if e.size == 0]
        if empty:
            raise ToolError(
                "以下镜像文件是空的（0 字节）：\n%s\n请先重新构建。"
                % "\n".join("    %s" % e.path for e in empty), 2)
        return
    lines = ["缺少固件文件，无法烧录："]
    for e in missing:
        lines.append("    缺失: %s（期望地址 0x%x）" % (e.path, e.addr))
    lines.append("    构建目录: %s" % build_dir)
    lines.append("    请先运行 ./build_firmware.sh %s 生成固件。" % profile.name)
    raise ToolError("\n".join(lines), 2)


# --------------------------------------------------------------------------
# esptool 命令构造
# --------------------------------------------------------------------------

def esptool_base(python_path: str, chip: str, port: str, baud: int,
                 after: str | None = None) -> list[str]:
    cmd = [python_path, "-m", "esptool", "--chip", chip,
           "--port", port, "--baud", str(baud)]
    if after:
        cmd += ["--after", after]
    return cmd


def write_flash_cmd(python_path: str, profile: Profile, port: str, baud: int,
                    manifest: Manifest) -> list[str]:
    cmd = esptool_base(python_path, profile.target, port, baud, after="hard-reset")
    cmd += ["write-flash", "--flash-size", profile.flash_size]
    if manifest.flash_mode:
        cmd += ["--flash-mode", manifest.flash_mode]
    if manifest.flash_freq:
        cmd += ["--flash-freq", manifest.flash_freq]
    for entry in manifest.entries:
        cmd += ["0x%x" % entry.addr, str(entry.path)]
    return cmd


def erase_flash_cmd(python_path: str, profile: Profile, port: str, baud: int) -> list[str]:
    cmd = esptool_base(python_path, profile.target, port, baud, after="no-reset")
    cmd += ["erase-flash"]
    return cmd


# --------------------------------------------------------------------------
# 监控
# --------------------------------------------------------------------------

MONITOR_CHILD_CODE = r'''
import sys, time
try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass
try:
    import serial
except Exception as exc:
    sys.stdout.write("__EHOME_MONITOR_ERROR__ pyserial 不可用: %s\n" % exc)
    sys.stdout.flush()
    raise SystemExit(4)

port = sys.argv[1]
baud = int(sys.argv[2])
seconds = float(sys.argv[3])
do_reset = sys.argv[4] == "1"

try:
    ser = serial.Serial(port, baud, timeout=0.2)
except Exception as exc:
    sys.stdout.write("__EHOME_MONITOR_ERROR__ 无法打开串口 %s: %s\n" % (port, exc))
    sys.stdout.flush()
    raise SystemExit(4)


def pulse_reset():
    # 经典 UART 复位时序：DTR 保持 IO0 高电平，RTS 拉低 EN 再放开。
    ser.setDTR(False)
    ser.setRTS(True)
    time.sleep(0.12)
    ser.setRTS(False)


def note(msg):
    sys.stdout.write("__EHOME_MONITOR_NOTE__ %s\n" % msg)
    sys.stdout.flush()


if do_reset:
    try:
        pulse_reset()
        note("已发送复位（DTR/RTS），从启动横幅开始抓取")
    except Exception as exc:
        note("复位失败（忽略，继续抓取）: %s" % exc)

start = time.time()
deadline = start + seconds
pending = b""
idle_reset_used = do_reset

while time.time() < deadline:
    try:
        chunk = ser.read(4096)
    except Exception as exc:
        sys.stdout.write("\n__EHOME_MONITOR_ERROR__ 读取串口失败: %s\n" % exc)
        sys.stdout.flush()
        break
    if chunk:
        pending += chunk
        while b"\n" in pending:
            line, pending = pending.split(b"\n", 1)
            sys.stdout.write(line.decode("utf-8", "replace").rstrip("\r") + "\n")
        sys.stdout.flush()
    elif not idle_reset_used and (time.time() - start) > 2.0:
        idle_reset_used = True
        try:
            pulse_reset()
            note("2 秒内没有输出，补发一次复位")
        except Exception:
            pass

if pending:
    sys.stdout.write(pending.decode("utf-8", "replace"))
    sys.stdout.flush()

try:
    ser.close()
except Exception:
    pass
raise SystemExit(0)
'''

ROM_BANNER_RE = re.compile(r"^\s*ESP-ROM:", re.MULTILINE)
IDF_LOG_RE = re.compile(r"\bI \(\d+\)")
REBOOT_RE = re.compile(r"ESP-ROM:|rst:0x[0-9a-fA-F]+")
PANIC_RE = re.compile(
    r"Guru Meditation|panic'ed|abort\(\)|assert failed|"
    r"Task watchdog got triggered|Stack canary|invalid header",
    re.IGNORECASE)


def run_monitor(python_path: str, port: str, baud: int, seconds: int,
                do_reset: bool, profile: Profile) -> int:
    info("")
    info("==> 监控串口 %s（%d 秒，baud=%d）" % (port, seconds, baud))
    info("    （日志原样输出如下；退出码 3 表示判定 BOOT_FAIL）")
    info("    " + "-" * 60)

    cmd = [python_path, "-c", MONITOR_CHILD_CODE, port, str(baud),
           str(seconds), "1" if do_reset else "0"]
    raw_lines: list[str] = []
    rc = 1
    try:
        proc = subprocess.Popen(
            cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, encoding="utf-8", errors="replace", bufsize=1,
            env=child_env(),
        )
        assert proc.stdout is not None
        try:
            for line in proc.stdout:
                text = line.rstrip("\n").rstrip("\r")
                if text.startswith("__EHOME_MONITOR_NOTE__"):
                    info("    [监控] "
                         + text[len("__EHOME_MONITOR_NOTE__"):].strip())
                    continue
                if text.startswith("__EHOME_MONITOR_ERROR__"):
                    print("    [监控错误] " + text[len("__EHOME_MONITOR_ERROR__"):].strip(),
                          file=sys.stderr, flush=True)
                    continue
                raw_lines.append(text)
                sys.stdout.write(line if line.endswith("\n") else line + "\n")
                sys.stdout.flush()
        finally:
            proc.wait()
        rc = proc.returncode
    except KeyboardInterrupt:
        try:
            proc.kill()
        except Exception:
            pass
        info("")
        warn("监控被中断（Ctrl+C）")
    info("    " + "-" * 60)

    if rc == 4:
        raise ToolError("监控失败：无法打开/读取串口 %s（可能被其它程序占用）。" % port, 1)
    if rc not in (0, None):
        warn("监控子进程退出码 %s" % rc)

    return analyze_boot(raw_lines, profile)


def analyze_boot(lines: list[str], profile: Profile) -> int:
    text = "\n".join(lines)
    rom_hits = len(ROM_BANNER_RE.findall(text))
    idf_hits = len(IDF_LOG_RE.findall(text))
    reboot_hits = len(REBOOT_RE.findall(text))
    panics = PANIC_RE.findall(text)

    banner = rom_hits > 0 or idf_hits > 0

    info("")
    info("==> 启动判定（profile=%s）" % profile.name)
    info("    ESP-ROM 横幅次数 : %d" % rom_hits)
    info("    IDF 日志行数     : %d" % idf_hits)
    info("    复位/重启标记数  : %d" % reboot_hits)
    if panics:
        info("    异常关键字       : %s" % ", ".join(sorted(set(p.lower() for p in panics))))

    reasons: list[str] = []
    if not banner:
        reasons.append("没有抓到 ESP-IDF 启动横幅（ESP-ROM: 或 I ( 形式）")
    if reboot_hits >= 3:
        reasons.append("抓到 %d 次复位/重启标记，疑似持续重启" % reboot_hits)

    if reasons:
        for reason in reasons:
            info("    失败原因         : %s" % reason)
        info("")
        info("BOOT_FAIL")
        if not banner and not lines:
            info("（监控期间没有任何串口输出；请确认串口正确、板子已上电、"
                 "或用 --no-reset 再试一次）")
        return 3

    if panics:
        warn("日志里出现异常关键字（%s），但启动横幅正常且未见持续重启。"
             % ", ".join(sorted(set(p.lower() for p in panics))))
    info("")
    info("BOOT_OK")
    return 0


# --------------------------------------------------------------------------
# 主流程
# --------------------------------------------------------------------------

def resolve_build_root(args) -> Path:
    if args.build_root:
        return Path(args.build_root).expanduser().resolve()
    env_root = os.environ.get("BUILD_ROOT", "").strip().strip('"')
    if env_root:
        return Path(env_root).expanduser().resolve()
    return DEFAULT_BUILD_ROOT


def print_header(profile: Profile, port: str, args, python_pick: PythonPick,
                 build_dir: Path) -> None:
    info("=" * 72)
    info("EHome 固件烧录工具  profile=%s" % profile.name)
    info("=" * 72)
    info("    目标芯片      : %s (--chip %s)" % (profile.chip_name, profile.target))
    info("    期望 flash    : %s (%d 字节)" % (profile.flash_size, profile.flash_bytes))
    info("    构建目录      : %s" % build_dir)
    info("    串口          : %s" % port)
    info("    波特率        : %d" % args.baud)
    info("    python 解释器 : %s（来源：%s）" % (python_pick.path, python_pick.source))
    info("    esptool       : %s" % ("可用（-m esptool）" if python_pick.has_esptool else "不可用"))
    info("    pyserial      : %s" % ("可用" if python_pick.has_pyserial else "不可用"))
    info("    模式          : %s" % ("dry-run（不碰串口）" if args.dry_run else "真实烧录"))
    if args.erase:
        info("    擦除          : 烧录前先 erase-flash 整片擦除")
    if args.monitor:
        info("    监控          : 烧录后抓 %d 秒启动日志" % args.monitor)


def probe_cmd(python_path: str, port: str, baud: int) -> list[str]:
    """目标校验命令：--chip auto + flash-id（同时拿到芯片型号与 flash 容量）。"""
    return [python_path, "-m", "esptool", "--chip", "auto",
            "--port", port, "--baud", str(baud), "flash-id"]


def main(argv: list[str] | None = None) -> int:
    setup_stdio()
    parser = build_parser()
    args = parser.parse_args(argv)

    profile_name = (args.profile or "").strip().lower()
    if profile_name not in PROFILES:
        return fail("profile %r 不合法，可选值：%s。" % (args.profile, PROFILE_LIST), 2)
    profile = PROFILES[profile_name]

    build_dir = resolve_build_root(args) / profile.name

    try:
        python_pick = resolve_python(require_esptool=not args.dry_run)
        port, port_notes = resolve_port(args.port, python_pick.path, args.dry_run)
        if port_notes:
            info("")
            info("==> 串口解析")
            for line in port_notes:
                info(line if line.startswith("警告") else "    " + line)
    except ToolError as exc:
        return fail(str(exc), exc.code)

    try:
        print_header(profile, port, args, python_pick, build_dir)

        # ---- 镜像清单 ----------------------------------------------------
        manifest = load_manifest(build_dir)
        missing = print_manifest(profile, build_dir, manifest)
        if missing:
            ensure_images_present(profile, manifest, build_dir)   # 抛错（中文）
        check_profile_match(profile, manifest)

        # ---- dry-run -----------------------------------------------------
        if args.dry_run:
            info("")
            info("==> dry-run：以下命令不会执行（不碰串口）")
            step = 0
            step += 1
            info("    (%d) 目标/容量校验：" % step)
            info("        " + display_cmd(probe_cmd(python_pick.path, port, args.baud)))
            if args.erase:
                step += 1
                info("    (%d) 整片擦除：" % step)
                info("        " + display_cmd(erase_flash_cmd(python_pick.path, profile, port, args.baud)))
            step += 1
            info("    (%d) 烧录：" % step)
            info("        " + display_cmd(write_flash_cmd(python_pick.path, profile, port,
                                                          args.baud, manifest)))
            if args.monitor:
                step += 1
                info("    (%d) 监控 %d 秒：python -c <pyserial 抓取> %s %d %d %s"
                     % (step, args.monitor, port, args.baud, args.monitor,
                        "0" if args.no_reset else "1"))
            info("")
            info("dry-run 结束：以上 %d 个步骤在真实运行时依次执行。" % step)
            return 0

        # ---- 真实烧录 ----------------------------------------------------
        rc, probe_out = probe_device(python_pick.path, port, args.baud)
        if rc != 0:
            chip, flash = parse_probe_output(probe_out)
            detail = ""
            if chip or flash:
                detail = "\n实测：芯片=%s，flash 容量=%s" % (chip or "未知", flash or "未知")
            return fail("连接开发板失败（esptool 退出码 %d）。请检查 --port %s 是否正确、"
                        "是否被其它程序占用、开发板是否上电。%s" % (rc, port, detail), 1)
        verify_target(profile, probe_out)

        if args.erase:
            rc, _ = run_stream(erase_flash_cmd(python_pick.path, profile, port, args.baud),
                               "整片擦除 erase-flash")
            if rc != 0:
                return fail("erase-flash 失败（退出码 %d）。" % rc, 1)

        rc, _ = run_stream(write_flash_cmd(python_pick.path, profile, port, args.baud, manifest),
                           "烧录固件 write-flash")
        if rc != 0:
            return fail("烧录失败（esptool 退出码 %d）。" % rc, 1)
        info("")
        info("==> 烧录完成：%s（%d 个镜像）" % (profile.name, len(manifest.entries)))

        if args.monitor:
            return run_monitor(python_pick.path, port, args.baud, args.monitor,
                               not args.no_reset, profile)

        info("")
        info("提示：加 --monitor 20 可以在烧录后抓 20 秒启动日志验证是否真的起来了。")
        return 0

    except ToolError as exc:
        return fail(str(exc), exc.code)
    except KeyboardInterrupt:
        info("")
        warn("已中断（Ctrl+C）")
        return 130
    except Exception as exc:  # 兜底：任何未预料的异常都用中文报出来
        return fail("未预期错误：%s: %s" % (type(exc).__name__, exc), 1)


if __name__ == "__main__":
    sys.exit(main())
