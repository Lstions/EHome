# 构建期静态内存预算门禁（WS-G）

- 脚本：`tools/mem_budget_check.py`
- 阈值：`tools/mem_budget.json`
- 基线依据：`docs/验证/内存与吞吐基线-2026-10-05.md`

## 1. 本地怎么跑

```bash
cd esp32-collector

# 方式 1（推荐）：BUILD_ROOT 显式指定构建根（build_firmware.sh 使用同一变量）。
# BUILD_ROOT 一旦设置就是唯一来源：该 profile 的 map 不存在会报错 exit 2，
# 不会静默回退到仓库里的旧 map。
export IDF_PATH=/home/sun/env/esp-idf
export IDF_TOOLS_PATH=/home/sun/.espressif
export IDF_PYTHON_ENV_PATH=/home/sun/.espressif/python_env/idf6.1_py3.14_env
source "$IDF_PATH/export.sh"
BUILD_ROOT=/tmp/ehome_memgate_build \
  python3 tools/mem_budget_check.py --profile s3-n16

# 方式 2：不设 BUILD_ROOT，自动在 build*/.fwbuild*/ 下找该 profile 最新的 map
python3 tools/mem_budget_check.py --profile s3-n16
```

也可显式指定某次构建的 map，便于对照历史基线：

```bash
python tools/mem_budget_check.py --profile c6-n16 \
    --map /mnt/storage/WorkSpace/EHome/esp32-collector/build61/c6-n16/ehome_collector.map
```

常用开关：

| 开关 | 用途 |
|---|---|
| `--report-only` | 只打印报告，永远 exit 0；用于调阈值/记录基线 |
| `--strict` | 把 `known_pending` 里的已知缺口也按 FAIL 拦截；task-3 落地后应改用此模式 |
| `--top N` | 调整组件 top-N（默认 10） |
| `--thresholds <json>` | 用临时阈值文件跑红/绿自证，不动仓库阈值 |

退出码：`0` 通过（或只有已知待办）、`1` 超限、`2` 无法验证（map 缺失 / esp-idf-size 不可用）。

## 2. 门禁口径

| 检查项 | 数据来源 | 语义 |
|---|---|---|
| `DIRAM used` | `idf_size.py --format json2` 的 `DIRAM.used` | `.dram0.bss + .dram0.data + IRAM 别名段`，按组件 top-N 归因 |
| `DIRAM remain` | `DIRAM.free` | C6 硬门禁 ≥ 20 KiB |
| `IRAM used/remain` | `IRAM.used/free` | S3 IRAM 硬门禁 remain ≥ 4 KiB（当前 task-3 未落地，见下） |
| 静态任务栈 | map 文本扫描 `.dram0.bss` 中符号名含 `stack` 的静态数组 | FreeRTOS `xTaskCreateStatic` 的 `.bss` 栈；**已含在 DIRAM used 内**，单列只为了看清账 |
| 组件 top-N | `esp_idf_size --archives --format json2` | `.bss/.data/别名 .text` 聚合到组件名 |

`--report-only` 与 `--strict` 之外一律按 `mem_budget.json > profiles.<profile>` 判定；未列出的 profile 用 `defaults`。

## 3. 阈值 JSON 结构

```jsonc
{
  "defaults": {
    "iram_remain_min": 4096,   // S3 IRAM 硬地板
    "dir_remain_min": 20480,   // C6 DIRAM 硬地板
    "top_n": 10
  },
  "profiles": {
    "s3-n16": {
      "dir_used_max": 188500,
      "iram_remain_min": 4096,
      "known_pending": {
        "iram_remain_min": "IRAM 100%：task-3 的 IRAM→flash 未落地"
      }
    }
  }
}
```

- `*_max`：超过即 FAIL。
- `*_min`：低于即 FAIL。
- `known_pending.<key>`：该检查 FAIL 时标 `PENDING`，默认不阻塞、`--strict` 时阻塞；值必须写清缺陷 ID / 方案章节 / 何时移除。
- 阈值改动必须同步 `docs/验证/内存与吞吐基线-2026-10-05.md` 的对照表；否则下一次回归没有可解释的基准。

## 4. 接入 `build_firmware.sh`（建议插入位置）

`build_firmware.sh` 当前 owner 是 task-1，本任务**未直接改它**，避免抢文件。建议由 task-1/集成方在 `build_profile()` 的 Wi-Fi ISR 检查之后、函数返回之前插入下面这段（与现有 `check_iram_isr_safety.py` 的调用方式一致，直接复用已解析的 IDF python）：

```bash
    # ---- WS-G: 静态内存预算门禁 ----
    # 脚本解析本 profile 的 ehome_collector.map，校验 DIRAM/IRAM 预算与
    # IRAM/DIRAM remain；超限 exit 1，让构建失败而不是把超预算固件推给设备。
    # 阈值在 tools/mem_budget.json，按 profile。
    local _budget="$PROJECT_DIR/tools/mem_budget_check.py"
    if [[ -f "$_budget" ]]; then
        echo "==> Checking static memory budget ($profile)"
        if ! "${IDF_PY_CMD[0]}" "$_budget" \
                --profile "$profile" \
                --map "$build_dir/ehome_collector.map"; then
            echo "ERROR: $profile: static memory budget check failed (see above)" >&2
            return 1
        fi
    fi
```

插入后：

- `./build_firmware.sh s3-n16` 在编译与 IRAM/ISR 检查之后自动跑预算门禁；
- `all` 分支逐个 profile 生效；
- `s3p-n16` 目前只有 `known_pending` 的 IRAM 缺口，默认不阻塞、`--strict` 可拦；
- 脚本本身也可独立跑（CI 不跑完整 `idf.py build` 时，用 `--map` 指向缓存 map 即可）。

## 5. 自证（门禁真的会红）

本任务用临时阈值文件对脚本做过变异自证，构建/脚本退出码如下：

```bash
# 1) 绿：真实阈值 + 真实 map
BUILD_ROOT=/tmp/ehome_memgate_build python3 tools/mem_budget_check.py --profile s3-n16
#   -> RESULT: PASS (1 项已知待办)  exit 0

# 2) 红：把 dir_used_max 改成 1000（临时阈值文件，不改仓库）
python3 tools/mem_budget_check.py --profile s3-n16 \
    --thresholds /tmp/mem_budget_tiny.json
#   -> FAIL DIRAM used 163840 <= 1000 / FAIL IRAM remain 0 >= 4096
#   -> RESULT: FAIL (2 项超限)  exit 1

# 3) 无法验证：显式 BUILD_ROOT 下的 map 不存在
BUILD_ROOT=/tmp/nope python3 tools/mem_budget_check.py --profile s3-n16
#   -> ERROR: 找不到 s3-n16 的 ehome_collector.map   exit 2

# 4) 严格：task-3 的 IRAM 缺口在 --strict 下拦
python3 tools/mem_budget_check.py --profile s3-n16 --strict
#   -> PENDING IRAM remain 0 >= 4096 ... exit 1
```

> 方法论 §2：一个永远为真的门禁等于不存在。上面第 2 条证明阈值判定链路能红；第 3 条证明“无法验证”不会被当成通过（exit 2 ≠ 0）。task-3 把 IRAM 腾出 4 KiB 后，应删除对应 `known_pending`，让第 4 条也变绿。
