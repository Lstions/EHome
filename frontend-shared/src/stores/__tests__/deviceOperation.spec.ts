import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { clearSessionCaches } from '@/utils/sessionCache'
import { useDeviceOperationStore } from '../deviceOperation'

const { actions, list, create, confirm, getDetail, queryResources } = vi.hoisted(() => ({
  actions: vi.fn<(...args: any[]) => Promise<any>>(() => Promise.resolve([])),
  list: vi.fn<(...args: any[]) => Promise<any>>(() => Promise.resolve([])),
  create: vi.fn<(...args: any[]) => Promise<any>>(),
  confirm: vi.fn<(...args: any[]) => Promise<any>>(),
  getDetail: vi.fn<(...args: any[]) => Promise<any>>(),
  queryResources: vi.fn<(...args: any[]) => Promise<any>>(),
}))

vi.mock('@/api/deviceOperation', () => ({
  deviceOperationApi: { actions, list, create, confirm },
}))
vi.mock('@/api/edgeDevice', () => ({ edgeDeviceApi: { getDetail } }))
vi.mock('@/api/node', () => ({ nodeApi: { queryResources } }))

const operation = (status: any, updatedAt = '2026-07-19T00:00:00Z') => ({
  command_id: 'command-1', edge_device_id: 7, node_id: 'node-1', action_id: 'read_rainfall',
  action_version: 1, status, created_at: '2026-07-19T00:00:00Z', updated_at: updatedAt,
})

describe('useDeviceOperationStore ordering', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    actions.mockResolvedValue([])
    list.mockResolvedValue([])
    getDetail.mockResolvedValue({ node_id: 'node-1' })
    queryResources.mockResolvedValue({ request_id: 'resource-1' })
  })

  it('does not regress an early WebSocket acceptance or final with a stale HTTP response', () => {
    const store = useDeviceOperationStore()
    store.apply(operation('DEVICE_ACCEPTED'))
    store.apply(operation('QUEUED', '2026-07-19T00:00:01Z'))
    expect(store.histories.get(7)?.[0].status).toBe('DEVICE_ACCEPTED')

    store.apply(operation('SUCCEEDED', '2026-07-19T00:00:02Z'))
    store.apply(operation('DISPATCHED', '2026-07-19T00:00:03Z'))
    expect(store.histories.get(7)?.[0].status).toBe('SUCCEEDED')
  })

  it('merges refresh history instead of replacing an early event', async () => {
    const store = useDeviceOperationStore()
    store.apply(operation('DEVICE_ACCEPTED'))
    list.mockResolvedValueOnce([operation('QUEUED')])
    await store.refresh(7)
    expect(store.histories.get(7)?.[0].status).toBe('DEVICE_ACCEPTED')
  })

  it('does not restore a refresh that completed after session clear', async () => {
    let resolveActions!: (value: any) => void
    let resolveList!: (value: any) => void
    actions.mockImplementationOnce(() => new Promise(resolve => { resolveActions = resolve }))
    list.mockImplementationOnce(() => new Promise(resolve => { resolveList = resolve }))
    const store = useDeviceOperationStore()
    const pending = store.refresh(7)
    clearSessionCaches()
    resolveActions([])
    resolveList([operation('SUCCEEDED')])
    await pending
    expect(store.catalogs.size).toBe(0)
    expect(store.histories.size).toBe(0)
  })

  it('forwards a confirmation token and reason when creating a confirmed action', async () => {
    create.mockResolvedValueOnce(operation('QUEUED'))
    const store = useDeviceOperationStore()
    await store.create(7, 'set_mode', { mode: 'SBU' }, 'token-1', 'controlled change')
    expect(create).toHaveBeenCalledWith(7, 'set_mode', { mode: 'SBU' }, 'token-1', 'controlled change')
  })

  it('refreshes a stale resource snapshot once before reloading the action catalog', async () => {
    vi.useFakeTimers()
    actions
      .mockResolvedValueOnce([{ definition: { id: 'read' }, available: false, reason_code: 'capability_stale' }])
      .mockResolvedValueOnce([{ definition: { id: 'read' }, available: true }])
    const store = useDeviceOperationStore()
    const pending = store.refresh(7)
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(2000)
    await pending
    expect(getDetail).toHaveBeenCalledWith(7)
    expect(queryResources).toHaveBeenCalledWith('node-1')
    expect(actions).toHaveBeenCalledTimes(2)
    expect(store.catalogs.get(7)?.[0].available).toBe(true)
    vi.useRealTimers()
  })
})
