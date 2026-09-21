/**
 * 受控操作「不可用」的可行动指引（P0-b 的收口）。
 *
 * 为什么需要它（2026-09-21 生产缺陷 + 主控提醒）：
 * 修复前，设备详情页对 9 个不可用操作只给一句「暂不可用操作（9）」+ 后端 reason，
 * 用户据此**无法行动**：既分不清「等一下就好」和「坏了」，也没有任何可点的入口。
 * 更危险的是「只修自己设的那个面」—— capability_stale 只是**一个** reason_code，
 * 同一页面还会出现 protocol_unverified / hardware_evidence_required /
 * edge device or node is unavailable 等等。它们必须逐个判定：
 *   - 哪些是用户自己能恢复的（selfHealable=true，页面给「立即恢复」按钮）；
 *   - 哪些只能等开发/产品（selfHealable=false，页面必须说明「无需操作」而不是让用户白点）。
 *
 * 分母（本文档冻结的 reason_code 全集）来自后端两个来源：
 *   backend/internal/commandexec/service.go  —— gateResult.reasonCode 与 Catalog 的
 *     `item.ReasonCode = "capability_stale"` 覆盖；
 *   backend/internal/drivers/*.go             —— ControlAction.AvailabilityCode。
 * 复跑命令（在仓库根执行）：
 *   grep -rhoE 'ReasonCode = "[a-z_]+"' backend/internal/commandexec/service.go
 *   grep -rhoE 'AvailabilityCode: "[a-z_]+"' backend/internal/drivers/*.go | sort -u
 * 与之配套的源码级门禁见 utils/__tests__/ActionAvailabilityCoverage.spec.ts ——
 * 后端新增任何 reason_code 而前端没有对应指引时，那条门禁会变红。
 */
import type { EffectiveAction } from '@/api/deviceOperation'

/** 后端 Catalog 里 reason_code 的**冻结全集**（含空串：多数门禁不带机器码）。 */
export const AVAILABILITY_REASON_CODES = [
  '',
  'capability_stale',
  'command_engine_gate',
  'protocol_unverified',
  'hardware_evidence_required',
] as const

/**
 * 后端 reasonForGate / AvailabilityReason 产生的**冻结用户可见文案**（UI 契约）。
 * 空 reason_code 时前端只能按这段文案分类，因此它同样是契约的一部分：
 * 改动它会让这里的分类落到兜底分支（门禁会因此变红）。
 */
export const AVAILABILITY_REASONS = [
  'device control v2 is disabled',
  'action is not enabled for rollout',
  'action requires the future high-risk command engine',
  'edge device or node is unavailable',
  'action channel is unavailable',
  'ChannelCmdV2 capability is unavailable or stale',
  'peripheral config is unavailable or disabled',
  "action exceeds current node capability",
  'action is unavailable',
] as const

/**
 * 能力快照窗口的**单一事实来源**（前端侧）。
 *
 * 为什么不把「5 分钟」写进文案：阈值本轮就从 5 分钟改成了 15 分钟。
 * 文案里硬编码数字，会在阈值调整后**悄悄开始说谎** —— 而用户看到的正是那句话。
 * 后端常量：backend/internal/commandexec/channel_cmd_v2_transport.go 的
 * `MaxCapabilityAge = resourceReportInterval + capabilityReportMargin`；
 * 前端只在文案里做定性描述（「一下子」「通常几秒」），不重复数字。
 */
export const serverCapabilityWindowCopy = '服务端手上最新的能力快照已超过有效期'

export interface UnavailableGuidance {
  /** 分类键：reason_code（空码时退化为 reason 文案），用于门禁与测试断言。 */
  key: string
  /** 一句话说明「为什么不能做」。 */
  summary: string
  /** 一句话说明「你能做什么」。必须可执行，不得是「请联系管理员」这类空话。 */
  action: string
  /** 页面是否应当提供「立即恢复」（只有能靠一次 QueryResources 解决的才是 true）。 */
  selfHealable: boolean
  /**
   * 后端给出的**原文**（reason 文案）。
   *
   * 为什么要保留它：分类器给的是「人话版」解释，但原文里可能带具体细节
   * （某个 pin、某个通道号、某次同步的中间状态）。
   * 把它丢掉就等于用一句更顺的文案替换掉证据 —— 那正是本轮要根治的
   * 「用户只看到一句不可用」。页面把它作为次要行原样展示。
   */
  raw: string
}

/**
 * 把一次不可用分类成可行动指引。
 *
 * 判定顺序刻意是「先机器码、后文案」：机器码稳定，文案是给人看的、可能被润色。
 * 兜底分支**必须**存在且明确标注：新出现的后端原因不能被静默吞掉，
 * 它会以 selfHealable=false + 原文回显的方式呈现，并由覆盖门禁在 CI 里拦下。
 */
export function classifyUnavailable(item: EffectiveAction): UnavailableGuidance {
  const code = item.reason_code ?? ''
  switch (code) {
    case 'capability_stale':
      return {
        key: code,
        summary: '节点还没来得及上报最新的能力快照，服务端暂时不敢下发指令。',
        action: '点「立即恢复」让服务端主动向节点要一次上报，通常几秒内恢复。',
        selfHealable: true,
        raw: item.reason || '',
      }
    case 'command_engine_gate':
      return {
        key: code,
        summary: '该动作需要尚未启用的高风险指令引擎。',
        action: '无需操作：这是产品放行节奏问题，需开发启用后才会出现。',
        selfHealable: false,
        raw: item.reason || '',
      }
    case 'protocol_unverified':
      return {
        key: code,
        summary: '该动作的报文格式还没有真机验证过，按 fail-closed 原则被冻结。',
        action: '无需操作：需开发在真机上冻结协议与读回证据后放行。',
        selfHealable: false,
        raw: item.reason || '',
      }
    case 'hardware_evidence_required':
      return {
        key: code,
        summary: '该动作只允许在开发实机取证，生产环境按设计禁止使用这条路径。',
        action: '无需操作：生产请改用对应的 bounded 工作流动作。',
        selfHealable: false,
        raw: item.reason || '',
      }
  }

  switch (item.reason) {
    case 'device control v2 is disabled':
      return {
        key: 'dispatch_disabled',
        summary: '服务端的受控操作下发开关处于关闭状态。',
        action: '无需操作：需运维开启 device control v2。',
        selfHealable: false,
        raw: item.reason || '',
      }
    case 'action is not enabled for rollout':
      return {
        key: 'not_enabled',
        summary: '该动作没有在灰度放行名单里。',
        action: '无需操作：需开发按灰度节奏放行。',
        selfHealable: false,
        raw: item.reason || '',
      }
    case 'action requires the future high-risk command engine':
      return {
        key: 'command_engine_gate',
        summary: '该动作需要尚未启用的高风险指令引擎。',
        action: '无需操作：这是产品放行节奏问题，需开发启用后才会出现。',
        selfHealable: false,
        raw: item.reason || '',
      }
    case 'action exceeds current node capability':
      return {
        key: 'definition_fits_capabilities',
        summary: '该动作的报文长度或超时超出了节点当前上报的能力上限。',
        action: '无需操作：需开发按节点实际上报的能力调整动作参数。',
        selfHealable: false,
        raw: item.reason || '',
      }
    case 'action is unavailable':
      return {
        key: 'unclassified_gate',
        summary: '服务端的可用性门禁没有给出更具体的原因。',
        action: '无需操作：请把这条原文反馈给开发，以便补上具体的拒绝原因。',
        selfHealable: false,
        raw: item.reason || '',
      }
    case 'edge device or node is unavailable':
      return {
        key: 'node_unavailable',
        summary: '节点当前不在线（或设备被停用），服务端无法与它通信。',
        action: '检查节点电源与网络；节点恢复上线后本页会自动刷新状态。',
        selfHealable: false,
        raw: item.reason || '',
      }
    case 'action channel is unavailable':
      return {
        key: 'channel_unavailable',
        summary: '设备所在通道未启用、未在节点上生效，或配置清单尚未同步完成。',
        action: '到节点详情的通道管理确认该通道已启用，必要时触发一次配置同步。',
        selfHealable: false,
        raw: item.reason || '',
      }
    case 'ChannelCmdV2 capability is unavailable or stale':
      return {
        key: 'capability_unavailable',
        summary: '节点尚未上报可用的指令引擎能力，或上报的能力不完整。',
        action: '点「立即恢复」让服务端主动向节点要一次上报。',
        selfHealable: true,
        raw: item.reason || '',
      }
    case 'peripheral config is unavailable or disabled':
      return {
        key: 'periph_config_unavailable',
        summary: '该 GPIO/PWM 资源在服务端没有配置，或配置处于停用状态。',
        action: '到节点详情的外设配置里创建并启用对应资源。',
        selfHealable: false,
        raw: item.reason || '',
      }
  }

  return {
    key: 'unknown',
    summary: item.reason || '该操作当前不可用。',
    action: '无需操作：这条原因还没有对应的处置指引，请把上面的原文反馈给开发。',
    selfHealable: false,
    raw: item.reason || '',
      }
}

/** 页面只对「能自愈」的不可用给一键入口。 */
export function selfHealableUnavailable(items: EffectiveAction[]): EffectiveAction[] {
  return items.filter((item) => classifyUnavailable(item).selfHealable)
}