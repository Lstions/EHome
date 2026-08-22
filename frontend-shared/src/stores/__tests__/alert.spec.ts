import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useAlertStore } from '../alert'
import { alertApi } from '@/api/alert'

// store CRUD mock (照抄现有 __tests__ 模式: vi.mock api 模块)
vi.mock('@/api/alert', () => ({
  alertApi: {
    listRules: vi.fn(),
    createRule: vi.fn(),
    updateRule: vi.fn(),
    deleteRule: vi.fn(),
    setRuleEnabled: vi.fn(),
    listEvents: vi.fn(),
    markEventsRead: vi.fn(),
  },
}))

const mockedApi = vi.mocked(alertApi)

const ruleFixture = {
  id: 1,
  target_type: 'edge_device' as const,
  target_id: 10,
  sensor_name: 'cell_voltage_1',
  comparator: 'gt' as const,
  threshold: 4.2,
  duration_sec: 0,
  silence_sec: 300,
  level: 'warning' as const,
  enabled: true,
  name: '电池过压',
  created_at: '2026-08-21T00:00:00Z',
  updated_at: '2026-08-21T00:00:00Z',
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
})

describe('alert store', () => {
  it('fetchRules 直接赋值 unwrap 结果', async () => {
    mockedApi.listRules.mockResolvedValue([ruleFixture])
    const store = useAlertStore()
    await store.fetchRules()
    expect(store.rules).toHaveLength(1)
    expect(store.rules[0].name).toBe('电池过压')
    expect(store.rulesLoading).toBe(false)
  })

  it('createRule 本地头部追加', async () => {
    const created = { ...ruleFixture, id: 2, name: '新规则' }
    mockedApi.createRule.mockResolvedValue(created)
    const store = useAlertStore()
    store.rules = [ruleFixture]
    await store.createRule({ ...ruleFixture, name: '新规则' })
    expect(store.rules[0].id).toBe(2)
    expect(store.rules).toHaveLength(2)
  })

  it('updateRule 本地替换对应项', async () => {
    const updated = { ...ruleFixture, threshold: 4.25 }
    mockedApi.updateRule.mockResolvedValue(updated)
    const store = useAlertStore()
    store.rules = [ruleFixture]
    await store.updateRule(1, { threshold: 4.25 })
    expect(store.rules[0].threshold).toBe(4.25)
  })

  it('deleteRule 本地移除', async () => {
    mockedApi.deleteRule.mockResolvedValue(undefined)
    const store = useAlertStore()
    store.rules = [ruleFixture]
    await store.deleteRule(1)
    expect(store.rules).toHaveLength(0)
  })

  it('setRuleEnabled 更新启用态', async () => {
    const toggled = { ...ruleFixture, enabled: false }
    mockedApi.setRuleEnabled.mockResolvedValue(toggled)
    const store = useAlertStore()
    store.rules = [ruleFixture]
    await store.setRuleEnabled(1, false)
    expect(store.rules[0].enabled).toBe(false)
  })

  it('fetchEvents 赋值事件列表并计算 firingCount', async () => {
    mockedApi.listEvents.mockResolvedValue([
      { id: 1, rule_id: 1, state: 'firing', value: 4.3, fired_at: '2026-08-21T01:00:00Z', resolved_at: null, notified_at: '2026-08-21T01:00:00Z', created_at: '2026-08-21T01:00:00Z' },
      { id: 2, rule_id: 1, state: 'resolved', value: 4.1, fired_at: '2026-08-21T01:00:00Z', resolved_at: '2026-08-21T02:00:00Z', notified_at: '2026-08-21T01:00:00Z', created_at: '2026-08-21T01:00:00Z' },
    ])
    const store = useAlertStore()
    await store.fetchEvents()
    expect(store.events).toHaveLength(2)
    expect(store.firingCount).toBe(1)
  })

  it('markEventsRead 透传 api', async () => {
    mockedApi.markEventsRead.mockResolvedValue(undefined)
    const store = useAlertStore()
    await store.markEventsRead([1, 2])
    expect(mockedApi.markEventsRead).toHaveBeenCalledWith([1, 2])
  })
})
