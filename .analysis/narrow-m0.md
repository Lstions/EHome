# 窄复核 ③b · M0 存量字段值扫描（脚本 + 实跑）

> 执行：verifier（无状态窄复核）｜时间：2026-10-21 21:26–21:52 (+08:00)｜基线：worktree `field-resolver` HEAD `9354408a`（基于 main `d406dc2d`）
> 依据：`docs/设计/数据字段解析层-方案-2026-10-21.md` v0.2 §4.4 / §5.1 / §13.1 用例 8；`docs/评审/数据字段解析层-评审报告-2026-10-21.md` #6
> 交付物：本文件 + `.analysis/m0_scan.py`（只读脚本，含 `--selftest` 自检）。**未改任何生产代码 / 冻结契约 / 他人文件。**
> 证据等级：C=代码/脚本静态事实；T=本轮实跑（命令+输出摘要）；D=文档声明。冲突时 T>C>D。

## 0. 一句话结论

**四类 M0 来源在本机 PG 集群 5 个非模板库中全部 0 行 → 按纪律，这只能证明"此刻本机这些库没有存量值"，不能证明其他部署无风险**；分类器用 18 条合成用例（14 个值 + 4 类 conditions_json）自证正确（PASS），并用"改坏 MAX=64→63 必须红"的变异自证证明自检不是假绿；**补充扫描真实物理名词表**（`unified_data.sensor_name`：ehome 13 distinct/179,532 行、ehome_bench 29 distinct/22,466,147 行）**100% literal**——这是本轮唯一的非空分母，可支撑"物理名一侧行为不变"，但**不覆盖规则侧自由文本**。

---

## 1. 口径与只读安全（先自证目标，再解释数字）

| 项 | 口径 |
|---|---|
| 分母 | 本机 PG 集群**非模板库**（排除 `ehome_sim_*` 仿真库）：`ehome / ehome_audit / ehome_bench / ehome_test / postgres` 共 5 个 |
| 单位 | 一条**非 NULL 存储值** = 一个待分类字符串；`conditions_json` 按数组元素 `sensor_name` 展开（SO 逐值计数，不是"逐行"） |
| 四类来源 | ① `alert_rules.sensor_name` ② `automation_rules.trigger_sensor_name` ③ `automation_rules.conditions_json[].sensor_name` ④ `data_sources.category` |
| 时刻 | 2026-10-21 21:44（单库）/ 21:46（全库） |
| 只读强制（不是声明） | 连接级 `PGOPTIONS=-c default_transaction_read_only=on`，由 PostgreSQL 在事务层拒绝写。**实证**：`SHOW transaction_read_only` → `on`；同连接执行 `UPDATE data_sources ... RETURNING id` → `ERROR: cannot execute UPDATE in a read-only transaction`，事后查表 0 行变化 |
| 目标自证 | `current_database=ehome / current_user=ehome / public_tables=57`，且同库 `unified_data=179,532、edge_devices=2` → 打到的是正在服役的生产库，不是空库/错库 |

复跑命令：

```bash
cd /mnt/storage/WorkSpace/EHome/.agents/worktrees/field-resolver
PYTHONDONTWRITEBYTECODE=1 python3 .analysis/m0_scan.py --selftest   # 分类器自检
PYTHONDONTWRITEBYTECODE=1 python3 .analysis/m0_scan.py              # 生产库（ehome）
PYTHONDONTWRITEBYTECODE=1 python3 .analysis/m0_scan.py --all-dbs    # 部署分母：5 库
```

---

## 2. 生产实跑结果（T）

### 2.1 单库 `ehome`（生产）

| 来源 | 表行数 | 非 NULL 值数 | literal | special | legacy |
|---|---:|---:|---:|---:|---:|
| alert_rules.sensor_name | 0 | 0 | 0 | 0 | 0 |
| automation_rules.trigger_sensor_name | 0 | 0 | 0 | 0 | 0 |
| data_sources.category | 0 | 0 | 0 | 0 | 0 |
| automation_rules.conditions_json[].sensor_name | 0 | 0 | 0 | 0 | 0 |

`conditions_json` 解析统计：`{"rules_with_json":0,"elements":0,"invalid_json":0,"not_array":0,"empty":0,"no_sensor_name":0}`

**补充证据（明确不属于 §4.4 四类来源，只作真实词表对照）**：
`unified_data.sensor_name` **distinct=13 / rows=179,532 → literal=13、special=0、legacy=0**（按行加权 179,532/0/0）。
13 个词：`rainfall(175,620)`、`current/cycle_count/fet_status/nominal_capacity/protection_status/remaining_capacity/rsoc/software_version/temperature_1/temperature_2/cell_count/total_voltage(各 326)` —— 全是 `^[a-z][a-z0-9_]*$`，**没有 `temperature_*` / `@soc` / `virtual:*` / 大写 / 中文**。

### 2.2 全部署分母（5 库）

| 库 | public 表数 | 四类来源合计非 NULL 值 | literal | special | legacy | 补充：unified_data 词表 |
|---|---:|---:|---:|---:|---:|---|
| ehome（生产） | 57 | 0 | 0 | 0 | 0 | distinct=13 / rows=179,532 / all literal |
| ehome_audit | 53 | 0 | 0 | 0 | 0 | distinct=0 / rows=0 |
| ehome_bench（压测） | 55 | 0 | 0 | 0 | 0 | distinct=29 / rows=22,466,147 / all literal |
| ehome_test | 0 | 表/列不存在 | - | - | - | 表不存在 |
| postgres | 0 | 表/列不存在 | - | - | - | 表不存在 |

> **0 行的正确解读（必须写进汇总）**：0 行 = "本机 5 个库此刻没有待分类的规则/来源存量值"。它**不能**证明：① 客户现场/其他部署的库无风险；② 未来快照无风险；③ 固件驱动内联名 / `device_configs.parser.fields[].name`（方案 §1.2 的另一类自由文本）无风险。**分类器是否可信，由 §4 的合成用例负责，不由 0 行负责**（空库对分类器没有分辨力）。

---

## 3. 分类口径与边界裁决（写入脚本并可复算）

对齐方案 §4.4 三类处置，判定**顺序有意固定**：

```
trim（§5.1 规则 1）
 → 空串                        ⇒ legacy / empty
 → 特殊族形态（@ / _* / virtual:）⇒ special（syntax_ok 单列 true/false）
 → len > 64                    ⇒ legacy / too_long   （§4.4 明写"超长"归 legacy）
 → 不匹配 ^[a-z][a-z0-9_]*$    ⇒ legacy / charset
 → 否则                        ⇒ literal
```

| 边界输入 | 本脚本分类 | 裁决理由 |
|---|---|---|
| `temperature_*` | special（syntax_ok=true） | family 合法；升级后**标 needs_review、不自动生效**（§5.5，评审否决了 fail-open） |
| `temperature_*:MAX` | special（syntax_ok=false） | 属特殊族但聚合大写非法（§5.1 用例 7）。**仍归 special**：M0 的目的是"行为是否可能改变"，语法写错也应人工看，而非当普通字面量放过 |
| `@soc` | special（true） | semantic 合法 |
| `virtual:power` | special（true） | virtual 合法 |
| `virtual`（无冒号） | **literal** | §5.1 用例 13 明确按 literal |
| `cell_voltage_10` | **literal** | §5.1 用例 14：不得误判为 family（无 `_*`） |
| `Temperature` / `温度` / `a_b c` | legacy / charset | 大写、非 ASCII、空格（§4.4 legacy 定义） |
| 64 字符 `a...a` | literal | §5.1 用例 10 边界 |
| 65 字符 `a...a` | legacy / too_long | §5.1 用例 11 / §4.4 "超长" |
| 纯空白 `"   "` | legacy / empty | trim 后空 ⇒ 方案会报 `empty_selector`，存量需人工处置 |
| `conditions_json` 非法 JSON | 单列 `invalid_json`（不计入三分类） | 它本身是存量风险（保存期不校验，见 §5），不应被"0 个 sensor_name"掩盖 |

**两处需要 Lead/M1 拍板的口径差异（诚实标注，不擅自改契约）**：
1. **EBNF 严格性**：§5.1 文法 `prefix = lower, {lower|digit|_}, lower-or-digit` 严格读要求 prefix ≥ 2 字符，故 `a_*` 会被本脚本判 `special/malformed`；若 M1 实现允许单字符 prefix，需同步调整 M0 分类器（本机数据未命中该形态）。
2. **special 的"语法不合法"子类**：方案把"匹配 family/@/virtual 文法"定义为 special。本脚本把"含特殊标记但不合法"的 near-miss 也计入 special（用 `syntax_ok=false` 区分），**偏保守方向**（多报 review，不少报）。若 M0 只允许严格文法 special，则 `syntax_ok=false` 的行应改判 legacy——但那样会让 `Temperature_*` 这类值在升级后按字面量静默处理，不推荐。
3. `data_sources.category` 当前列宽 `VARCHAR(32)`（`models.go:344`），而 selector 上限 64（方案 §5.1 / M1 计划 32→64）。**推论**：该来源今天不可能出现 33–64 字符值；扩宽后才会出现 `too_long` 边界，M0 的 `too_long` 只对另外三源有现实意义。

**M0 只做分类，不做标记**：本轮遵守只读纪律，未写 `needs_review` 列/表；标记动作属 M1/M2（方案 §4.2/§14.1）。

---

## 4. 合成数据验证（分类器正确性，T）

`--selftest`：**18 条用例全 PASS（exit 0）**，失败时 exit 2（不会假绿）。

| # | 输入 | 期望 | 实际 | 结果 |
|---|---|---|---|---|
| 1 | `rsoc` | literal | literal | PASS |
| 2 | `cell_voltage_10` | literal | literal | PASS |
| 3 | `virtual` | literal | literal | PASS |
| 4 | 64×`a` | literal | literal | PASS |
| 5 | 65×`a` | legacy/too_long | legacy | PASS |
| 6 | `temperature_*` | special(true) | special(true) | PASS |
| 7 | `temperature_*:max` | special(true) | special(true) | PASS |
| 8 | `temperature_*:MAX` | special(false) | special(false) | PASS |
| 9 | `@soc` | special(true) | special(true) | PASS |
| 10 | `virtual:power` | special(true) | special(true) | PASS |
| 11 | `Temperature` | legacy/charset | legacy | PASS |
| 12 | `温度` | legacy/charset | legacy | PASS |
| 13 | `"   "` | legacy/empty | legacy | PASS |
| 14 | `a_b c` | legacy/charset | legacy | PASS |
| 15 | `[{"sensor_name":"temperature_*"},{"sensor_name":"@soc"}]` | names=2/status=ok | 同 | PASS |
| 16 | `not-a-json` | invalid_json | invalid_json | PASS |
| 17 | `[{"sensor":"humidity"}]` | ok_with_missing_sensor_name | 同 | PASS |
| 18 | `[]` | empty | empty | PASS |

**变异自证（证明自检能变红，而不是只会在绿上打勾）**：
`sed 's/MAX_SELECTOR_CHARS = 64/MAX_SELECTOR_CHARS = 63/' m0_scan.py > /tmp/m0_mut.py && python3 /tmp/m0_mut.py --selftest`
→ 用例 4（64×a）由 literal 变 legacy，输出 `**FAIL**`、**exit 2**；恢复原文后 exit 0。自检对分类器有分辨力。

---

## 5. 交叉发现（扫描过程中撞到、与 M0 结论相关）

1. **conditions_json 非法 JSON 是"零 sensor_name"掩盖下的存量风险**：本轮 `rules_with_json=0`，但仓库侧已复核过"保存期不校验 conditions_json、运行期非法 JSON 只 warn 且规则永不触发"（02-automation 复核 §4.6）。M0 脚本把它单列 `invalid_json`——**建议 M1 把 `ParseConditions` 前置到保存期**，否则未来存量仍可能以"0 个可分类值"的形式躲过 M0。
2. **四类来源 0 行与既往复核一致**：01 复核（昨 13:23）`data_sources=0`、本轮 `alert_rules=0 / automation_rules=0 / data_sources=0`（21:44）——两次独立采集同一结论，但仍只是"本机这一刻"。
3. **真实物理名词表全 literal**（§2.1/§2.2）：这为方案 §5.5"literal 行为不变"提供了非空证据；同时给 M1 一个可直接复用的真实词表基准（13/29 个名字）。

---

## 6. 未复核 / 限制（诚实边界）

| # | 未做 | 原因/影响 |
|---|---|---|
| 1 | 客户现场/其他部署的库 | 无访问；**0 行不能外推**，需在交付前对每个部署重跑本脚本 |
| 2 | `device_configs.parser.fields[].name` 与固件驱动内联名 | 不属 §4.4 四类来源（属 M1 的 ConfigParser 名称校验改进项）；它们是"物理名从哪来"的另一入口，建议 M1 纳入 |
| 3 | `needs_review` 落库标记 | 纪律=只读；标记属 M1/M2 |
| 4 | 未来快照 | 本报告是时刻性测量，重跑即可 |
| 5 | 前端口径（规则表单/数据源表单输入是否已有前端预校验） | 本轮窄复核范围为 M0 扫描，未做前端核验 |
| 6 | ehome_bench 22M 行数据非真实客户命名分布 | 压测数据词表同质，只能证明"该库全 literal" |

---

## 7. 结论对 M0 验收的贡献

- 方案 §13.1 用例 8 要求"构造 literal / special / legacy 三类行，扫描后分类正确，legacy 走精确匹配"→ **已由 18 条自检覆盖并 PASS（含变异自证）**，分类侧可验收。
- 方案 §15 D0 要求"M0 报告产出"→ **本报告即为该产物**；其中"本机 5 库 0 行"是事实，**不构成"存量无风险"的结论**。
- 方案 §15 验收 5 "`data_sources.category` 已扩宽" → 现状仍 `size:32`（M1 项，未在本轮改动）；提醒 M1 迁移时 **M0 脚本无需改判据、只需重跑**（64 字符边界已内建）。
- 建议 Lead 在汇总里引用本报告时保留完整分母句："本机 PG 5 库（ehome/ehome_audit/ehome_bench/ehome_test/postgres）四类来源 0 行；真实物理名词表 13/29 distinct 全 literal；分类器 18 条合成用例 + 变异自证 PASS。"

---

## 8. 附：本报告全部命令

```bash
cd /mnt/storage/WorkSpace/EHome/.agents/worktrees/field-resolver
python3 .analysis/m0_scan.py --selftest
python3 .analysis/m0_scan.py
python3 .analysis/m0_scan.py --all-dbs

# 只读强制实证（写语句必须被库拒绝；本轮探针已执行且 0 行变化）
docker exec -e 'PGOPTIONS=-c default_transaction_read_only=on' ehome-postgres \
  psql -X -U ehome -d ehome -Atq -c "SHOW transaction_read_only;"
docker exec -e 'PGOPTIONS=-c default_transaction_read_only=on' ehome-postgres \
  psql -X -U ehome -d ehome -Atq -c "UPDATE data_sources SET category='__m0_probe__' WHERE id=1 RETURNING id;"
# → ERROR: cannot execute UPDATE in a read-only transaction

# 真实词表补充证据
docker exec -e 'PGOPTIONS=-c default_transaction_read_only=on' ehome-postgres \
  psql -X -U ehome -d ehome -Atq -F ' | ' \
  -c "SELECT sensor_name, count(*) FROM unified_data WHERE sensor_name IS NOT NULL AND sensor_name <> '' GROUP BY 1 ORDER BY 2 DESC;"
```
