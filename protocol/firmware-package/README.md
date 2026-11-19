# 固件单文件包 `.ehfw` 的 golden vectors

> 规格真源：[固件单文件包与构建打包规范](../../docs/计划/固件单文件包与构建打包规范-2026-11-19.md)
> §4（包格式 v1）、§8.2（向量清单）。本目录只放**格式契约**与**它的证据**，不放实现。

## 为什么这里会有二进制文件

打包在 Python（构建机：`esp32-collector/tools/pack_firmware.py`），解析在 Go（服务端：
`backend/internal/firmwarepkg`）。两份实现必然同时存在，风险是**各自自洽却互相不通** ——
本仓在帧协议上吃过这个亏，于是有了 `protocol/vectors/` + `tools/check_golden_vectors.py`。
`.ehfw` 沿用同一做法：**新增一条格式规则，必须同时加一个 vector，两侧都绿才算改完**。

## 包格式速查（详细定义见规格 §4）

```text
.ehfw = ZIP（PKZIP 2.0，不启用 ZIP64）；识别**按内容**：首 4 字节 50 4B 03 04，不看扩展名

  fw-manifest.json       必需，format_version = 1，≤ 64 KiB
  ehome_collector.bin    必需，设备下载的就是这串字节，STORE 方式

  条目恒为 2 / 全部在根（扁平命名，无目录）/ 全小写 / 1980-01-01 00:00 / 按名排序
  上限：解压总量 16 MiB、单条目压缩比 100:1（解压前用中央目录判定）
```

**条目顺序**：按名排序 ⇒ `ehome_collector.bin` 排在 `fw-manifest.json` **之前**
（`ehome_collector.bin` < `fw-manifest.json`）。排序是确定性要求（规格 §7.5，AC-P11：
同一输入两次打包字节相同）；§4.2 的 `unzip -l` 示例已按真实顺序同步。

**清单只写镜像说不了的事**：`identity.psram` + `identity.profile`。chip / flash_mb /
version / elf_sha256 全部由服务端从镜像现读 —— 在包里再存一遍就是双真源。

不变量（违反时的错误码见下表）：INV-1 恰好两条 · INV-2 摘要一致 · INV-3 `profile` 必须能被
（镜像 chip + 镜像 flash_mb + 清单 psram）还原 · INV-4 条目名匹配 `^[a-z0-9][a-z0-9._-]{0,63}$`
· INV-5 上限 · INV-6 清单是输入的纯函数（无时间戳/随机数/绝对路径）。

## 向量清单

| 文件 | 用途 | 期望（`open_error` / `handler_error`） |
|---|---|---|
| `ok_c6-n8.ehfw` | 两条目正例，2.8 KB | 成功 / 摘要一致 |
| `bad_digest.ehfw` | `artifact.sha256` 错一位，其余自洽 | 成功 / `FW_PKG_DIGEST_MISMATCH` |
| `bad_entry_count.ehfw` | 多一个 `extra.txt`（3 条目） | `FW_PKG_INVALID` / — |
| `bad_entry_name.ehfw` | 条目名 `../evil.bin` | `FW_PKG_INVALID` / — |
| `bad_format_version.ehfw` | `format_version: 99` | `FW_PKG_UNSUPPORTED` / — |
| `bad_compression_ratio.ehfw` | 200 KiB 全零 Deflate（≈1000:1） | `FW_PKG_TOO_LARGE` / — |
| `ok_c6-n8.expected.json` | ok 用例的清单与镜像身份（**唯一**允许被断言的常量来源） | — |
| `cases.json` | 文件 → 期望错误码（Go/Python 共用契约） | — |
| `gen_vectors.py` | 生成器（独立第三实现） | — |

`bad_digest` 的两段含义：`firmwarepkg.Open` **只解出不比较**（评审 B-2），摘要比对发生在
上传 handler 第 5 步，所以它在 `open_error` 上必须成功、在 `handler_error` 上被拒。

## `cases.json` schema

```json
{
  "generator": "protocol/firmware-package/vectors/gen_vectors.py",
  "format": "ehome-firmware",
  "format_version": 1,
  "cases": [
    {
      "file": "ok_c6-n8.ehfw",
      "open_error": "",
      "handler_error": "",
      "expected_file": "ok_c6-n8.expected.json",
      "note": "两条目正例；ExtractArtifactTo 的 (size, sha256) 与清单一致"
    }
  ]
}
```

| 字段 | 语义 |
|---|---|
| `file` | 向量文件名（与本目录同级） |
| `open_error` | `firmwarepkg.Open` 期望的错误码；**空串 = 必须成功** |
| `handler_error` | 摘要比对那一步（规格 §5.2 步骤 5）期望的错误码；**空串 = 摘要必须一致** |
| `expected_file` | 仅 ok 用例：期望清单 + 镜像身份所在文件 |
| `note` | 人读说明（可跳过） |

两个错误字段在**每条用例里都必须出现**（空串也是值）。缺键会逼消费方写 `.get()` 兜底，
而兜底分支没人测；Go 的 struct 也分不清「缺键」与「空串」。`gen_vectors.py` 在生成与
`--check` 两条路径上都断言这一点，缺键直接报错退出。

`ok_c6-n8.expected.json`：

```json
{
  "manifest": { "format": "ehome-firmware", "format_version": 1, "packer": { },
                "artifact": { "name": "ehome_collector.bin", "size": 2256, "sha256": "..." },
                "identity": { "psram": false, "profile": "c6-n8" } },
  "image": { "chip": "esp32c6", "chip_id": 13, "flash_mb": 8, "version": "2.9.0",
             "project": "ehome_collector", "idf_version": "v6.1.0",
             "elf_sha256": "...", "size_bytes": 2256, "sha256": "..." }
}
```

**禁止手抄常量**：断言一律从 `cases.json` / `ok_c6-n8.expected.json` 读。2026-11-19 有一次
惨痛例子：生成器写定宽字符串时用了 `header[off:off+32] = b"v6.1.0"`，而 bytearray 的切片
赋值会**改变长度**（32 B 槽被 6 B 值收缩 26 B），镜像头整体错位；因为 chip/flash/version
都在 0x50 之前，解析「看起来是对的」，只有 `elf_sha256` 露馅。若消费方把当时的值抄进断言，
这次修复就会被断言挡住。

## 生成方法（固定输入）

合成镜像（**不是**真实构建产物：前 0xD0 字节之外没有意义，没必要把 1.4 MB 塞进仓库）：

```text
0x00  0xE9                image magic
0x03  0x30                spi_size 高半字节 3 => 8MB
0x0C  0x0D 0x00           chip_id u16 LE 0x000D => esp32c6
0x20  32 54 CD AB         app desc magic 0xABCD5432
0x30  "2.9.0"             version[32]
0x50  "ehome_collector"   project[32]
0x70  "00:00:00"          time[16]
0x80  "2026-11-19"        date[16]
0x90  "v6.1.0"            idf_ver[32]
0xB0  sha256("ehome-collector-synthetic-image")[:32]   app_elf_sha256[32]
0xD0  + 2048 B 确定性填充 -> 镜像 2256 B
```

```bash
# 重建全部向量（会改写 .ehfw / *.json，务必 git diff 看一眼）
python3 protocol/firmware-package/vectors/gen_vectors.py

# 只比对不写：向量是否仍可由生成器逐字节重建
python3 protocol/firmware-package/vectors/gen_vectors.py --check
```

`gen_vectors.py` **故意不复用** `tools/pack_firmware.py`，而是用最朴素的 `zipfile` 直接拼字节。
同一份实现只能证明「它和自己一致」；独立实现才能区分「打包器漂了」与「向量写错了」。

## 消费方

| 消费方 | 用法 | 读路径 |
|---|---|---|
| Go `backend/internal/firmwarepkg/vectors_test.go` | 逐条跑 `Open` / `ExtractArtifactTo` | `../../../protocol/firmware-package/vectors/` |
| Go `backend/internal/api/handler_ota_upload_test.go` | 直接上传 `ok_c6-n8.ehfw` | 同上 |
| Python `esp32-collector/tools/test_pack_firmware.py` | `pack_firmware.py --verify` 每个向量 | 经 `ROOT` 计算 |
| 人 / 运维 | 离线校验任意包 | `python3 esp32-collector/tools/pack_firmware.py --verify <pkg>` |

## CI

`.github/workflows/test.yml` 的 **backend job** 有一 step「Firmware package vectors + packer (Python)」，
内容就是下面两条（`working-directory` 覆盖 job 默认的 `backend/`）：

```bash
PYTHONDONTWRITEBYTECODE=1 python3 protocol/firmware-package/vectors/gen_vectors.py --check
PYTHONDONTWRITEBYTECODE=1 python3 -m pytest esp32-collector/tools/test_pack_firmware.py -q
```

为什么两条都要在 CI：
- `pytest`：Python 侧打包器 + 与服务端同规则的 `--verify`（含全部负例）；
- `--check`：向量必须能由生成器重建。否则「固定输入」只是句空话，向量可以被手改而没人发现；
- `PYTHONDONTWRITEBYTECODE=1`（等价 `python3 -B`）：不要在 `vectors/` 与 `tools/` 里留
  `__pycache__/`。虽然 `.gitignore` 已忽略，但没必要每次 CI 产生垃圾。

## 改动规则

1. **新增一条格式规则 → 同时加一个 vector**（并在 `cases.json` 写期望码），两侧测试都补；
2. 改向量二进制（尺寸/摘要）**必须先通知 Go 侧**再落盘：它的断言若已硬编码会一起红；
3. 不要手抄 size / sha256 / elf_sha256，一律从 `ok_c6-n8.expected.json` 读；
4. 提交前跑一遍 `gen_vectors.py --check` 与 `pytest esp32-collector/tools/test_pack_firmware.py -q`。
