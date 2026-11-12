import type { Component } from 'vue'
import { Timer, Operation, SetUp, Tools, Document } from '@element-plus/icons-vue'
import type { ActionDefinition, DeviceOperation, EffectiveAction, OperationStatus } from '@/api/deviceOperation'
import type { TagType } from '../bmsFormat'

/**
 * BMS「受控操作」的展示层模型（纯常量 / 纯函数，无状态）。
 *
 * 为什么单独一个文件：目录分组、风险判定、执行状态词表、操作历史的结果标签
 * 都只跟后端目录与回执有关，与页面响应式状态无关。拆出后
 *   · useBmsOperations（状态机）与 BmsOpsHistorySection（子组件）共用同一份定义；
 *   · 断言「状态文案不暴露 QUEUED/VERIFYING 等内部枚举」的门禁仍只指向一处。
 * 内容与拆分前逐字一致。
 */

/**
 * 重验证目标：MOS 直控与受控操作共用同一个「验证当前身份」弹窗，
 * 故目标形状（而不是两份状态）由页面持有、两侧共同读写。
 */
export type ReauthTarget = { type: 'mos'; which: 'charge' | 'discharge' } | { type: 'op'; id: string } | null

export interface OpDef {
  id: string
  name: string
  note: string
  risky: boolean
  requiresConfirmation: boolean
  available: boolean
  reason: string
  needsParams: boolean
  movedTo: boolean
  /** 后端动作定义，参数化表单据此生成字段（input_schema） */
  definition: ActionDefinition
}
export interface OpGroup { id: string; name: string; hint: string; icon: Component; ops: OpDef[] }
export type OpPhase = 'idle' | 'sent' | 'running' | 'verifying' | 'success' | 'failed' | 'unknown' | 'cancelled'
export interface OpState { phase: OpPhase; text: string; reason: string }

/** 分组只是展示层归属（目录本身不含分组），成员按 action id 归属；未列出的归入「其他操作」。 */
export const OP_GROUP_DEFS: Array<{ id: string; name: string; hint: string; icon: Component; ids: string[] }> = [
  {
    id: 'read', name: '读取信息', hint: '一次性读取，不改设备配置', icon: Document,
    ids: ['read_basic_info', 'read_cell_voltage', 'read_hardware_version', 'read_comprehensive', 'read_protection_count', 'read_custom_attributes', 'read_test_mos_status'],
  },
  {
    id: 'diag', name: '诊断与测试', hint: '不写入参数，用于现场排障', icon: Tools,
    ids: ['find_car', 'test_charge_mos', 'test_discharge_mos'],
  },
  {
    id: 'runtime', name: '运行控制', hint: '直接影响设备当前运行状态', icon: Operation,
    ids: ['set_mos_policy', 'force_balance', 'clear_alarm', 'auto_test_edv', 'bms_restart'],
  },
  {
    id: 'param', name: '参数与标定', hint: '读取 / 写入厂内参数，需进厂模式', icon: SetUp,
    ids: ['read_protection_parameters', 'read_system_parameters', 'write_protection_parameters', 'write_system_parameters', 'write_custom_attributes', 'write_internal_resistance', 'write_sn'],
  },
  {
    id: 'timer', name: '时间与上报设置', hint: '改变设备行为参数', icon: Timer,
    ids: ['set_static_correction_time', 'set_report_interval', 'set_charge_time_window', 'set_discharge_time_limit'],
  },
]

export function toOp(action: EffectiveAction): OpDef {
  const def = action.definition
  const properties = Object.keys(def.input_schema?.properties ?? {})
  return {
    id: def.id,
    name: def.name,
    note: def.description || '',
    risky: def.risk_level === 'elevated',
    requiresConfirmation: def.requires_confirmation === true,
    available: action.available,
    reason: action.reason || '',
    needsParams: properties.length > 0,
    movedTo: def.id === 'set_mos_policy',
    definition: def,
  }
}

/** 执行状态 → 中文（不暴露 QUEUED/VERIFYING 等内部枚举）。 */
export const OPERATION_UI: Record<OperationStatus, { phase: OpPhase; text: string }> = {
  QUEUED: { phase: 'sent', text: '已发送' },
  DISPATCHED: { phase: 'running', text: '已下发' },
  DEVICE_ACCEPTED: { phase: 'running', text: '设备已收到' },
  VERIFYING: { phase: 'verifying', text: '校验中' },
  SUCCEEDED: { phase: 'success', text: '成功' },
  FAILED: { phase: 'failed', text: '失败' },
  UNKNOWN: { phase: 'unknown', text: '结果未知' },
  CANCELLED: { phase: 'cancelled', text: '已取消' },
}

export const TERMINAL: OperationStatus[] = ['SUCCEEDED', 'FAILED', 'UNKNOWN', 'CANCELLED']

export function stateDot(phase: OpPhase): string {
  switch (phase) {
    case 'success': return 'dot-green'
    case 'failed': return 'dot-red'
    case 'unknown': return 'dot-orange'
    case 'cancelled': return 'dot-orange'
    default: return 'dot-blue'
  }
}

export type HistoryResult = 'success' | 'failed' | 'unknown' | 'cancelled'
export interface HistoryRow { id: string; time: string; type: string; content: string; result: HistoryResult }

/** 后端 reason 里会出现 final_succeeded / final_failed 这类内部令牌，转成中文或交给回读摘要。 */
export function readableFinalReason(reason?: string): string {
  if (!reason) return ''
  if (reason === 'final_failed') return '设备未接受或回读校验不一致'
  if (reason === 'final_succeeded') return ''
  return reason
}

export function verifiedSummary(op: DeviceOperation): string {
  return (op.verified_result ?? [])
    .filter((v) => !v.name.endsWith('_ack'))
    .slice(0, 3)
    .map((v) => v.name + '=' + (v.string_value || v.value) + (v.unit || ''))
    .join(', ')
}

export function resultTag(r: HistoryResult): TagType {
  return r === 'success' ? 'success' : r === 'failed' ? 'danger' : r === 'cancelled' ? 'info' : 'warning'
}
export function resultText(r: HistoryResult): string {
  return r === 'success' ? '成功' : r === 'failed' ? '失败' : r === 'cancelled' ? '已取消' : '结果未知'
}
