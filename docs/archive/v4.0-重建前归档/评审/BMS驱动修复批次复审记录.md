# 边缘设备 BMS 驱动修复批次复审记录

- 日期：2026-08-16
- 复审对象：工作树未提交 diff（backend/internal/drivers/jiabaida.go +1230、jiabaida_test.go +957、batch_envelope.go/_test.go 新增、deviceaction/definition_test.go +23）
- diff 构成：V19 写能力批次（前序）+ P0 修复批次 + P0-1/P1 重构批次
- 复审方式：双路独立并行（deepseek-v4-flash × 2，只读审查），主 Agent 交叉核验
- 源码基线：`git show HEAD:backend/internal/drivers/jiabaida.go`（行为等价性基准）

## 修复内容摘要

| 批次 | 项 | 内容 |
|---|---|---|
| A | P0-3 | jiabaidaExpectZeroAck 追加 verifyJiabaidaChecksum；set_mos_policy/bms_restart 手工 ACK 检查替换为 helper |
| A | P0-2 | set_mos_policy fet_status 位级读回对账（极性：写帧 1=关 / fet 1=开，取反映射；仅查 bit0/bit1） |
| A | P2 | parse0xAA <24B、parse0x05 空输入 → ErrDataTooShort |
| B | P0-1 | F2/F3 写后对账 bytes.Equal → jiabaidaEqualOnSpans 已声明字段区间比较（保留区与 CRC 豁免，截断仍 fail-closed） |
| B | P1-2 | jiabaidaField 描述符表统一 compile/parse/schema/spans 四处 |
| B | P1-1 | decodeBatchPlanEnvelope 移入 drivers/batch_envelope.go（deviceaction 已 import drivers，反向成环故留包内共享） |
| B | P1-3 | 读帧字面量收敛 jiabaidaReadFrame(cmd)，11 个黄金向量测试锁定 |

## 复审 A：安全不变量（A1-A7 全 PASS）

| # | 审查项 | 结论 | 关键证据 |
|---|---|---|---|
| A1 | ACK 帧校验和真实验证 | PASS | LEN=0 ⇒ 补和 0x0000；Python 逐位复算 DD E1 00 00 00 00 77 verify=true / FF FF verify=false；负路径测试含好坏对照 |
| A2 | fet 对账极性 | PASS | ChargeClosed⇒期望 fet bit0=0；高位掩码不参与；4 向极性表+mismatch+缺失三测试真实 |
| A3 | spans 不漏比 | PASS | 程序化复算：F2→[[0,50]] 无洞、F3→[[0,15],[20,31],[48,49]] 全覆盖；len 不等即失配 |
| A4 | CRC 豁免合理性 | PASS | 帧级校验和仍过 verifyJiabaidaChecksum，仅字节对账豁免 CRC 区间；四象限测试齐全 |
| A5 | 信封解码边界与固件互逆 | PASS | bus_worker.c 编码逐字节互逆；固件 CMD_BATCH_MAX_STEPS=8 与 Go >8 拒绝一致 |
| A6 | 新测试非恒真 | PASS | 负路径全部断言真实 err 且信息匹配；辅助构造实时算校验和 |
| A7 | 表驱动 vs 旧手写换算 | PASS | 5000 组随机块独立 Python 仿真与 HEAD 逐位一致（×10、/100、/10-273.15、/10） |

**VERDICT: APPROVED**

## 复审 B：行为等价性（20 项全 PASS）

- parse0x03/0x04/0x0F/0xF6/0xA2 与 HEAD 逐字节一致；checksum/verify/CRC16Modbus diff 为空
- parse0xF2/F3 表驱动重写经 5000 组随机块仿真证明数值逐位一致（23/15 字段同名同序）
- GetCommandTemplates 五条 WriteData 字节内容全同（生成方式改为 jiabaidaReadFrameHex）
- 工厂模式三帧逐字节一致（仅加"禁止重构"注释）
- 合法行为差异 8 项（D1-D8）全部为任务许可的防御性修复或纯新增，均被测试锁定
  - D7（hex 大写→小写）：主 Agent 逐个追踪 5 处消费方（manifest_codec:229/242/303、driverTemplateMatches、sender_snapshot:236、consumers_heavy、前端）确认全部 ToUpper 归一或 hex 解码，无风险

**VERDICT: APPROVED**

## 终验（主 Agent）

- go build ./... ✅；go test ./internal/... 24 包 1157 PASS ✅；make test 全仓（含前端覆盖率门禁）✅
- make lint-backend（go vet）✅；gofmt 干净 ✅
- 11 个黄金向量主 Agent 亲算核对（A2→FF5E 修正了任务书笔误值 FF5D）

## 复审后源码变化

无。复审完成后源码未再变动（D7 消费方核验与文档写入均不触碰源码），本记录持续有效。

## 遗留改进建议（非阻断，复审 A 提出）

1. jiabaidaResponseData 读回帧校验和负路径测试（当前仅 ACK 校验和有负测试）
2. fet 高位锁定测试（fet=0x80|期望低位，钉死"高位不参与判断"）
3. decodeBatchPlanEnvelope 可考虑返回 kind 字段（[]struct{Kind byte; Data []byte}）
4. verifyJiabaidaChecksum 补 LEN=0 补和语义注释/独立单测
5. V19 协议 E1/fet_status 字节表原文摘录固化进 docs（钉死 A2 依据）
