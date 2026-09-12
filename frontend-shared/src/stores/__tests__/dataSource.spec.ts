import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useDataSourceStore } from '../dataSource'
import { dataSourceApi } from '@/api/dataSource'
import type { DataSource } from '@/api/dataSource'

// store 依赖的 API 模块整体 mock (照抄 stores/__tests__/alert.spec.ts 模式)
vi.mock('@/api/dataSource', () => ({
  dataSourceApi: {
    list: vi.fn(),
    get: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
    activate: vi.fn(),
    deactivate: vi.fn(),
    reset: vi.fn(),
    getHealth: vi.fn(),
    getFailoverLogs: vi.fn(),
  },
}))

const mockedApi = vi.mocked(dataSourceApi)

function makeSource(overrides: Partial<DataSource> = {}): DataSource {
  return {
    id: 1,
    device_id: 10,
    category: 'temperature',
    edge_device_id: 100,
    source_type: 'edge_device',
    name: 'source-1',
    description: '',
    priority: 0,
    is_primary: false,
    max_fail_count: 3,
    fail_count: 0,
    status: 'standby',
    last_success: null,
    last_failure: null,
    config: '',
    created_at: '2026-09-12T00:00:00Z',
    updated_at: '2026-09-12T00:00:00Z',
    ...overrides,
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
})

describe('useDataSourceStore', () => {
  it('fetchList 成功写入 items/total 并复位 loading', async () => {
    mockedApi.list.mockResolvedValue({ items: [makeSource()], total: 7 })
    const store = useDataSourceStore()
    await store.fetchList({ device_id: 10 })
    expect(mockedApi.list).toHaveBeenCalledWith({ device_id: 10 })
    expect(store.items).toHaveLength(1)
    expect(store.items[0].id).toBe(1)
    expect(store.total).toBe(7)
    expect(store.loading).toBe(false)
  })

  it('fetchList 失败写入 error 且 rethrow, 复位 loading', async () => {
    mockedApi.list.mockRejectedValue(new Error('列表加载失败'))
    const store = useDataSourceStore()
    await expect(store.fetchList()).rejects.toThrow('列表加载失败')
    expect(store.error).toBe('列表加载失败')
    expect(store.loading).toBe(false)
  })

  it('createSource 成功后本地前插', async () => {
    const created = makeSource({ id: 2, name: 'new' })
    mockedApi.create.mockResolvedValue(created)
    const store = useDataSourceStore()
    store.items = [makeSource({ id: 1 })]
    await store.createSource({ device_id: 10, category: 'temperature', edge_device_id: 100 })
    expect(store.items.map(s => s.id)).toEqual([2, 1])
    expect(store.items[0].name).toBe('new')
  })

  it('updateSource 成功后本地替换对应项', async () => {
    const updated = makeSource({ id: 1, name: 'renamed', priority: 9 })
    mockedApi.update.mockResolvedValue(updated)
    const store = useDataSourceStore()
    store.items = [makeSource({ id: 1, name: 'old' }), makeSource({ id: 2 })]
    await store.updateSource(1, { name: 'renamed', priority: 9 })
    expect(mockedApi.update).toHaveBeenCalledWith(1, { name: 'renamed', priority: 9 })
    expect(store.items[0].name).toBe('renamed')
    expect(store.items[0].priority).toBe(9)
    expect(store.items).toHaveLength(2)
  })

  it('removeSource 成功后本地过滤', async () => {
    mockedApi.remove.mockResolvedValue(undefined)
    const store = useDataSourceStore()
    store.items = [makeSource({ id: 1 }), makeSource({ id: 2 })]
    await store.removeSource(1)
    expect(store.items.map(s => s.id)).toEqual([2])
  })

  it('createSource 失败写入 error 且 rethrow', async () => {
    mockedApi.create.mockRejectedValue(new Error('创建失败'))
    const store = useDataSourceStore()
    await expect(store.createSource({ device_id: 10, category: 'temperature', edge_device_id: 100 }))
      .rejects.toThrow('创建失败')
    expect(store.error).toBe('创建失败')
    expect(store.loading).toBe(false)
  })

  it('activateSource 用返回值替换本地项并更新 status', async () => {
    mockedApi.activate.mockResolvedValue(makeSource({ id: 2, status: 'active' }))
    const store = useDataSourceStore()
    store.items = [makeSource({ id: 2, status: 'standby' })]
    await store.activateSource(2)
    expect(store.items[0].status).toBe('active')
  })

  it('deactivateSource 用返回值替换本地项并更新 status', async () => {
    mockedApi.deactivate.mockResolvedValue(makeSource({ id: 1, status: 'disabled' }))
    const store = useDataSourceStore()
    store.items = [makeSource({ id: 1, status: 'active' })]
    await store.deactivateSource(1)
    expect(store.items[0].status).toBe('disabled')
  })

  it('resetSource 用返回值替换本地项并更新 status/fail_count', async () => {
    mockedApi.reset.mockResolvedValue(makeSource({ id: 1, status: 'standby', fail_count: 0 }))
    const store = useDataSourceStore()
    store.items = [makeSource({ id: 1, status: 'error', fail_count: 3 })]
    await store.resetSource(1)
    expect(store.items[0].status).toBe('standby')
    expect(store.items[0].fail_count).toBe(0)
  })

  it('fetchHealth 写入 health 并复位 healthLoading', async () => {
    const records = [{
      id: 1, source_id: 1, device_id: 10, category: 'temperature',
      status: 'failure' as const, message: 'offline', response_time: 12, created_at: '2026-09-12T01:00:00Z',
    }]
    mockedApi.getHealth.mockResolvedValue(records)
    const store = useDataSourceStore()
    await store.fetchHealth(1, 20)
    expect(mockedApi.getHealth).toHaveBeenCalledWith(1, 20)
    expect(store.health).toEqual(records)
    expect(store.healthLoading).toBe(false)
  })

  it('fetchHealth 失败写入 error 且 rethrow', async () => {
    mockedApi.getHealth.mockRejectedValue(new Error('健康记录加载失败'))
    const store = useDataSourceStore()
    await expect(store.fetchHealth(1)).rejects.toThrow('健康记录加载失败')
    expect(store.error).toBe('健康记录加载失败')
    expect(store.healthLoading).toBe(false)
  })

  it('fetchFailoverLogs 写入 failoverLogs 并复位 failoverLoading', async () => {
    const logs = [{
      id: 1, device_id: 10, category: 'temperature', from_source_id: 1, to_source_id: 2,
      reason: 'auto' as const, trigger: 'device_offline' as const, created_at: '2026-09-12T02:00:00Z',
    }]
    mockedApi.getFailoverLogs.mockResolvedValue(logs)
    const store = useDataSourceStore()
    await store.fetchFailoverLogs(10, { limit: 50, category: 'temperature' })
    expect(mockedApi.getFailoverLogs).toHaveBeenCalledWith(10, { limit: 50, category: 'temperature' })
    expect(store.failoverLogs).toEqual(logs)
    expect(store.failoverLoading).toBe(false)
  })

  it('computed 按状态计数正确 (给定 4 条不同状态)', () => {
    const store = useDataSourceStore()
    store.items = [
      makeSource({ id: 1, status: 'active' }),
      makeSource({ id: 2, status: 'standby' }),
      makeSource({ id: 3, status: 'error' }),
      makeSource({ id: 4, status: 'disabled' }),
    ]
    expect(store.activeCount).toBe(1)
    expect(store.standbyCount).toBe(1)
    expect(store.errorCount).toBe(1)
    expect(store.disabledCount).toBe(1)
  })

  it('clearError 清空 error', async () => {
    mockedApi.list.mockRejectedValue(new Error('boom'))
    const store = useDataSourceStore()
    await expect(store.fetchList()).rejects.toThrow('boom')
    expect(store.error).toBe('boom')
    store.clearError()
    expect(store.error).toBeNull()
  })
})
