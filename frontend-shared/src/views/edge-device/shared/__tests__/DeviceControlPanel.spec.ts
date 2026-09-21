import { describe, expect, it } from 'vitest'
import source from '../DeviceControlPanel.vue?raw'
import availabilitySource from '@/utils/actionAvailability.ts?raw'

describe('DeviceControlPanel high-risk recovery', () => {
  it('reauthenticates and retries the original confirmed intent', () => {
    expect(source).toContain("isApiErrorCode(error, 'recent_auth_required')")
    expect(source).toContain('authApi.reauthenticate(password)')
    expect(source).toContain('await queueConfirmed(action, reason)')
    expect(source).toContain('selectedKey.value')
    expect(source).toContain('该高风险操作要求最近 10 分钟内验证密码')
  })

  it('distinguishes authoritative HTTP failures from ambiguous lost responses', () => {
    expect(source).toContain('error instanceof ApiError && error.response')
    expect(source).toContain('操作结果未确认，已刷新状态；请勿重复提交')
  })
})

describe('DeviceControlPanel catalog-driven action cards (design: 受控操作)', () => {
  it('renders available actions as clickable cards with icon, name, description', () => {
    expect(source).toContain('当前设备可执行的受控操作')
    expect(source).toContain('class="op-item"')
    expect(source).toContain('actionIcon(action.definition)')
    expect(source).toContain('action.definition.description')
    // 图标按语义映射：reset→RefreshLeft / read→View / set→EditPen
    expect(source).toContain("action.semantics === 'reset' ? RefreshLeft")
    expect(source).toContain("action.semantics === 'read' ? View")
    expect(source).toContain("action.semantics === 'set' ? EditPen")
  })

  it('marks high/critical risk on the card and keeps cards keyboard-accessible', () => {
    expect(source).toContain("action.definition.risk === 'critical'")
    expect(source).toContain('严重风险')
    expect(source).toContain('高风险')
    expect(source).toContain('tabindex="0"')
    expect(source).toContain('@keydown.enter.space.prevent')
  })

  it('keeps gated actions (e.g. protocol_unverified) in the unavailable collapse with reasons', () => {
    expect(source).toContain('暂不可用操作')
    expect(source).toContain('availabilityReason(action)')
  })

  it('keeps the empty state when no action is executable (fail-closed 语义)', () => {
    expect(source).toContain('当前没有可执行的受控操作')
  })
})
describe('DeviceControlPanel availability guidance (P0-b)', () => {
  it('按分类聚合不可用项，并对可自愈类提供「立即恢复」按钮', () => {
    expect(source).toContain('classifyUnavailable')
    expect(source).toContain('unavailableGuidance')
    expect(source).toContain('selfHealableCount')
    expect(source).toContain('立即恢复')
  })

  it('不得再按单一 reason_code 分支（那正是「只修自己设的那个面」）', () => {
    // 修复前：只有 reason_code === 'capability_stale' 才会出指引。
    expect(source).not.toContain("item.reason_code === 'capability_stale'")
  })

  it('不得在文案里硬编码阈值（阈值改过一次就悄悄说谎）', () => {
    // 分母：文案不在 .vue 里，而在 utils/actionAvailability.ts。
    // M5 变异自证暴露过这个洞：只扫 .vue 时，把阈值硬编码进模块文案仍是绿的。
    const copySources = [source, availabilitySource]
    for (const text of copySources) {
      expect(text, '文案里出现硬编码的「有效期（N 分钟）」').not.toMatch(/有效期（\s*\d+\s*分钟）/)
      expect(text, '文案里出现硬编码的「每 N 分钟」').not.toMatch(/每\s*\d+\s*分钟/)
    }
    // 反向自证：扫描面确实包含承载文案的那个模块。
    expect(availabilitySource).toContain('serverCapabilityWindowCopy')
  })

  it('空态只在「没有可用操作且没有任何指引」时出现（否则会和指引同时显示）', () => {
    expect(source).toContain('available.length === 0 && unavailableGuidance.length === 0')
  })
})