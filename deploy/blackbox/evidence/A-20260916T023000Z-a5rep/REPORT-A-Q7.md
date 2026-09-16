# 子代理 A · 第 2 轮报告：Q7 镜像可重复构建

> 执行：2026-09-16 | 探针：`deploy/blackbox/probes/07-reproducible.sh`
> 基线 commit：`4698d230`（被测内容指纹 `e4b09b9393dc93194c7e64d30b0ad592`，CONTRACT 规则 A 期间无变动）
> 结论：**Q7 通过 —— 同 commit 连续两次构建的产物文件名集合与 index.html 字节 hash 均一致（确定性）**

---

## 0. 一句话结论

在同一 commit 下、加 `--no-cache` **全量重建**两次（`ehome-bb-img:rep1` / `rep2`），
`index.html` 引用的资产文件名集合**完全一致**，整棵 `/app/static/dist` 的文件名集合一致，
`index.html` 的 sha256 **逐字节相同**。⇒ **本仓库当前不存在构建不确定性**（就本断言范围而言）。

---

## 1. 断言结果（13 PASS / 0 FAIL）

证据：`evidence/A-20260916T023000Z-a5rep/07-assertions.tsv`

| 断言 | 结论 | 关键实测值 |
|---|---|---|
| A1 | PASS | rep1 exit=0，rep2 exit=0（均 `--no-cache` 全量重建） |
| G1a | PASS | asset 引用数 rep1=10 / rep2=10（守卫 ≥2） |
| G1b | PASS | dist 文件数 rep1=100 / rep2=100（守卫 ≥5） |
| G1c | PASS | 两次 index.html 均存在，均 1942 bytes |
| G2a | PASS | **非空转自证**：清空清单后被判出差异（10 行） |
| G2b | PASS | **非空转自证**：sha 改为 FILE_REMOVED 被检出 |
| G3 | PASS | 幂等自证：同一清单自比较判一致 |
| G4 | PASS | 正控：注入已知不同清单被判出差异 |
| **R1** | **PASS** | **两次 asset 文件名集合一致（各 10 个，diff 为空）** |
| R1b | PASS | 整棵 `static/dist` 文件名集合一致（各 100 个文件，含全部懒加载 chunk） |
| R1c | PASS | index.html sha256 一致 = `e02f3822…e6a91` |
| R2 | PASS | 镜像体积均为 90456890 bytes |
| C1 | PASS | 临时镜像零残留 |

**镜像 id 不同但内容一致**：`rep1=b3ad1fa7…`、`rep2=5e5f00bf…`；而 `index.html` sha 完全相同。

### 1.1 追加：深比对（`BB_DEEP=1`）与镜像级不确定性的归因

证据：`evidence/A-20260916T030500Z-a5deep/`（14 PASS / 0 FAIL）

| 断言 | 结论 | 实测 |
|---|---|---|
| G1d | PASS | 两次各取到 101 个文件摘要（守卫 ≥5） |
| **R1d** | **PASS** | **`/app` 内 101 个文件逐个 sha256 全部一致**（`07-app-sha.diff` 为 0 字节）⇒ 产物**字节级确定** |

**结论修正（如实收紧）**：

- **产物层（docker 镜像内的 /app 内容）：字节级确定** —— 连 48MB 的 Go 二进制都两次同 sha
  （`0736b49dfe14a02b04a892e9…`，且在相隔约 1 小时、不同时间构建的镜像间也一致）。
- **镜像层（image id / Size / 层 digest）：不确定**。三次运行实测：

| run | rep1 Size | rep2 Size | 差值 |
|---|---|---|---|
| `A-20260916T023000Z-a5rep` | 90456890 | 90456890 | 0 |
| `A-20260916T025500Z-a5final` | 90456895 | 90456872 | −23 B |
| `A-20260916T030500Z-a5deep` | 90456883 | 90460983 | **+4100 B** |

- **归因证据**：镜像内文件 mtime 跟随**构建墙钟时间**，而内容 sha 不变 ——
  `warm`（01:17:32 构建）与 `mut01`（02:19:03 构建）的 `ehome-server` sha 完全相同
  （`0736b49d…`），但 mtime 分别为 `01:17:32` / `02:19:03`。
  ⇒ **变化的不是字节内容，而是层 tar 中的 mtime / 层元数据**，这正是
  `SOURCE_DATE_EPOCH` 这类可复现构建机制要解决的问题。
- **对"可重复构建"意味着什么**：
  - "同一 commit 产出**相同的应用产物**" —— **成立**（R1/R1b/R1c/R1d 全绿）。
  - "同一 commit 产出**逐位相同的镜像**（相同 image id / digest）" —— **不成立**，且
    **这不是本仓库的缺陷**，而是"未设置 `SOURCE_DATE_EPOCH` / 未做 layer 归一化"的
    通用现象。若 CI 需要按 digest 做缓存/校验，会因此每次 miss —— 属**已知配置项**，
    非构建不确定性。

## 2. 复现命令

```bash
export PATH=/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH; export HOME=/home/sun
cd /home/sun/workspace/EHomeSystem/deploy/blackbox
bash probes/07-reproducible.sh
# 开发自测（契约规则 B）：
BB_PROJECT=ehome-bb-dev-a5 BB_HOME_PORT=18091 BB_DB=ehome_bbdev_a5_1 \
  bash probes/07-reproducible.sh
```
构建使用 `--build-arg GOPROXY=https://goproxy.cn,direct` 加速（脚本内用 `--no-cache` 保证两次独立）。

## 3. 变异自证（缺陷先红）

### 3.1 probes/01 变异 ⇒ 必红（硬要求，已完成）
把 Dockerfile 的 `COPY --from=frontend-builder /app/dist ./static/dist` 注释掉后重建：
- **变异落地自证**：`grep -c '# MUTATED(01): COPY …'` = **1**（确认脚本真的改了东西）
- 结果：**4 条 FAIL** —— `A3`(index.html 缺失) / `G1b`(0 条引用) / `G1c`(/app 仅 1 个文件) / `G1d`(无 js bundle)
- **恢复**：Dockerfile md5 `f982fc9f…` → `2171b7a9…`，与变异前**逐字节一致**（M1 PASS）
- 证据：`evidence/A-mut01-01/01-assertions.tsv`、`01-image-contract.log`、`01-dockerfile.{before,mutated}.txt`

### 3.2 probes/07 变异 ⇒ 可测差异（已完成）
`BB_MUTATION=drop-frontend-copy` 重建变异树：
- `R3` PASS：变异镜像 `INDEX_PRESENT=0`、引用数 0 ⇒ 该破坏**确实改变了被测量**
- `M1` PASS：Dockerfile `c7de3cf6…` → `2171b7a9…` 字节级还原
- 证据：`evidence/A-20260916T023500Z-a5mut/`

## 4. 重要发现：A4 存在"空转绿"风险（已用分母守卫挡住）

变异实测暴露：产物整体缺失时，**A4 报的是 PASS** ——
`A4 index.html 引用的 0 个静态资源在镜像内逐个存在（FOUND=0 MISSING=0）`。

即：**"逐个存在"型断言在没有分母守卫时会空转成绿**。本例是靠 `G1b`(引用数≥2) 与
`G1c`(/app 文件数≥5) 才整盘变红。⇒ 分母守卫不是装饰，而是这类断言的**必要**组成。

**对 07 的直接影响（这是 07 加 G2 的原因）**：若被测产物整体缺失，两次构建都缺、
两份清单都空，**朴素比较器（只做两次 diff）会得出"一致 ⇒ 绿"的假绿**。实测确认：

```
$ : > empty1.txt; : > empty2.txt; diff -q empty1.txt empty2.txt  ⇒ SAME（假绿）
```

因此 07 显式加入 **G2 非空转自证**：先证明"比较器对产物缺失会红"，R1 的绿才可信。

## 5. 未覆盖项 / 诚实边界

1. **只测了静态资源文件名集合与 index.html 字节**，未做全镜像 bit-for-bit 复现
   （未用 `SOURCE_DATE_EPOCH`；CI 层面的 digest 级复现不在本轮范围）。
2. **未验证跨机器/跨架构复现**（仅本机 linux/amd64）。
3. **两次构建镜像 id 不同**（层元数据含时间戳）—— 未断言 id 一致，**也不建议**断言。
4. **未在清理流程中验证 DB/网络**（本探针不创建容器、不创建库，故无 DB 残留面）。
5. **后台 chunk 内容未逐字节比较**：R1b 只比较**文件名集合**，未比较每个 chunk 的 sha。
   即"新增/缺失/改名"能抓到，但"同名不同内容"抓不到。
6. 构建期间工作树有他人未跟踪文件（`06-wiring.sh` 等），**不属于被测量**；
   本探针用 `git ls-files` 指纹守护 `backend/frontend-shared/Dockerfile`，确认基线未动。

## 6. 残留与清理

- 07 的临时镜像 `rep1/rep2/rep-mut` 结束时由 `trap cleanup` 自动删除（C1 PASS 核对）
- 本探针**不创建**容器/卷/网络/数据库 ⇒ 无 `ehome-bb*` 容器残留
- 注意：仓库中存在 01 探针遗留的镜像 `ehome-bb-img:{warm,recheck,606d8e6f,mut01}`
  （非 07 产物，属调试缓存，见 §7）
