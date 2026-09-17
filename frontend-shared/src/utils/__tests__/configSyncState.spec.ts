import { describe, it, expect } from 'vitest'
import { configSyncStateLabel, configSyncStateTagType, isConfigSyncFailed } from '../configSyncState'

// 回归测试：生产节点 F0F5BDFFFE02 曾出现「后端每 30s 拒绝配置下发，UI 却显示已同步」。
// 后端在拒绝时写 config_sync_state='failed'，但两个视图的映射表都漏了 'failed'，
// 于是落到兜底分支显示「未知」——与"正常"在视觉上难以区分。
describe('configSyncStateLabel', () => {
  it('renders every backend-written state explicitly, including failed', () => {
    expect(configSyncStateLabel('in_sync')).toBe('已同步')
    expect(configSyncStateLabel('syncing')).toBe('同步中')
    // 关键回归点：failed 必须有自己的文案，不能落到「未知」
    expect(configSyncStateLabel('failed')).toBe('同步失败')
    expect(configSyncStateLabel('lag')).toBe('落后')
    expect(configSyncStateLabel('error')).toBe('错误')
    expect(configSyncStateLabel('unknown')).toBe('未知')
  })

  it('never renders failed as the unknown fallback', () => {
    expect(configSyncStateLabel('failed')).not.toBe(configSyncStateLabel('unknown'))
  })

  it('falls back to 未知 for empty/absent/unrecognised values', () => {
    for (const v of [undefined, null, '', 'bogus', 42, {}]) {
      expect(configSyncStateLabel(v)).toBe('未知')
    }
  })
})

describe('configSyncStateTagType', () => {
  it('marks failed as danger rather than the neutral info fallback', () => {
    expect(configSyncStateTagType('failed')).toBe('danger')
    expect(configSyncStateTagType('error')).toBe('danger')
    expect(configSyncStateTagType('in_sync')).toBe('success')
    expect(configSyncStateTagType('syncing')).toBe('warning')
    expect(configSyncStateTagType('bogus')).toBe('info')
  })
})

describe('isConfigSyncFailed', () => {
  it('distinguishes a real failure from an unknown state', () => {
    expect(isConfigSyncFailed('failed')).toBe(true)
    expect(isConfigSyncFailed('error')).toBe(true)
    expect(isConfigSyncFailed('unknown')).toBe(false)
    expect(isConfigSyncFailed('in_sync')).toBe(false)
  })
})
