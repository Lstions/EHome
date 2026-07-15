import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useChannelStore } from '../channel'
import { useDmaStore } from '../dma'
import { useFirmwareStore } from '../firmware'
import { useParserStore } from '../parser'

const { getChannels, createChannel, getDma, updateDma, getFirmwares, deleteFirmware, getParsers } = vi.hoisted(() => ({
  getChannels: vi.fn(), createChannel: vi.fn(), getDma: vi.fn(), updateDma: vi.fn(), getFirmwares: vi.fn(), deleteFirmware: vi.fn(), getParsers: vi.fn(),
}))
vi.mock('@/api/channel', () => ({ channelApi: { getList: getChannels, create: createChannel } }))
vi.mock('@/api/node', () => ({ nodeApi: { getDmaChannels: getDma, updateDmaConfig: updateDma } }))
vi.mock('@/api/firmware', () => ({ firmwareApi: { getList: getFirmwares, delete: deleteFirmware } }))
vi.mock('@/api/parser', () => ({ parserApi: { getList: getParsers } }))

describe('session-scoped stores', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it.each([
    ['channel', () => useChannelStore(), getChannels, (s: any) => s.fetchChannels(), (s: any) => s.channels, [{ id: 1 }]],
    ['dma', () => useDmaStore(), getDma, (s: any) => s.fetch(1), (s: any) => s.channels, [{ dma_id: 1 }]],
    ['firmware', () => useFirmwareStore(), getFirmwares, (s: any) => s.fetchList(), (s: any) => s.list, { list: [{ id: 1 }], total: 1 }],
    ['parser', () => useParserStore(), getParsers, (s: any) => s.fetchParsers(), (s: any) => s.parsers, [{ id: 'p1' }]],
  ])('does not restore %s data after clearCache', async (_name, create, mock, fetch, read, response) => {
    let resolve!: (value: any) => void
    mock.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const store: any = create()
    const pending = fetch(store)
    store.clearCache()
    resolve(response)
    await pending
    expect(read(store)).toEqual([])
    if (_name === 'firmware') expect(store.loading).toBe(false)
  })

  it('does not append a channel created by the previous session', async () => {
    let resolve!: (value: any) => void
    createChannel.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const store = useChannelStore()
    const pending = store.createChannel({ name: 'old' })
    store.clearCache()
    resolve({ id: 1, name: 'old' })
    await expect(pending).rejects.toThrow('会话已变更')
    expect(store.channels).toEqual([])
  })

  it('does not restore firmware after a previous-session delete fails', async () => {
    let reject!: (error: Error) => void
    getFirmwares.mockResolvedValueOnce({ list: [{ id: 1 }], total: 1 })
    deleteFirmware.mockImplementationOnce(() => new Promise((_resolve, r) => { reject = r }))
    const store = useFirmwareStore()
    await store.fetchList()
    const pending = store.deleteFirmware(1)
    store.clearCache()
    reject(new Error('failed'))
    await expect(pending).rejects.toThrow('failed')
    expect(store.list).toEqual([])
  })

  it('rolls back only the failed firmware during concurrent deletes', async () => {
    let rejectOne!: (error: Error) => void
    let resolveTwo!: () => void
    getFirmwares.mockResolvedValueOnce({ list: [{ id: 1 }, { id: 2 }], total: 2 })
    deleteFirmware
      .mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectOne = reject }))
      .mockImplementationOnce(() => new Promise<void>(resolve => { resolveTwo = resolve }))
    const store = useFirmwareStore()
    await store.fetchList()
    const first = store.deleteFirmware(1)
    const second = store.deleteFirmware(2)
    resolveTwo()
    rejectOne(new Error('failed'))
    await expect(first).rejects.toThrow('failed')
    await second
    expect(store.list.map(item => item.id)).toEqual([1])
    expect(store.total).toBe(1)
  })

  it('joins duplicate firmware delete calls so they share the real result', async () => {
    let reject!: (error: Error) => void
    getFirmwares.mockResolvedValueOnce({ list: [{ id: 1 }], total: 1 })
    deleteFirmware.mockImplementationOnce(() => new Promise((_resolve, r) => { reject = r }))
    const store = useFirmwareStore()
    await store.fetchList()
    const first = store.deleteFirmware(1)
    const duplicate = store.deleteFirmware(1)
    reject(new Error('failed'))
    await expect(first).rejects.toThrow('failed')
    await expect(duplicate).rejects.toThrow('failed')
    expect(deleteFirmware).toHaveBeenCalledTimes(1)
  })

  it('rejects a previous-session firmware delete even when the API succeeds', async () => {
    let resolve!: () => void
    getFirmwares.mockResolvedValueOnce({ list: [{ id: 1 }], total: 1 })
    deleteFirmware.mockImplementationOnce(() => new Promise<void>(r => { resolve = r }))
    const store = useFirmwareStore()
    await store.fetchList()
    const pending = store.deleteFirmware(1)
    store.clearCache()
    resolve()
    await expect(pending).rejects.toThrow('会话已变更')
  })

  it('rejects a previous-session DMA write instead of reporting success', async () => {
    let resolve!: () => void
    updateDma.mockImplementationOnce(() => new Promise<void>(r => { resolve = r }))
    const store = useDmaStore()
    const pending = store.toggle(1, { dma_id: 1, state: 2, name: 'DMA1', compatible_bus: 1 } as any, true, 'uart/UART1')
    store.clearCache()
    resolve()
    await expect(pending).rejects.toThrow('会话已变更')
  })

  it('does not let a delayed DMA write for A refresh over collector B', async () => {
    let resolveUpdate!: () => void
    getDma.mockResolvedValueOnce([{ dma_id: 1, name: 'A' }])
    getDma.mockResolvedValueOnce([{ dma_id: 2, name: 'B' }])
    updateDma.mockImplementationOnce(() => new Promise<void>(r => { resolveUpdate = r }))
    const store = useDmaStore()
    await store.fetch('A')
    const pending = store.toggle('A', { dma_id: 1, state: 2, name: 'A', compatible_bus: 1 } as any, true, 'uart/UART1')
    await store.fetch('B')
    resolveUpdate()
    await expect(pending).rejects.toThrow('节点已变更')
    expect(store.channels).toEqual([{ dma_id: 2, name: 'B' }])
  })
})