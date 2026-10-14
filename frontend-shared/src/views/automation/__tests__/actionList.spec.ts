import { describe, expect, it } from 'vitest'
import {
  NORMAL_BADGE, emptyAction, moveAction, paramsSummary, requiresConfirmation, riskBadge, riskPresentation, toSequencedActions,
  type ActionDraft,
} from '../actionList'

/**
 * F-2 门禁：多动作列表可增删排序，且落库的 seq 满足后端 V1（1..N 连续无重复）。
 *
 * 反向验证（方案 §8 F-2）：把列表改回单动作（moveAction 变成空操作 / toSequencedActions
 * 不再按下标重排）→ 本文件必红。
 */
function drafts(count: number): ActionDraft[] {
  return Array.from({ length: count }, (_, i) => ({
    ...emptyAction(i + 1),
    action_device_id: 10,
    action_id: `act_${i + 1}`,
  }))
}

describe('F-2 多动作列表', () => {
  it('上移把目标动作换到前一位，且返回新数组（原地改不会触发重渲染）', () => {
    const list = drafts(3)
    const moved = moveAction(list, 3, -1)
    expect(moved.map(a => a.action_id)).toEqual(['act_1', 'act_3', 'act_2'])
    expect(moved).not.toBe(list)
    // 原数组未被改动：组件依赖"新数组"触发重渲染（设计稿 S8）
    expect(list.map(a => a.action_id)).toEqual(['act_1', 'act_2', 'act_3'])
  })

  it('下移对称生效', () => {
    const moved = moveAction(drafts(3), 1, 1)
    expect(moved.map(a => a.action_id)).toEqual(['act_2', 'act_1', 'act_3'])
  })

  it('首行上移 / 末行下移是越界空操作，不抛错', () => {
    const list = drafts(3)
    expect(moveAction(list, 1, -1).map(a => a.action_id)).toEqual(['act_1', 'act_2', 'act_3'])
    expect(moveAction(list, 3, 1).map(a => a.action_id)).toEqual(['act_1', 'act_2', 'act_3'])
  })

  it('seq 按**数组下标**重排为 1..N（后端 V1 fail-closed 要求）', () => {
    // 构造"上移后序号错位"的场景：先给一组乱序 uid 与人为 seq 无关性
    const list = moveAction(drafts(3), 3, -1)
    const seqs = toSequencedActions(list).map(a => a.seq)
    expect(seqs).toEqual([1, 2, 3])
    // 顺序与执行顺序一致：第 2 个是刚上移的动作
    expect(toSequencedActions(list)[1].action_id).toBe('act_3')
  })

  it('device_action 只带设备/动作/参数，notification 只带级别', () => {
    const device = toSequencedActions([{ ...emptyAction(1), action_device_id: 7, action_id: 'gpio_set', action_params_json: '{"pin":1}' }])
    expect(device[0]).toEqual({ seq: 1, action_type: 'device_action', action_device_id: 7, action_id: 'gpio_set', action_params_json: '{"pin":1}' })
    const notice = toSequencedActions([{ ...emptyAction(2), action_type: 'notification', action_level: 'warning' }])
    expect(notice[0]).toEqual({ seq: 1, action_type: 'notification', action_level: 'warning' })
  })

  it('空参数不提交空串（后端 CanonicalizeParams 会拒空串）', () => {
    expect(toSequencedActions([{ ...emptyAction(1), action_device_id: 7, action_id: 'gpio_set' }])[0]).not.toHaveProperty('action_params_json')
  })
})

/**
 * F-3 门禁：风险徽标**非仅靠颜色**（图标 + 文字），且判定源是后端下发的 2 级契约。
 *
 * 反向验证（方案 §8 F-3）：把 label 改成空串 → 本文件必红；把判定改回 4 级 risk
 * 字面量 → riskLiteralGate.spec.ts (F-7) 必红。
 */
describe('F-3 风险徽标与原因', () => {
  it('elevated 渲染"⚠ 风险"（图标 + 文字，不只靠颜色）', () => {
    const badge = riskBadge({ risk_level: 'elevated', requires_confirmation: true })
    expect(badge).not.toBeNull()
    expect(badge!.icon).toBe('⚠')
    expect(badge!.label).toBe('风险')
    // 只靠颜色时色觉障碍用户读不到风险 —— 文字必须非空且语义完整
    expect(badge!.label.length).toBeGreaterThan(0)
  })

  it('normal 不渲染风险徽标（F-6 普通动作无风险色）', () => {
    expect(riskBadge({ risk_level: 'normal', requires_confirmation: false })).toBeNull()
  })

  it('未识别/缺失等级不渲染徽标（fail-closed，不猜风险等级）', () => {
    expect(riskBadge({})).toBeNull()
    expect(riskBadge(undefined)).toBeNull()
    expect(riskBadge({ risk_level: 'high' as never })).toBeNull()
  })

  it('三态呈现把"目录里明确说普通"与"目录里没有这个动作"分开', () => {
    expect(riskPresentation({ risk_level: 'elevated' })).toBe('risk')
    expect(riskPresentation({ risk_level: 'normal' })).toBe('normal')
    // 未知不得回落到 'normal'：那等于替后端断言"这个动作不危险"
    expect(riskPresentation({})).toBe('unknown')
    expect(riskPresentation(undefined)).toBe('unknown')
    expect(riskPresentation({ risk_level: 'high' as never })).toBe('unknown')
  })

  it('普通动作徽标同样带图标与文字（信息不靠颜色区分）', () => {
    expect(NORMAL_BADGE.icon).toBe('●')
    expect(NORMAL_BADGE.label).toBe('普通')
  })

  it('requiresConfirmation 只读后端下发值，缺失时 fail-closed 判为需确认', () => {
    expect(requiresConfirmation({ requires_confirmation: true })).toBe(true)
    expect(requiresConfirmation({ requires_confirmation: false })).toBe(false)
    // 后端未下发时宁可多一次确认，不可漏一次危险动作
    expect(requiresConfirmation({})).toBe(true)
    expect(requiresConfirmation(null)).toBe(true)
  })
})

describe('参数摘要', () => {
  it('对象 → k=v 串联；空/无参数 → 明确文案', () => {
    expect(paramsSummary('{"pin":12,"value":1}')).toBe('pin=12, value=1')
    expect(paramsSummary('')).toBe('无参数')
    expect(paramsSummary(undefined)).toBe('无参数')
    expect(paramsSummary('{}')).toBe('无参数')
  })

  it('非法 JSON 如实回显原文（不在摘要层报错，保存前 validate 负责）', () => {
    expect(paramsSummary('{oops')).toBe('{oops')
  })
})
