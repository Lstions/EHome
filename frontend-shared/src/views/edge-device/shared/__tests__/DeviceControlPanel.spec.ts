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
