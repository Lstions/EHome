import { describe, expect, it } from 'vitest'
import source from '../DeviceControlPanel.vue?raw'

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
