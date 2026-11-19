#!/usr/bin/env python3
"""pack_firmware.py / fwimage.py 的回归用例（CI 落点：backend job 的 pytest 一步）。

覆盖（方案 §7.5 + §8.2 + §10）：
  - 正例：打包 → 自检 → 清单字段 / 条目元数据全部正确；
  - 确定性（AC-P11）：同一输入连续打包两次字节相同；
  - 交叉实现：打包器必须能吃下 vectors 生成的 `ok_c6-n8.ehfw` 里的合成镜像，
    且产出的清单（除 `build` 块）与 `ok_c6-n8.expected.json` 完全一致；
  - 负例：摘要不符 / 条目数错 / 条目名非法 / format_version 错 / 压缩比超限；
  - 打包前门禁：陈旧 .bin / 档位不符 / psram 矛盾 / sdkconfig 与镜像不符 / 版本不是 X.Y.Z。

约定：用例只通过 **CLI 子进程**驱动 pack_firmware.py（打包器是给构建脚本用的可执行文件，
不是库），这样「参数解析 / 退出码 / stderr 机器码」也被测到。
"""
import json
import os
import shutil
import subprocess
import sys
import zipfile

import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))  # <repo>/esp32-collector/tools -> <repo>
VECTORS = os.path.join(ROOT, "protocol", "firmware-package", "vectors")
GEN_VECTORS = os.path.join(VECTORS, "gen_vectors.py")
PACK = os.path.join(HERE, "pack_firmware.py")

sys.path.insert(0, HERE)
import fwimage  # noqa: E402  (构建侧共享库，被测对象之一)


def run_pack(*args):
    """跑一次 CLI。cwd 固定仓库根：用例里的相对路径都相对它。"""
    return subprocess.run([sys.executable, PACK] + list(args),
                          cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          text=True, check=False)


def write_sdkconfig(path, target="esp32c6", flash="8MB", spiram=None):
    """写一份最小 sdkconfig（pack_firmware 只读三个符号）。

    spiram=None 表示**整个符号缺失** —— 这正是 C6 的真实形态（该 SoC 无 PSRAM 能力，
    Kconfig 里没有 CONFIG_SPIRAM），也是 fwimage.resolve_psram 必须特别处理的情形。
    """
    lines = ['CONFIG_IDF_TARGET="%s"' % target,
             'CONFIG_ESPTOOLPY_FLASHSIZE="%s"' % flash]
    if spiram is True:
        lines.append("CONFIG_SPIRAM=y")
    elif spiram is False:
        lines.append("# CONFIG_SPIRAM is not set")
    with open(path, "w", encoding="utf-8") as fp:
        fp.write("\n".join(lines) + "\n")
    return path


def extract_artifact(pkg_path, dest):
    """从向量包里取出 artifact：让「打包器吃同一份合成镜像」成为可能。"""
    with zipfile.ZipFile(pkg_path, "r") as zf:
        data = zf.read("ehome_collector.bin")
    with open(dest, "wb") as fp:
        fp.write(data)
    return dest


@pytest.fixture()
def c6_build(tmp_path):
    """一份「C6 / 8MB / 2.9.0」的构建产物：合成镜像直接取自 ok 向量（固定输入）。"""
    bin_path = extract_artifact(os.path.join(VECTORS, "ok_c6-n8.ehfw"),
                                str(tmp_path / "ehome_collector.bin"))
    sdkconfig = write_sdkconfig(str(tmp_path / "sdkconfig"), target="esp32c6", flash="8MB")
    return {"bin": bin_path, "sdkconfig": sdkconfig, "tmp": str(tmp_path)}


def load_cases():
    with open(os.path.join(VECTORS, "cases.json"), "r", encoding="utf-8") as fp:
        return json.load(fp)["cases"]


@pytest.fixture(scope="module")
def expected_ok():
    with open(os.path.join(VECTORS, "ok_c6-n8.expected.json"), "r", encoding="utf-8") as fp:
        return json.load(fp)


# ============================================================================
# 正例
# ============================================================================
def test_pack_ok_writes_two_entry_package(c6_build):
    out = os.path.join(c6_build["tmp"], "ehome_collector-2.9.0-c6-n8.ehfw")
    result = run_pack("--bin", c6_build["bin"], "--sdkconfig", c6_build["sdkconfig"],
                      "--profile", "c6-n8", "--version", "2.9.0", "--out", out)
    assert result.returncode == 0, result.stderr
    assert os.path.isfile(out)
    assert not os.path.exists(out + ".tmp"), "临时文件必须被 os.replace 消费掉"

    with zipfile.ZipFile(out, "r") as zf:
        infos = zf.infolist()
        assert [info.filename for info in infos] == sorted(info.filename for info in infos), \
            "条目必须按名排序（确定性的一部分）"
        assert len(infos) == 2
        names = {info.filename for info in infos}
        assert names == {"fw-manifest.json", "ehome_collector.bin"}
        # 逐条锁定确定性属性：写错一项，两次打包的字节就不再相同（AC-P11）。
        for info in infos:
            assert info.date_time == (1980, 1, 1, 0, 0, 0), info.filename
            assert info.compress_type == zipfile.ZIP_STORED, info.filename
            assert info.create_system == 3, info.filename
            # 0o100644 << 16 = Unix mode 含 S_IFREG；缺了会显示 ?rw-r--r--（§7.5）。
            assert info.external_attr == 0o100644 << 16, info.filename
        manifest = json.loads(zf.read("fw-manifest.json").decode("utf-8"))
        artifact = zf.read("ehome_collector.bin")

    assert manifest["format"] == "ehome-firmware"
    assert manifest["format_version"] == 1
    assert manifest["packer"]["name"] == "pack_firmware.py"
    assert manifest["artifact"]["name"] == "ehome_collector.bin"
    assert manifest["artifact"]["size"] == len(artifact)
    assert manifest["artifact"]["sha256"] == fwimage.sha256_bytes(artifact)
    assert manifest["identity"] == {"psram": False, "profile": "c6-n8"}
    # 清单里**没有** version：版本在镜像里，服务端从镜像读（避免双真源）。
    assert "version" not in json.dumps(manifest["artifact"])
    assert "built_at" not in manifest.get("build", {})

    check = run_pack("--verify", out)
    assert check.returncode == 0, check.stderr
    assert "FW_PKG" not in check.stderr


def test_pack_is_deterministic(c6_build):
    """AC-P11：同一输入连续打包两次，.ehfw 的 sha256 相同。"""
    digests = []
    for index in (1, 2):
        out = os.path.join(c6_build["tmp"], "twice-%d.ehfw" % index)
        result = run_pack("--bin", c6_build["bin"], "--sdkconfig", c6_build["sdkconfig"],
                          "--profile", "c6-n8", "--version", "2.9.0", "--out", out)
        assert result.returncode == 0, result.stderr
        digests.append(fwimage.sha256_file(out))
    assert digests[0] == digests[1]
    with open(os.path.join(c6_build["tmp"], "twice-1.ehfw"), "rb") as fp:
        first = fp.read()
    with open(os.path.join(c6_build["tmp"], "twice-2.ehfw"), "rb") as fp:
        second = fp.read()
    assert first == second


def test_pack_matches_vector_manifest(c6_build, expected_ok):
    """交叉实现：向量是「独立第三实现」写的，打包器必须给出同一份清单（除 build 块）。"""
    out = os.path.join(c6_build["tmp"], "cross.ehfw")
    result = run_pack("--bin", c6_build["bin"], "--sdkconfig", c6_build["sdkconfig"],
                      "--profile", "c6-n8", "--version", "2.9.0", "--out", out)
    assert result.returncode == 0, result.stderr
    with zipfile.ZipFile(out, "r") as zf:
        manifest = json.loads(zf.read("fw-manifest.json").decode("utf-8"))
    manifest.pop("build", None)  # 向量刻意不含 build：它是打包机的 git 状态，不是输入
    assert manifest == expected_ok["manifest"]
    assert manifest["artifact"]["sha256"] == expected_ok["image"]["sha256"]


def test_verify_accepts_ok_vector():
    result = run_pack("--verify", os.path.join(VECTORS, "ok_c6-n8.ehfw"))
    assert result.returncode == 0, result.stderr
    assert "profile=c6-n8" in result.stdout
    assert "chip=esp32c6" in result.stdout


def test_ok_vector_uses_same_zip_metadata_as_packer():
    """生成器与打包器是两个写方，条目元数据（mode/system/时间戳/压缩）必须同值。"""
    with zipfile.ZipFile(os.path.join(VECTORS, "ok_c6-n8.ehfw"), "r") as zf:
        for info in zf.infolist():
            assert info.external_attr == 0o100644 << 16, info.filename
            assert info.create_system == 3, info.filename
            assert info.compress_type == zipfile.ZIP_STORED, info.filename
            assert info.date_time == (1980, 1, 1, 0, 0, 0), info.filename


def test_ok_vector_matches_expected_identity(expected_ok):
    """向量自洽：清单/镜像身份与实际内容一致（Go 侧断言同一组值）。"""
    pkg = os.path.join(VECTORS, "ok_c6-n8.ehfw")
    with zipfile.ZipFile(pkg, "r") as zf:
        artifact = zf.read("ehome_collector.bin")
    image = fwimage.parse_image_header(artifact[:fwimage.HEADER_SIZE], len(artifact))
    assert image["chip"] == expected_ok["image"]["chip"]
    assert image["chip_id"] == expected_ok["image"]["chip_id"]
    assert image["flash_mb"] == expected_ok["image"]["flash_mb"]
    assert image["version"] == expected_ok["image"]["version"]
    assert image["project"] == expected_ok["image"]["project"]
    assert image["elf_sha256"] == expected_ok["image"]["elf_sha256"]
    assert fwimage.sha256_bytes(artifact) == expected_ok["image"]["sha256"]


# ============================================================================
# 负例（向量驱动：cases.json 是 Go/Python 两侧共用的契约）
# ============================================================================
@pytest.mark.parametrize("case", [c for c in load_cases() if c["open_error"]],
                         ids=lambda case: case["file"])
def test_verify_rejects_vector_with_open_error(case):
    result = run_pack("--verify", os.path.join(VECTORS, case["file"]))
    assert result.returncode == 1, "expected rejection, got rc=%d" % result.returncode
    assert case["open_error"] in result.stderr, result.stderr


@pytest.mark.parametrize("case", [c for c in load_cases()
                                  if c["handler_error"] and not c["open_error"]],
                         ids=lambda case: case["file"])
def test_verify_rejects_vector_with_handler_error(case):
    """结构合法但摘要不符：Open 等价步骤通过，摘要比对那一步拒绝（评审 B-2 的归属）。"""
    result = run_pack("--verify", os.path.join(VECTORS, case["file"]))
    assert result.returncode == 1
    assert case["handler_error"] in result.stderr, result.stderr
    assert "FW_PKG_INVALID" not in result.stderr, "摘要问题不该被报成结构问题"


def test_verify_rejects_raw_bin(c6_build):
    """裸 .bin（0xE9 开头）→ 必须提示「不是包，用 pack_firmware.py 打包」。"""
    result = run_pack("--verify", c6_build["bin"])
    assert result.returncode == 1
    assert "FW_PKG_REQUIRED" in result.stderr
    assert "pack_firmware.py" in result.stderr


def test_cases_json_covers_every_vector():
    """向量目录里不许有 cases.json 没登记的 .ehfw（否则新规则没有对应向量）。"""
    on_disk = {name for name in os.listdir(VECTORS) if name.endswith(".ehfw")}
    listed = {case["file"] for case in load_cases()}
    assert on_disk == listed


def test_vectors_are_reproducible():
    """向量本身必须能由 gen_vectors.py 逐字节重建（否则「固定输入」是空话）。"""
    result = subprocess.run([sys.executable, GEN_VECTORS, "--check"],
                            cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            text=True, check=False)
    assert result.returncode == 0, result.stdout + result.stderr


# ============================================================================
# 打包前门禁
# ============================================================================
def _expect_reject(c6_build, needle, **overrides):
    arguments = {"bin": c6_build["bin"], "sdkconfig": c6_build["sdkconfig"],
                 "profile": "c6-n8", "version": "2.9.0"}
    arguments.update(overrides)
    out = os.path.join(c6_build["tmp"], "should-not-exist.ehfw")
    result = run_pack("--bin", arguments["bin"], "--sdkconfig", arguments["sdkconfig"],
                      "--profile", arguments["profile"], "--version", arguments["version"],
                      "--out", out, *(["--psram", arguments["psram"]] if "psram" in arguments else []))
    assert result.returncode == 1, result.stdout
    assert needle in result.stderr, result.stderr
    # fail-closed：失败不落包、不留临时文件。
    assert not os.path.exists(out)
    assert not os.path.exists(out + ".tmp")
    return result


def test_pack_rejects_stale_bin(c6_build):
    """镜像内 version 才是判据（评审 A-2：不再读 CMakeLists 正则）。"""
    result = _expect_reject(c6_build, "stale .bin", version="2.8.0")
    assert "2.9.0" in result.stderr


def test_pack_rejects_bad_version_shape(c6_build):
    _expect_reject(c6_build, "is not X.Y.Z", version="0.0.0-dev")


def test_pack_rejects_wrong_profile(c6_build):
    result = _expect_reject(c6_build, "--profile", profile="c6-n16")
    assert "c6-n8" in result.stderr


def test_pack_rejects_psram_override_conflict(c6_build):
    """C6 上 --psram yes 与 sdkconfig（无 CONFIG_SPIRAM）矛盾 → 拒绝。"""
    _expect_reject(c6_build, "--psram", psram="yes")


def test_pack_rejects_sdkconfig_image_mismatch(c6_build, tmp_path):
    wrong = write_sdkconfig(str(tmp_path / "sdkconfig-s3"), target="esp32s3", flash="8MB",
                            spiram=False)
    result = _expect_reject(c6_build, "CONFIG_IDF_TARGET", sdkconfig=wrong)
    assert "esp32s3" in result.stderr


def test_pack_rejects_missing_artifact(tmp_path):
    result = run_pack("--bin", str(tmp_path / "nope.bin"),
                      "--sdkconfig", write_sdkconfig(str(tmp_path / "sdkconfig")),
                      "--profile", "c6-n8", "--version", "2.9.0",
                      "--out", str(tmp_path / "x.ehfw"))
    assert result.returncode == 1
    assert "FW_NOT_IMAGE" in result.stderr or "FW_TRUNCATED" in result.stderr


def test_verify_is_exclusive():
    """--verify 与打包参数同时出现必须报错，而不是静默忽略一半。"""
    result = run_pack("--verify", os.path.join(VECTORS, "ok_c6-n8.ehfw"),
                      "--profile", "c6-n8")
    assert result.returncode == 1
    assert "exclusive" in result.stderr
