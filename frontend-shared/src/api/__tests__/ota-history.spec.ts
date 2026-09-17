import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockClient = vi.hoisted(() => ({
  get: vi.fn(),
  delete: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
}))

vi.mock('../client', () => ({ default: mockClient }))

import { nodeApi, type OTARecord } from '../node'

// 2026-09-17 生产实测回归：节点详情「OTA 历史」表格出现一行
// 「— / 空状态徽标 / 0%」的脏行，真实记录全部不可见。
//
// 根因：后端 getNodeOTAHistory 返回 Success(gin.H{"data": tasks})，
// 真正数组在 response.data.data；而 API 层直接 return response.data（对象），
// el-table 把对象当成"一行"渲染。
describe('nodeApi.getOTAHistory', () => {
  beforeEach(() => vi.clearAllMocks())

  const record: OTARecord = {
    id: 3,
    node_id: 'F0F5BDFFFE02',
    ota_id: 'ota-1',
    firmware_id: 1,
    to_version: '2.5.21',
    status: 'success',
    progress: 100,
    created_at: '2026-09-17T08:11:07Z',
  }

  it('unwraps the nested data.data envelope the backend actually sends', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { data: [record] }, message: 'ok' })
    const out = await nodeApi.getOTAHistory('F0F5BDFFFE02')
    expect(Array.isArray(out)).toBe(true)
    expect(out).toHaveLength(1)
    expect(out[0].status).toBe('success')
    expect(out[0].progress).toBe(100)
  })

  it('tolerates an already-flat array (guards against a future backend flattening)', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: [record], message: 'ok' })
    const out = await nodeApi.getOTAHistory('F0F5BDFFFE02')
    expect(Array.isArray(out)).toBe(true)
    expect(out).toHaveLength(1)
  })

  it('never returns a non-array (that is what produced the phantom table row)', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { data: null }, message: 'ok' })
    const out = await nodeApi.getOTAHistory('F0F5BDFFFE02')
    expect(Array.isArray(out)).toBe(true)
    expect(out).toHaveLength(0)
  })
})
