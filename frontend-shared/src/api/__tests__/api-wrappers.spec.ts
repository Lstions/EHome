import { describe, it, expect, vi, beforeEach } from 'vitest'

// Hoisted mock — vi.mock factory runs before imports, so we need hoisted refs
const mockClient = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  delete: vi.fn(),
  defaults: { baseURL: '' },
}))

vi.mock('../client', () => ({
  default: mockClient,
}))

// ─── auth ───
import { authApi } from '../auth'

describe('authApi', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('login posts to /api/v1/auth/login and unwraps .data', async () => {
    const loginData = { token: 'jwt-1', user: { id: 1, username: 'admin', email: 'a@b', role: 'admin' } }
    mockClient.post.mockResolvedValue({ data: loginData })
    const result = await authApi.login({ username: 'admin', password: '123' })
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/auth/login', { username: 'admin', password: '123' })
    expect(result).toEqual(loginData)
  })

  it('logout posts to /api/v1/auth/logout', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await authApi.logout()
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/auth/logout', {})
  })

  it('reauthenticate posts only the current password to the protected endpoint', async () => {
    mockClient.post.mockResolvedValue({ data: { authenticated_at: '2026-07-19T00:00:00Z' } })
    await authApi.reauthenticate('current-password')
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/account/reauthenticate', { password: 'current-password' })
  })

  it('getToken reads from localStorage first', () => {
    localStorage.setItem('token', 'local-token')
    expect(authApi.getToken()).toBe('local-token')
  })

  it('getToken falls back to sessionStorage', () => {
    sessionStorage.setItem('token', 'session-token')
    expect(authApi.getToken()).toBe('session-token')
  })

  it('getToken returns empty string when no token', () => {
    expect(authApi.getToken()).toBe('')
  })
})

// ─── deviceOperation ───
import { deviceOperationApi } from '../deviceOperation'

describe('deviceOperationApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('does not retry an authoritative HTTP rejection', async () => {
    const rejection = Object.assign(new Error('recent authentication is required'), { response: { status: 403 } })
    mockClient.post.mockRejectedValue(rejection)
    await expect(deviceOperationApi.create(7, 'set_mode', {}, '', '', 'same-key')).rejects.toBe(rejection)
    expect(mockClient.post).toHaveBeenCalledOnce()
  })

  it('retries one ambiguous lost response with the exact idempotency key', async () => {
    const execution = { command_id: 'cmd-1', edge_device_id: 7, status: 'QUEUED' }
    mockClient.post.mockRejectedValueOnce(new Error('timeout')).mockResolvedValueOnce({ data: { execution } })
    await expect(deviceOperationApi.create(7, 'read', {}, '', '', 'same-key')).resolves.toEqual(execution)
    expect(mockClient.post).toHaveBeenCalledTimes(2)
    expect(mockClient.post.mock.calls[0]).toEqual(mockClient.post.mock.calls[1])
  })
})

// ─── channel ───
import { channelApi, CHANNEL_LIST_MAX_PAGE_SIZE } from '../channel'

describe('channelApi', () => {
  beforeEach(() => vi.clearAllMocks())

  // 契约（2026-09-16 静默截断回归修复）：GET /channels 已改为服务端分页，
  // 不传 page_size 时服务端只给默认的 20 条。因此 getList 在调用方未显式指定 pageSize 时
  // 必须下发上界 CHANNEL_LIST_MAX_PAGE_SIZE（200），恢复改造前的「全量」语义。
  // 下面三条断言钉死这个契约；任何「缺省就不下发」的改法都会让它们变红。
  it('getList with nodeId passes params with node_id and explicit max page_size', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { items: [{ id: 1 }], total: 1 } })
    const res = await channelApi.getList(5)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/channels', {
      params: { node_id: 5, page_size: CHANNEL_LIST_MAX_PAGE_SIZE },
    })
    expect(res).toEqual({ items: [{ id: 1 }], total: 1 })
  })

  it('getList without explicit pageSize still sends page_size (regression guard)', async () => {
    // 防的是「把缺省 pageSize 优化成不下发」——那正是本轮静默截断回归的根因。
    // 能发现的错误：把 pageSize !== undefined 的判断原样恢复（不下发）、
    // 或把默认值从 200 改小（>200 条以外的截断边界被悄悄移动）。
    mockClient.get.mockResolvedValue({ code: 200, data: { items: [] } })
    const res = await channelApi.getList()
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/channels', {
      params: { page_size: CHANNEL_LIST_MAX_PAGE_SIZE },
    })
    // 逐键校验：params 里多塞/少塞键时不会被 toEqual 的宽松比较掩盖
    const callArgs = mockClient.get.mock.calls[0][1] as { params: Record<string, unknown> }
    expect(Object.keys(callArgs.params)).toEqual(['page_size'])
    expect(callArgs.params.page_size).toBe(CHANNEL_LIST_MAX_PAGE_SIZE)
    expect(res).toEqual({ items: [] })
  })

  it('getList with explicit pageSize sends that value, not the default', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { items: [] } })
    await channelApi.getList(5, 50)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/channels', {
      params: { node_id: 5, page_size: 50 },
    })
  })

  it('getList with array data returns array', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: [{ id: 1 }, { id: 2 }] })
    const res = await channelApi.getList()
    expect(Array.isArray(res)).toBe(true)
    expect(res).toEqual([{ id: 1 }, { id: 2 }])
  })

  it('getList drops malformed channel entries', async () => {
    mockClient.get.mockResolvedValue({
      code: 200,
      data: { items: [undefined, null, { id: 3, node_id: 1 }] },
    })

    const res = await channelApi.getList()

    expect(res).toEqual({ items: [{ id: 3, node_id: 1 }] })
  })

  it('getList with error code throws', async () => {
    mockClient.get.mockResolvedValue({ code: 400, data: null })
    await expect(channelApi.getList()).rejects.toThrow('获取通道列表失败')
  })

  it('getList with null data returns { items: [] }', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: null })
    const res = await channelApi.getList()
    expect(res).toEqual({ items: [] })
  })

  it('getById returns channel data', async () => {
    const ch = { id: 1, name: 'ch1' }
    mockClient.get.mockResolvedValue({ data: ch })
    const res = await channelApi.getById(1)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/channels/1')
    expect(res).toEqual(ch)
  })

  it('create posts and returns data', async () => {
    const ch = { id: 1, name: 'new' }
    mockClient.post.mockResolvedValue({ data: ch })
    const res = await channelApi.create({ name: 'new' })
    expect(res).toEqual(ch)
  })

  it('update calls put', async () => {
    mockClient.put.mockResolvedValue(undefined)
    await channelApi.update(1, { name: 'updated' })
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/channels/1', { name: 'updated' })
  })

  it('delete calls delete', async () => {
    mockClient.delete.mockResolvedValue(undefined)
    await channelApi.delete(1)
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/channels/1')
  })

  it('write posts hex data', async () => {
    const writeRes = { channel_id: 1, request_id: 10, success: true }
    mockClient.post.mockResolvedValue({ data: writeRes })
    const res = await channelApi.write(1, 'F4')
    expect(res).toEqual(writeRes)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/channels/1/write', { data: 'F4', hex_mode: true })
  })

  it('terminalWrite with readSize', async () => {
    const writeRes = { channel_id: 1, request_id: 11, success: true }
    mockClient.post.mockResolvedValue({ data: writeRes })
    const res = await channelApi.terminalWrite(1, 'dev1', 'AA', 4)
    expect(res).toEqual(writeRes)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/channels/1/terminal/write', {
      device_id: 'dev1', data_hex: 'AA', read_size: 4,
    })
  })

  it('terminalWrite without readSize omits it', async () => {
    mockClient.post.mockResolvedValue({ data: { channel_id: 1, request_id: 12, success: true } })
    await channelApi.terminalWrite(1, 'dev1', 'BB')
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/channels/1/terminal/write', {
      device_id: 'dev1', data_hex: 'BB',
    })
  })

  it('terminalWrite with readSize 0 omits it', async () => {
    mockClient.post.mockResolvedValue({ data: { channel_id: 1, request_id: 13, success: true } })
    await channelApi.terminalWrite(1, 'dev1', 'CC', 0)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/channels/1/terminal/write', {
      device_id: 'dev1', data_hex: 'CC',
    })
  })

  it('scan posts with defaults', async () => {
    const scanRes = { channel_id: 1, devices: ['0x48'] }
    mockClient.post.mockResolvedValue({ data: scanRes })
    const res = await channelApi.scan(1)
    expect(res).toEqual(scanRes)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/channels/1/scan', {
      scan_type: 'i2c', start_addr: 1, end_addr: 247, timeout_ms: 200,
    })
  })

  it('scan posts with custom options', async () => {
    mockClient.post.mockResolvedValue({ data: { channel_id: 1, devices: [] } })
    await channelApi.scan(1, { scan_type: 'uart', start_addr: 0, end_addr: 100, timeout_ms: 500 })
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/channels/1/scan', {
      scan_type: 'uart', start_addr: 0, end_addr: 100, timeout_ms: 500,
    })
  })

  it('reconfigure posts baudrate and clock', async () => {
    mockClient.post.mockResolvedValue({ data: { status: 'ok', request_id: 'r1' } })
    const res = await channelApi.reconfigure(1, 9600, 1000000)
    expect(res).toEqual({ status: 'ok', request_id: 'r1' })
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/channels/1/reconfigure', { baudrate: 9600, clock_hz: 1000000 })
  })

  it('reconfigure with clock 0 default', async () => {
    mockClient.post.mockResolvedValue({ data: { status: 'ok', request_id: 'r2' } })
    await channelApi.reconfigure(1, 115200)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/channels/1/reconfigure', { baudrate: 115200, clock_hz: 0 })
  })
})

// ─── data ───
import { dataApi } from '../data'

describe('dataApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('getOverview returns data', async () => {
    const overview = { nodes: { total: 1, online: 1, offline: 0 }, edge_devices: { total: 0, online: 0, offline: 0 }, latest_data: [] }
    mockClient.get.mockResolvedValue({ data: overview })
    const res = await dataApi.getOverview()
    expect(res).toEqual(overview)
  })

  it('getNodeDevicesData passes params', async () => {
    mockClient.get.mockResolvedValue({ data: [] })
    const res = await dataApi.getNodeDevicesData(1, { start_time: '2024-01-01', end_time: '2024-12-31' })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/nodes/1/latest', {
      params: { start_time: '2024-01-01', end_time: '2024-12-31' },
    })
    expect(res).toEqual([])
  })
})

// ─── dataSource ───
import { dataSourceApi } from '../dataSource'

describe('dataSourceApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('list calls get with params and unwraps { items, total }', async () => {
    // 2026-10-23 逻辑设备移除：组键维度 device_id → edge_device_id（契约 §6）
    const payload = { items: [{ id: 1, edge_device_id: 10, category: 'temperature' }], total: 1 }
    mockClient.get.mockResolvedValue({ code: 200, data: payload, message: 'ok' })
    const res = await dataSourceApi.list({ edge_device_id: 10, category: 'temperature', status: 'active', page: 1, page_size: 10 })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/data-sources', {
      params: { edge_device_id: 10, category: 'temperature', status: 'active', page: 1, page_size: 10 },
    })
    expect(res).toEqual(payload)
  })

  it('list without params forwards undefined params', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { items: [], total: 0 }, message: 'ok' })
    const res = await dataSourceApi.list()
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/data-sources', { params: undefined })
    expect(res).toEqual({ items: [], total: 0 })
  })

  it('get calls get with id and unwraps data', async () => {
    const source = { id: 1, edge_device_id: 10, category: 'temperature' }
    mockClient.get.mockResolvedValue({ code: 200, data: source, message: 'ok' })
    const res = await dataSourceApi.get(1)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/data-sources/1')
    expect(res).toEqual(source)
  })

  it('create posts body and unwraps data', async () => {
    const req = { category: 'temperature', edge_device_id: 100 }
    const created = { id: 1, ...req, name: 'ds1' }
    mockClient.post.mockResolvedValue({ code: 200, data: created, message: 'ok' })
    const res = await dataSourceApi.create(req)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/data-sources', req)
    expect(res).toEqual(created)
  })

  it('update puts body and unwraps data', async () => {
    const req = { name: 'updated', priority: 5 }
    const updated = { id: 1, name: 'updated', priority: 5 }
    mockClient.put.mockResolvedValue({ code: 200, data: updated, message: 'ok' })
    const res = await dataSourceApi.update(1, req)
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/data-sources/1', req)
    expect(res).toEqual(updated)
  })

  // 2026-10-24（方案 A）：remove / activate / deactivate / reset 四个封装方法
  // 已删除 —— 后端端点一并删除。它们的测试随之删除，**不留**「方法不存在但测试
  // 还在跑」的假绿。手工干预能力的覆盖在 sourceGroup 侧的测试里。

  it('getHealth passes limit and unwraps array', async () => {
    const records = [{ id: 1, source_id: 1, status: 'failure' }]
    mockClient.get.mockResolvedValue({ code: 200, data: records, message: 'ok' })
    const res = await dataSourceApi.getHealth(1, 10)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/data-sources/1/health', { params: { limit: 10 } })
    expect(res).toEqual(records)
  })

  it('getHealth 空数组返回 [] (不是 undefined)', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: [], message: 'ok' })
    const res = await dataSourceApi.getHealth(1)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/data-sources/1/health', { params: { limit: undefined } })
    expect(res).toEqual([])
    expect(res).toHaveLength(0)
  })

  it('getFailoverLogs 走边缘设备路径并透传 category/limit', async () => {
    // 2026-10-23 逻辑设备移除：路由 /devices/:id/failover-logs（:id 曾是逻辑设备 id）
    // 改为 /edge-devices/:id/failover-logs，响应项 device_id → edge_device_id。
    const logs = [{ id: 1, edge_device_id: 10, reason: 'auto', trigger: 'device_offline' }]
    mockClient.get.mockResolvedValue({ code: 200, data: logs, message: 'ok' })
    const res = await dataSourceApi.getFailoverLogs(10, { limit: 20, category: 'temperature' })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices/10/failover-logs', {
      params: { limit: 20, category: 'temperature' },
    })
    expect(res).toEqual(logs)
  })

  it('getFailoverLogs 空数组返回 [] (不是 undefined)', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: [], message: 'ok' })
    const res = await dataSourceApi.getFailoverLogs(10)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices/10/failover-logs', { params: undefined })
    expect(res).toEqual([])
  })
})

// ─── deviceConfig ───
import { deviceConfigApi } from '../deviceConfig'

describe('deviceConfigApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('getList returns response.data', async () => {
    // 2026-09-15 方言收敛：/device-configs 与全仓其余 11 个分页端点统一为 items。
    const listRes = { items: [{ id: 1 }], total: 1, page: 1, page_size: 20 }
    mockClient.get.mockResolvedValue({ code: 200, data: listRes, message: 'ok' })
    const res = await deviceConfigApi.getList({ device_type: 'sensor' })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/device-configs', { params: { device_type: 'sensor' } })
    expect(res).toEqual(listRes)
  })

  it('getDetail returns response.data', async () => {
    const detail = { id: 1, name: 'cfg1' }
    mockClient.get.mockResolvedValue({ code: 200, data: detail, message: 'ok' })
    const res = await deviceConfigApi.getDetail(1)
    expect(res).toEqual(detail)
  })

  it('create posts and returns data', async () => {
    const created = { id: 1, name: 'new' }
    mockClient.post.mockResolvedValue({ code: 200, data: created, message: 'ok' })
    const res = await deviceConfigApi.create({ name: 'new', device_type: 'sensor', hardware_type: 'i2c', config: {} })
    expect(res).toEqual(created)
  })

  it('update puts and returns data', async () => {
    const updated = { id: 1, name: 'updated' }
    mockClient.put.mockResolvedValue({ code: 200, data: updated, message: 'ok' })
    const res = await deviceConfigApi.update(1, { name: 'updated', device_type: 'sensor', hardware_type: 'i2c', config: {} })
    expect(res).toEqual(updated)
  })

  it('delete calls delete', async () => {
    mockClient.delete.mockResolvedValue(undefined)
    await deviceConfigApi.delete(1)
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/device-configs/1')
  })

  it('setDefault posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await deviceConfigApi.setDefault(1)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/device-configs/1/default')
  })

  it('getDefault returns data on success', async () => {
    const cfg = { id: 1, name: 'default-cfg' }
    mockClient.get.mockResolvedValue({ code: 200, data: cfg, message: 'ok' })
    const res = await deviceConfigApi.getDefault('sensor')
    expect(res).toEqual(cfg)
  })

  it('getDefault returns null on error', async () => {
    mockClient.get.mockRejectedValue(new Error('not found'))
    const res = await deviceConfigApi.getDefault('nonexistent')
    expect(res).toBeNull()
  })

  it('getByDeviceType returns list', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { items: [{ id: 1 }], total: 1, page: 1, page_size: 100 }, message: 'ok' })
    const res = await deviceConfigApi.getByDeviceType('sensor')
    expect(res).toEqual([{ id: 1 }])
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/device-configs', { params: { device_type: 'sensor', page_size: 100 } })
  })
})

// ─── driver ───
import { getDriverTree, getDriverList, getDriverDetail, transformToCascaderOptions, flattenDrivers } from '../driver'

describe('driver API', () => {
  beforeEach(() => vi.clearAllMocks())

  it('getDriverTree returns envelope.data', async () => {
    const tree = [{ id: 'bosch', name: 'Bosch' }]
    mockClient.get.mockResolvedValue({ data: tree })
    const res = await getDriverTree()
    expect(res).toEqual(tree)
  })

  it('getDriverTree returns [] when no data', async () => {
    mockClient.get.mockResolvedValue({})
    const res = await getDriverTree()
    expect(res).toEqual([])
  })

  // 2026-09-15 方言收敛：/device-configs 的 data 段由 `{list,...}` 改为 `{items,...}`
  // （全仓 12 个分页端点原为 11:1，现统一）。下面第三条**断言旧键必须被忽略**，
  // 这样「端点被改回 list」时前端不会静默返回空数组，而是测试变红。
  it('getDriverList returns items from envelope.data.items', async () => {
    const items = [{ type: 'bmp280', model: 'BMP280' }]
    mockClient.get.mockResolvedValue({ data: { items } })
    const res = await getDriverList()
    expect(res).toEqual(items)
  })

  it('getDriverList 不再兼容已废弃的 list 键（保留兼容会让形状漂移静默通过）', async () => {
    mockClient.get.mockResolvedValue({ data: { list: [{ type: 'stale' }] } })
    const res = await getDriverList()
    expect(res, '旧 list 键必须被忽略（方言已收敛为 items）').toEqual([])
  })

  it('getDriverList reads envelope.data.items and ignores data without .items', async () => {
    mockClient.get.mockResolvedValue({ data: { total: 1 } })
    const res = await getDriverList()
    expect(res).toEqual([])
  })

  it('getDriverList returns [] if not array', async () => {
    mockClient.get.mockResolvedValue({ data: 'not-array' })
    const res = await getDriverList()
    expect(res).toEqual([])
  })

  it('getDriverDetail returns envelope.data', async () => {
    const detail = { type: 'bmp280', display_name: 'BMP280' }
    mockClient.get.mockResolvedValue({ data: detail })
    const res = await getDriverDetail('bmp280')
    expect(res).toEqual(detail)
  })

  it('transformToCascaderOptions transforms tree', () => {
    const tree = [{
      id: 'bosch', name: 'Bosch',
      children: [{
        id: 'temp', name: 'Temperature',
        drivers: [{ type: 'bmp280', model: 'BMP280', display_name: 'BMP280', hardware_types: ['i2c'], description: 'Temp sensor' }]
      }]
    }]
    const options = transformToCascaderOptions(tree)
    expect(options).toHaveLength(1)
    expect(options[0].value).toBe('bosch')
    expect(options[0].children).toHaveLength(1)
    expect(options[0].children![0].children).toHaveLength(1)
    expect(options[0].children![0].children![0].value).toBe('bmp280')
  })

  it('transformToCascaderOptions skips OEMs with no children', () => {
    const tree = [{ id: 'empty', name: 'Empty', children: [] }]
    const options = transformToCascaderOptions(tree)
    expect(options).toHaveLength(0)
  })

  it('transformToCascaderOptions skips categories with no drivers', () => {
    const tree = [{ id: 'bosch', name: 'Bosch', children: [{ id: 'cat', name: 'Cat', drivers: [] }] }]
    const options = transformToCascaderOptions(tree)
    expect(options).toHaveLength(0)
  })

  it('flattenDrivers returns flat list', () => {
    const tree = [{
      id: 'bosch', name: 'Bosch',
      children: [{
        id: 'temp', name: 'Temperature',
        drivers: [
          { type: 'bmp280', model: 'BMP280', display_name: 'BMP280', hardware_types: ['i2c'], description: 'd1' },
          { type: 'bme680', model: 'BME680', display_name: 'BME680', hardware_types: ['i2c'], description: 'd2' },
        ]
      }]
    }]
    const flat = flattenDrivers(tree)
    expect(flat).toHaveLength(2)
    expect(flat[0].type).toBe('bmp280')
    expect(flat[1].type).toBe('bme680')
  })

  it('flattenDrivers handles nested children', () => {
    const tree = [{
      id: 'oem', name: 'OEM',
      drivers: [{ type: 'd1', model: 'D1', display_name: 'D1', hardware_types: [], description: '' }],
      children: [{
        id: 'cat', name: 'Cat',
        drivers: [{ type: 'd2', model: 'D2', display_name: 'D2', hardware_types: [], description: '' }],
      }]
    }]
    const flat = flattenDrivers(tree)
    expect(flat).toHaveLength(2)
  })
})

// ─── edgeDevice ───
import { edgeDeviceApi } from '../edgeDevice'

describe('edgeDeviceApi', () => {
  beforeEach(() => vi.clearAllMocks())

  // ── 负债 I-11: GET /edge-devices 改为真分页契约 {items,total,page,page_size} ──
  // 改前这里同时接受"data 是裸数组"与"data 是 {items,total}"两种形状, 并对裸数组
  // 回填 total = data.length —— 后端口径不明时前端照样能拼出"自洽"的分页信封,
  // 这正是"假分页"在两端都不报错的原因。以下断言全部收紧到**只认新契约**。

  it('getList ignores a bare array response (旧契约已废弃)', async () => {
    // 裸数组是新契约的反面: 必须返回空页, 而不是把它当成"全量一页"。
    mockClient.get.mockResolvedValue({ data: [{ id: 1, name: 'dev1', status: 'online' }] })
    const res = await edgeDeviceApi.getList()
    expect(res.total).toBe(0)
    expect(res.items).toEqual([])
  })

  it('getList drops malformed entries before normalization', async () => {
    mockClient.get.mockResolvedValue({
      data: {
        items: [
          undefined,
          null,
          { name: 'missing-id' },
          { id: 2, name: 'valid-device', status: 'active' },
        ],
        total: 4,
      },
    })

    const res = await edgeDeviceApi.getList()

    // total 用**后端给的** 4 (含无法渲染的条目), 不是压缩后的 1 ——
    // 分页器必须按后端口径算总页数, 否则与真实数据量对不上。
    expect(res.total).toBe(4)
    expect(res.items).toHaveLength(1)
    expect(res.items[0].id).toBe(2)
  })

  it('getList with envelope .data.items', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1, status: 'offline' }], total: 1 } })
    const res = await edgeDeviceApi.getList()
    expect(res.total).toBe(1)
    expect(res.items[0].status).toBe('offline')
  })

  it('getList 回显后端 clamp 后的 page/page_size', async () => {
    // 后端会把 page=0 归 1、page_size=100000 归 20 —— 前端受控值必须跟随回显,
    // 否则分页器显示 100000/页、实际拿到 20 条。
    mockClient.get.mockResolvedValue({
      data: { items: [{ id: 1 }], total: 25, page: 1, page_size: 20 },
    })
    const res = await edgeDeviceApi.getList({ page: 0, page_size: 100000 })
    expect(res.page).toBe(1)
    expect(res.page_size).toBe(20)
    expect(res.total).toBe(25)
  })

  it('getList 旧后端缺省回显时以请求值兜底', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1 }], total: 25 } })
    const res = await edgeDeviceApi.getList({ page: 3, page_size: 10 })
    expect(res.page).toBe(3)
    expect(res.page_size).toBe(10)
  })

  it('getList with unexpected format returns empty', async () => {
    mockClient.get.mockResolvedValue({ data: null })
    const res = await edgeDeviceApi.getList()
    expect(res.items).toEqual([])
    expect(res.total).toBe(0)
  })

  it('getList passes params', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [], total: 0 } })
    await edgeDeviceApi.getList({ node_id: 5, status: 'online' })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices', { params: { node_id: 5, status: 'online' } })
  })

  it('getList 转发服务端筛选参数 (search/hardware_type)', async () => {
    // 筛选下沉服务端 (§3.3.5) 的前提是这些参数真的被发出去 —— 少传一个,
    // 后端就收不到、页面就退回"本地过滤伪装全局检索"。
    mockClient.get.mockResolvedValue({ data: { items: [], total: 0 } })
    await edgeDeviceApi.getList({ search: 'bms', hardware_type: 'uart', page: 2, page_size: 24 })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices', {
      params: { search: 'bms', hardware_type: 'uart', page: 2, page_size: 24 },
    })
  })

  it('create forwards device_config_id required by the backend', async () => {
    mockClient.post.mockResolvedValue({ data: { id: 9 } })

    await edgeDeviceApi.create({
      name: 'BMS',
      node_id: '30EDA0A9A808',
      channel_id: 1,
      type: 'jiabaida_bms',
      hardware_id: 'UART0',
      device_config_id: 42,
    })

    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/edge-devices', {
      name: 'BMS',
      node_id: '30EDA0A9A808',
      channel_id: 1,
      type: 'jiabaida_bms',
      hardware_id: 'UART0',
      device_config_id: 42,
    })
  })

  it('create forwards command_intervals (EDGE-WIZ-004 per-command polling)', async () => {
    mockClient.post.mockResolvedValue({ data: { id: 9 } })

    await edgeDeviceApi.create({
      name: 'BMS',
      node_id: '30EDA0A9A808',
      channel_id: 1,
      type: 'jiabaida_bms',
      hardware_id: 'UART0',
      command_intervals: {
        read_basic_info: 10000,
        read_cell_voltage: 0,
        read_hardware_version: 0,
        read_comprehensive: 0,
        read_protection_count: 0,
      },
    })

    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/edge-devices', {
      name: 'BMS',
      node_id: '30EDA0A9A808',
      channel_id: 1,
      type: 'jiabaida_bms',
      hardware_id: 'UART0',
      command_intervals: {
        read_basic_info: 10000,
        read_cell_voltage: 0,
        read_hardware_version: 0,
        read_comprehensive: 0,
        read_protection_count: 0,
      },
    })
  })

  it('getDetail with envelope data', async () => {
    mockClient.get.mockResolvedValue({ data: { id: 1, name: 'dev1', status: 'online' } })
    const res = await edgeDeviceApi.getDetail(1)

    expect(res.id).toBe(1)
    expect(res.status).toBe('online')
  })

  it('getDetail maps config_version and node.firmware_version', async () => {
    mockClient.get.mockResolvedValue({
      data: {
        id: 1,
        name: 'bms-1',
        status: 'active',
        node_id: 'F0F5BDFFFE02',
        config_version: 'v2-abc123',
        node: { id: 7, name: 'node-A', firmware_version: '2.5.21' },
      },
    })
    const res = await edgeDeviceApi.getDetail(1)
    expect(res.config_version).toBe('v2-abc123')
    expect(res.node).toEqual({ id: 7, name: 'node-A', firmware_version: '2.5.21' })
  })

  it('getDetail omits empty config_version and node firmware', async () => {
    mockClient.get.mockResolvedValue({
      data: { id: 1, name: 'bms-1', status: 'active', node_id: 'F0F5BDFFFE02', config_version: '', node: { id: 7, name: 'node-A' } },
    })
    const res = await edgeDeviceApi.getDetail(1)
    expect(res.config_version).toBeUndefined()
    expect(res.node?.firmware_version).toBeUndefined()
  })

  it('getDetail keeps an unnamed node for id/firmware (display falls back to node_id)', async () => {
    mockClient.get.mockResolvedValue({
      data: { id: 1, name: 'bms-1', status: 'active', node_id: 'F0F5BDFFFE02', node: { id: 1, name: '', firmware_version: '2.5.21' } },
    })
    const res = await edgeDeviceApi.getDetail(1)
    expect(res.node).toEqual({ id: 1, name: '', firmware_version: '2.5.21' })
  })

  it('create with envelope data.id', async () => {
    mockClient.post.mockResolvedValue({ data: { id: 2 } })
    const res = await edgeDeviceApi.create({ name: 'new' })
    expect(res).toEqual({ id: 2 })
  })

  it('update calls put', async () => {
    mockClient.put.mockResolvedValue(undefined)
    await edgeDeviceApi.update(1, { name: 'updated' })
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/edge-devices/1', { name: 'updated' })
  })

  it('delete calls delete', async () => {
    mockClient.delete.mockResolvedValue(undefined)
    await edgeDeviceApi.delete(1)
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/edge-devices/1')
  })

  it('delete with delete_data=true appends the query param (方案 v3.3 §2.1)', async () => {
    mockClient.delete.mockResolvedValue(undefined)
    await edgeDeviceApi.delete(1, { delete_data: true })
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/edge-devices/1', {
      params: { delete_data: 'true' },
    })
  })

  it('delete with delete_data=false keeps the legacy request shape', async () => {
    mockClient.delete.mockResolvedValue(undefined)
    await edgeDeviceApi.delete(2, { delete_data: false })
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/edge-devices/2')
  })

  // 2026-10-23 逻辑设备移除：getLogicalDeviceInfo / getCandidates 已随逻辑身份层
  // 一起删除（后端 /edge-devices/:id/logical-device-info、/edge-devices/candidates
  // 端点不存在）。替代能力是下面三组端点。
  it('does not expose retired logical-device endpoints', () => {
    expect(edgeDeviceApi).not.toHaveProperty('getLogicalDeviceInfo')
    expect(edgeDeviceApi).not.toHaveProperty('getCandidates')
  })

  it('getDeleted reads paginated {items,total,page,page_size} envelope', async () => {
    const items = [{
      id: 3, name: '旧BMS', device_type: 'bms', hardware_id: '1',
      node_id: 'N1', channel_id: 5, deleted_at: '2026-10-01T00:00:00Z',
      last_data_at: '2026-09-30T00:00:00Z', row_estimate: 1234,
      data_purge_requested: false, purge_in_progress: false,
    }]
    mockClient.get.mockResolvedValue({
      code: 200,
      data: { items, total: 61, page: 2, page_size: 50 },
      message: 'ok',
    })
    const res = await edgeDeviceApi.getDeleted({ page: 2, page_size: 50 })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices/deleted', {
      params: { page: 2, page_size: 50 },
    })
    // 2026-10-25 新契约：必须消费 total（否则 >page_size 的已删设备被静默隐藏）
    expect(res).toEqual({ items, total: 61, page: 2, page_size: 50 })
  })

  it('getDeleted 透传 device_type 精确型号过滤（合并弹窗服务端过滤）', async () => {
    mockClient.get.mockResolvedValue({
      code: 200,
      data: { items: [], total: 0, page: 1, page_size: 200 },
      message: 'ok',
    })
    await edgeDeviceApi.getDeleted({ device_type: 'jiabaida_bms', page: 1, page_size: 200 })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices/deleted', {
      params: { device_type: 'jiabaida_bms', page: 1, page_size: 200 },
    })
  })

  it('getDeleted 容忍裸数组（按单页归一，不伪造 total）并忽略非数组形状', async () => {
    mockClient.get.mockResolvedValueOnce({ code: 200, data: [{ id: 1 }] })
    expect(await edgeDeviceApi.getDeleted()).toEqual({ items: [{ id: 1 }], total: 1, page: 1, page_size: 1 })
    mockClient.get.mockResolvedValueOnce({ code: 200, data: { unexpected: true } })
    expect(await edgeDeviceApi.getDeleted()).toEqual({ items: [], total: 0, page: 1, page_size: 0 })
  })

  it('restore posts binding body and normalizes the returned device', async () => {
    mockClient.post.mockResolvedValue({
      code: 200,
      data: { id: 3, name: '旧BMS', node_id: 'N2', channel_id: 9, type: 'bms', status: 'pending' },
      message: 'ok',
    })
    const res = await edgeDeviceApi.restore(3, { node_id: 'N2', channel_id: 9 })
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/edge-devices/3/restore', { node_id: 'N2', channel_id: 9 })
    expect(res.id).toBe(3)
    expect(res.node_id).toBe('N2')
    expect(res.channel_id).toBe(9)
  })

  it('getDataSummary 解析 2026-10-25 新契约：target/sources 分组 + 各自合计', async () => {
    // 真实后端形状（backend handler_edge_device.go 的 data-summary 响应）
    mockClient.get.mockResolvedValue({
      code: 200,
      data: {
        target: { source_id: 1, device_name: '目标设备', device_type: 'jiabaida_bms', state: 'live', rows: 200, first_time: '2026-09-01T00:00:00Z', last_time: '2026-10-01T00:00:00Z' },
        sources: [
          { source_id: 2, device_name: '旧BMS', device_type: 'jiabaida_bms', state: 'deleted', rows: 500, first_time: '2026-08-01T00:00:00Z', last_time: '2026-09-01T00:00:00Z' },
        ],
        target_rows: 200,
        source_rows: 500,
      },
      message: 'ok',
    })
    const res = await edgeDeviceApi.getDataSummary(1, [2, 3])
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices/1/data-summary', {
      params: { source_ids: '2,3' },
    })
    // 目标与源绝不混用：「预计迁移」只能读 source_rows（不含目标自身）
    expect(res.target_rows).toBe(200)
    expect(res.source_rows).toBe(500)
    expect(res.target?.state).toBe('live')
    // 2026-10-25 产品重做：item 新增 device_type，供合并弹窗做「同型号」准入校验
    expect(res.target?.device_type).toBe('jiabaida_bms')
    expect(res.sources[0].device_name).toBe('旧BMS')
    expect(res.sources[0].device_type).toBe('jiabaida_bms')
    expect(res.sources[0].state).toBe('deleted')
  })

  it('getDataSummary 两组合计缺失时保持 undefined（不兜底成 0）', async () => {
    mockClient.get.mockResolvedValue({
      code: 200,
      data: {
        target: { source_id: 1, rows: null, first_time: null, last_time: null },
        sources: [{ source_id: 2, rows: null, first_time: null, last_time: null, state: 'missing' }],
      },
      message: 'ok',
    })
    const res = await edgeDeviceApi.getDataSummary(1, [2])
    // 缺失必须是 undefined，不能是 0（0 会被 UI 渲染成「预计迁移 0 行」，
    // 而实际可能有百万行 —— 不可逆操作前的规模提示错得最危险）
    expect(res.source_rows).toBeUndefined()
    expect(res.target_rows).toBeUndefined()
    expect(res.sources[0].rows).toBeNull()
  })

  it('getDataSummary 兼容旧 items/total_rows 形状（items[0]=目标，其余=源）', async () => {
    mockClient.get.mockResolvedValue({
      code: 200,
      data: {
        items: [
          { source_id: 1, rows: 200, first_time: null, last_time: null },
          { source_id: 2, rows: 500, first_time: null, last_time: null },
        ],
        total_rows: 700,
      },
      message: 'ok',
    })
    const res = await edgeDeviceApi.getDataSummary(1, [2])
    expect(res.target?.source_id).toBe(1)
    expect(res.target_rows).toBe(200)
    expect(res.sources.map(s => s.source_id)).toEqual([2])
    // 旧 total_rows 含目标自身，不能拿来冒充 source_rows
    expect(res.source_rows).toBeUndefined()
  })

  it('mergeHistory 把 source_ids 收敛为正整数，请求体只有 source_ids（无 allow_live_source）', async () => {
    mockClient.post.mockResolvedValue({
      code: 200,
      data: {
        rows_migrated: 42,
        sources: [{ id: 2, state: 'deleted', device_type: 'jiabaida_bms' }],
        warnings: [],
      },
      message: 'ok',
    })
    // el-select allow-create 产出的是字符串：封装层必须 map(Number)，并过滤 0/负数/NaN、去重
    const res = await edgeDeviceApi.mergeHistory(1, ['2', 3, 3, 0, -1, Number.NaN] as unknown as number[])
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/edge-devices/1/merge-history', {
      source_ids: [2, 3],
    })
    // 2026-10-25 产品重做：在役设备不是合法源，请求体不存在 allow_live_source
    const body = mockClient.post.mock.calls[0][1] as Record<string, unknown>
    expect(Object.keys(body)).toEqual(['source_ids'])
    expect(res.rows_migrated).toBe(42)
    expect(res.sources).toEqual([{ id: 2, state: 'deleted', device_type: 'jiabaida_bms' }])
    expect(res.warnings).toEqual([])
  })

  it('mergeHistory 解析新响应：sources 带 device_type、warnings 透传（无 retired_sources）', async () => {
    mockClient.post.mockResolvedValue({
      code: 200,
      data: {
        rows_migrated: 0,
        sources: [{ id: 2, state: 'deleted', device_type: 'jiabaida_bms' }],
        warnings: ['人工 data_sources 与目标冲突，已保留在源设备上', 123],
      },
      message: 'ok',
    })
    const res = await edgeDeviceApi.mergeHistory(1, [2])
    expect(res.warnings).toEqual(['人工 data_sources 与目标冲突，已保留在源设备上'])
    expect(res).not.toHaveProperty('retired_sources')
    expect(res.sources[0].device_type).toBe('jiabaida_bms')
    expect(res.sources[0].state).toBe('deleted')
  })

  it('mergeHistory 响应缺 sources 时回填请求 id（state unknown），不谎报成功/退役', async () => {
    mockClient.post.mockResolvedValue({ code: 200, data: { rows_migrated: 7 }, message: 'ok' })
    const res = await edgeDeviceApi.mergeHistory(1, [2])
    expect(res.sources).toEqual([{ id: 2, state: 'unknown' }])
    expect(res.warnings).toEqual([])
    expect(res).not.toHaveProperty('retired_sources')
  })

  it('getDeleted 透传 search 服务端模糊检索（历史设备视图）', async () => {
    mockClient.get.mockResolvedValue({
      code: 200,
      data: { items: [], total: 0, page: 1, page_size: 20 },
      message: 'ok',
    })
    await edgeDeviceApi.getDeleted({ search: 'bms', page: 1, page_size: 20 })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices/deleted', {
      params: { search: 'bms', page: 1, page_size: 20 },
    })
  })

  it('batchUpdate posts enabled and unwraps per-item results', async () => {
    mockClient.post.mockResolvedValue({
      code: 200,
      data: {
        total: 2,
        succeeded: 1,
        failed: 1,
        results: [
          { id: 1, success: true, enabled: false },
          { id: 2, success: false, error: '被来源组规则引用' },
        ],
      },
      message: 'ok',
    })
    const res = await edgeDeviceApi.batchUpdate([1, 2], { enabled: false })
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/edge-devices/batch-update', {
      ids: [1, 2],
      enabled: false,
    })
    expect(res).toEqual({
      total: 2,
      succeeded: 1,
      failed: 1,
      results: [
        { id: 1, success: true, enabled: false },
        { id: 2, success: false, error: '被来源组规则引用' },
      ],
    })
  })

  it('batchUpdate 透传 interval_ms 并收敛 ids 为正整数', async () => {
    mockClient.post.mockResolvedValue({
      code: 200,
      data: { total: 1, succeeded: 1, failed: 0, results: [{ id: 3, success: true, interval_ms: 5000 }] },
      message: 'ok',
    })
    await edgeDeviceApi.batchUpdate([3, 0, Number.NaN] as unknown as number[], { interval_ms: 5000 })
    const body = mockClient.post.mock.calls[0][1] as Record<string, unknown>
    expect(body).toEqual({ ids: [3], interval_ms: 5000 })
  })

  it('batchDeleteHistory posts ids to deleted/batch-delete and maps queued', async () => {
    mockClient.post.mockResolvedValue({
      code: 200,
      data: {
        total: 2,
        succeeded: 1,
        failed: 1,
        results: [
          { id: 5, success: true, queued: true },
          { id: 6, success: false, error: '设备不存在' },
        ],
      },
      message: 'ok',
    })
    const res = await edgeDeviceApi.batchDeleteHistory([5, 6])
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/edge-devices/deleted/batch-delete', { ids: [5, 6] })
    expect(res.succeeded).toBe(1)
    expect(res.failed).toBe(1)
    expect(res.results[0]).toEqual({ id: 5, success: true, queued: true })
    expect(res.results[1].error).toBe('设备不存在')
  })

  it('batch 响应缺字段时按 results 统计（不伪造成功/失败数）', async () => {
    mockClient.post.mockResolvedValue({
      code: 200,
      data: { results: [{ id: 1, success: true }, { id: 2, success: false }] },
      message: 'ok',
    })
    const res = await edgeDeviceApi.batchDeleteHistory([1, 2])
    expect(res).toEqual({
      total: 2,
      succeeded: 1,
      failed: 1,
      results: [{ id: 1, success: true }, { id: 2, success: false }],
    })
  })

  it('does not expose retired direct control endpoints', () => {
    expect(edgeDeviceApi).not.toHaveProperty('executeOperation')
    expect(edgeDeviceApi).not.toHaveProperty('changeAddress')
  })

  it('getLatestData returns data', async () => {
    mockClient.get.mockResolvedValue({ data: { temp: 25 } })
    const res = await edgeDeviceApi.getLatestData(1)
    expect(res).toEqual({ temp: 25 })
  })

  it('getHistoryData passes params', async () => {
    mockClient.get.mockResolvedValue({ data: [] })
    await edgeDeviceApi.getHistoryData(1, { start_time: '2024-01-01', end_time: '2024-12-31' })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices/1/data', {
      params: { start_time: '2024-01-01', end_time: '2024-12-31' },
    })
  })

  // ⚠ 2026-10-11：原 3 条 getOperationHistory 用例已随该方法一并删除。
  //   该方法指向 `/edge-devices/:id/operations/history` —— 后端**从未实现**（实测 404），
  //   且全仓无任何调用方。这 3 条此前是绿的，却只证明"一个没人用的封装能解析 mock"：
  //   锁的是死代码形状，不是对外契约 ⇒ 删方法的同批删测试。
  //   受控操作历史的真实契约由 deviceOperationApi.list()（/operations）覆盖。

  it('normalize maps unknown status', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1, status: 'weird_status' }], total: 1 } })
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].status).toBe('unknown')
  })

  it('normalize maps empty status to offline', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1 }], total: 1 } })
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].status).toBe('offline')
  })

  it('normalize falls back device_type from type', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1, type: 'sensor' }], total: 1 } })
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].device_type).toBe('sensor')
  })

  it('normalize uses channel.hardware_type fallback', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1, channel: { hardware_type: 'i2c', hardware_id: 'I2C0' } }], total: 1 } })
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].hardware_type).toBe('i2c')
    // G4 (task-10)：**本条副断言改前是 .toBe('I2C0')，把缺陷钉成了期望行为** ——
    //   EdgeDevice.hardware_id 是设备从站地址，通道的 hardware_id ('I2C0') 是总线名，
    //   两者语义不同。旧 normalize 的 `|| d.channel?.hardware_id` 兜底让"设备地址为空"
    //   时把总线名填进来，编辑框于是以 "I2C0" 打开，用户点保存就把总线名写回后端。
    //   现在：设备地址为空 ⇒ hardware_id 就是空（不再跨语义兜底），
    //   总线名照旧可从 channel_hardware_id 取到（展示层唯一入口）。
    expect(res.items[0].hardware_id).toBe('')
    expect(res.items[0].channel_hardware_id).toBe('I2C0')
  })

  it('normalize uses node info', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1, node: { id: 5, name: 'node5' } }], total: 1 } })
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].node).toEqual({ id: 5, name: 'node5' })
  })

  it('normalize keeps an unnamed node for its id (link/firmware survive)', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1, node: { id: 5 } }], total: 1 } })
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].node).toEqual({ id: 5, name: '' })
  })
})

// ─── firmware ───
import { firmwareApi } from '../firmware'

describe('firmwareApi', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('getList returns list', async () => {
    mockClient.get.mockResolvedValue({ data: [{ id: 1, version: '1.0' }] })
    const res = await firmwareApi.getList({ model: 'esp32' })
    expect(res.list).toHaveLength(1)
    expect(res.total).toBe(1)
  })

  it('getList with null data returns empty', async () => {
    mockClient.get.mockResolvedValue({ data: null })
    const res = await firmwareApi.getList()
    expect(res).toEqual({ total: 0, list: [] })
  })

  it('upload posts FormData', async () => {
    const fd = new FormData()
    mockClient.post.mockResolvedValue({ data: { id: 1, version: '2.0' } })
    const res = await firmwareApi.upload(fd)
    expect(res.version).toBe('2.0')
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/firmwares/upload', fd)
  })

  it('upload reads the unified envelope data', async () => {
    // 后端统一 envelope: {code, data: Firmware, message}
    const fd = new FormData()
    mockClient.post.mockResolvedValue({ code: 201, message: 'ok', data: { id: 9, version: '3.1', filename: 'fw.bin' } })
    const res = await firmwareApi.upload(fd)
    expect(res).toEqual({ id: 9, version: '3.1', filename: 'fw.bin' })
    expect(res.version).toBe('3.1')
  })

  it('update calls put', async () => {
    mockClient.put.mockResolvedValue(undefined)
    await firmwareApi.update(1, { version: '2.0' })
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/firmwares/1', { version: '2.0' })
  })

  it('delete calls delete', async () => {
    mockClient.delete.mockResolvedValue(undefined)
    await firmwareApi.delete(1)
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/firmwares/1')
  })

  it('getDownloadUrl includes token', () => {
    localStorage.setItem('token', 'jwt-123')
    const url = firmwareApi.getDownloadUrl('fw.bin')
    expect(url).toContain('fw.bin')
    expect(url).toContain('token=jwt-123')
  })

  it('getDownloadUrl without token', () => {
    const url = firmwareApi.getDownloadUrl('fw.bin')
    expect(url).toContain('fw.bin')
    expect(url).toContain('token=')
  })
})

// ─── homeassistant ───
import { haApi } from '../homeassistant'

describe('haApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('syncDevice posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await haApi.syncDevice(1)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/ha/sync/1')
  })

  it('syncNode posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await haApi.syncNode(5)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/ha/sync/node/5')
  })

  it('syncAll posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await haApi.syncAll()
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/ha/sync/all')
  })

  it('removeDevice deletes', async () => {
    mockClient.delete.mockResolvedValue(undefined)
    await haApi.removeDevice(1)
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/ha/device/1')
  })
})

// ─── monitor ───
import { getMetricsSummary, getMetrics } from '../monitor'

describe('monitor API', () => {
  beforeEach(() => vi.clearAllMocks())

  it('getMetricsSummary calls get', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: {} })
    await getMetricsSummary()
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/metrics/summary')
  })

  it('getMetrics calls get', async () => {
    mockClient.get.mockResolvedValue({})
    await getMetrics()
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/metrics')
  })
})

// ─── node ───
import { nodeApi } from '../node'

describe('nodeApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('getList requires the envelope and ignores a bare array response', async () => {
    mockClient.get.mockResolvedValue([{ id: 1, node_id: 'n1' }])
    const res = await nodeApi.getList()
    expect(res).toEqual({ total: 0, page: 1, page_size: 20, items: [] })
  })

  it('getList with envelope .data.items', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1 }], total: 1, page: 1, page_size: 20 } })
    const res = await nodeApi.getList()
    expect(res.total).toBe(1)
  })

  // 负债 I-11: nodeApi.getList 不再兼容"后端返回裸数组" —— 该分支对裸数组回填
  // page_size = inner.length, 会掩盖"后端忽略 page 参数"。裸数组现在必须返回空页。
  it('getList 旧契约(裸数组)已废弃, 返回空页', async () => {
    mockClient.get.mockResolvedValue({ data: [{ id: 1 }] })
    const res = await nodeApi.getList({ page: 2, page_size: 10 })
    expect(res.items).toEqual([])
    expect(res.page).toBe(2)
  })

  it('getList 回显后端 clamp 后的 page/page_size', async () => {
    mockClient.get.mockResolvedValue({
      data: { items: [{ id: 1 }], total: 25, page: 1, page_size: 20 },
    })
    const res = await nodeApi.getList({ page: 0, page_size: 100000 })
    expect(res.page).toBe(1)
    expect(res.page_size).toBe(20)
    expect(res.total).toBe(25)
  })

  it('getList 旧后端缺省回显时以请求值兜底', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1 }], total: 25 } })
    const res = await nodeApi.getList({ page: 3, page_size: 10 })
    expect(res.page).toBe(3)
    expect(res.page_size).toBe(10)
  })

  it('getList 转发服务端筛选参数 (search/status/model)', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [], total: 0 } })
    await nodeApi.getList({ search: 'esp32', status: 'online', model: 'ESP32-C6', page: 2 })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/nodes', {
      params: { search: 'esp32', status: 'online', model: 'ESP32-C6', page: 2 },
    })
  })

  it('getList fallback returns empty', async () => {
    mockClient.get.mockResolvedValue('unexpected')
    const res = await nodeApi.getList()
    expect(res).toEqual({ total: 0, page: 1, page_size: 20, items: [] })
  })

  it('getDetail with envelope returns data', async () => {
    mockClient.get.mockResolvedValue({ data: { id: 1, node_id: 'n1' } })
    const res = await nodeApi.getDetail(1)
    expect(res.node_id).toBe('n1')
  })

  it('delete calls delete', async () => {
    mockClient.delete.mockResolvedValue(undefined)
    await nodeApi.delete(1)
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/nodes/1')
  })

  it('getConfig returns data', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { uart: [] }, message: 'ok' })
    const res = await nodeApi.getConfig(1)
    expect(res).toEqual({ uart: [] })
  })

  it('updateConfig puts config', async () => {
    mockClient.put.mockResolvedValue(undefined)
    await nodeApi.updateConfig(1, { uart: [{ baud: 9600 }] })
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/nodes/1/config', { uart: [{ baud: 9600 }] })
  })

  it('syncConfig posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await nodeApi.syncConfig(1)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/nodes/1/config/sync')
  })

  it('startOTA posts with force', async () => {
    mockClient.post.mockResolvedValue({ code: 200, data: { ota_record_id: 1, status: 'started' }, message: 'ok' })
    const res = await nodeApi.startOTA(1, 5, true)
    expect(res).toEqual({ ota_record_id: 1, status: 'started' })
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/ota/tasks', { node_id: 1, firmware_id: 5, force: true })
  })

  it('startOTA default force=false', async () => {
    mockClient.post.mockResolvedValue({ code: 200, data: { ota_record_id: 2, status: 'started' }, message: 'ok' })
    await nodeApi.startOTA(1, 5)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/ota/tasks', { node_id: 1, firmware_id: 5, force: false })
  })

  it('getOTAProgress returns data', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { id: 1, progress: 50 }, message: 'ok' })
    const res = await nodeApi.getOTAProgress(1, 1)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/ota/tasks/1')
    expect(res.progress).toBe(50)
  })

  it('getOTAHistory returns array', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: [{ id: 1 }], message: 'ok' })
    const res = await nodeApi.getOTAHistory(1)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/nodes/1/ota/history')
    expect(res).toHaveLength(1)
  })

  it('cancelOTA posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await nodeApi.cancelOTA(1, 1)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/ota/tasks/1/cancel')
  })

  it('getHardwareConfig returns data', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { uart: [] }, message: 'ok' })
    const res = await nodeApi.getHardwareConfig(1)
    expect(res).toEqual({ uart: [] })
  })

  it('updateHardwareConfig puts', async () => {
    mockClient.put.mockResolvedValue(undefined)
    await nodeApi.updateHardwareConfig(1, { uart: [] })
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/nodes/1/hardware/config', { hardware: { uart: [] } })
  })

  it('getCapabilities returns data', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { model: 'ESP32' }, message: 'ok' })
    const res = await nodeApi.getCapabilities(1)
    expect(res.model).toBe('ESP32')
  })

  it('queryResources returns data', async () => {
    mockClient.post.mockResolvedValue({ code: 200, data: { request_id: 'req1' }, message: 'ok' })
    const res = await nodeApi.queryResources(1)
    expect(res.request_id).toBe('req1')
  })

  it('scanI2C returns devices', async () => {
    mockClient.post.mockResolvedValue({ code: 200, data: { devices: ['0x48'] }, message: 'ok' })
    const res = await nodeApi.scanI2C(1, 'I2C0')
    expect(res.devices).toEqual(['0x48'])
  })

  it('ping returns timestamp', async () => {
    mockClient.post.mockResolvedValue({ code: 200, data: { timestamp_us: '12345' }, message: 'ok' })
    const res = await nodeApi.ping(1)
    expect(res.timestamp_us).toBe('12345')
  })

  it('getDmaChannels returns list', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { dma_channels: [{ dma_id: 1, name: 'DMA0' }] }, message: 'ok' })
    const res = await nodeApi.getDmaChannels(1)
    expect(res).toHaveLength(1)
  })

  it('getDmaChannels with empty data returns []', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: {}, message: 'ok' })
    const res = await nodeApi.getDmaChannels(1)
    expect(res).toEqual([])
  })

  it('updateDmaConfig puts configs', async () => {
    mockClient.put.mockResolvedValue(undefined)
    await nodeApi.updateDmaConfig(1, [{ dma_id: 1, enabled: true, bind_to: 'SPI2' }])
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/nodes/1/dma-config', [{ dma_id: 1, enabled: true, bind_to: 'SPI2' }])
  })
})

// ─── notification ───
import { getNotifications, getUnreadCount, markAsRead, markAllAsRead } from '../notification'

describe('notification API', () => {
  beforeEach(() => vi.clearAllMocks())

  it('getNotifications returns array', async () => {
    const notifs = [{ id: 1, type: 'info', title: 't', description: 'd', source: 's', read: false, created_at: '' }]
    mockClient.get.mockResolvedValue({ data: notifs })
    const res = await getNotifications(10)
    expect(res).toEqual(notifs)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/notifications?limit=10')
  })

  it('getNotifications default limit', async () => {
    mockClient.get.mockResolvedValue({ data: [] })
    await getNotifications()
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/notifications?limit=20')
  })

  it('getNotifications null data returns []', async () => {
    mockClient.get.mockResolvedValue({ data: null })
    const res = await getNotifications()
    expect(res).toEqual([])
  })

  it('getUnreadCount returns count', async () => {
    mockClient.get.mockResolvedValue({ data: { count: 5 } })
    const res = await getUnreadCount()
    expect(res).toBe(5)
  })

  it('getUnreadCount null data returns 0', async () => {
    mockClient.get.mockResolvedValue({})
    const res = await getUnreadCount()
    expect(res).toBe(0)
  })

  it('markAsRead calls put', async () => {
    mockClient.put.mockResolvedValue(undefined)
    await markAsRead(1)
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/notifications/1/read')
  })

  it('markAllAsRead calls post', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await markAllAsRead()
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/notifications/read-all')
  })
})

// ─── parser ───
import { parserApi } from '../parser'

describe('parserApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('getList requests status=active filter', async () => {
    mockClient.get.mockResolvedValue({
      data: {
        items: [
          { id: 42, type: 'bmp280', display_name: 'BMP280', oem: 'Bosch', category: 'temp', hardware_types: ['i2c'], measure_type: ['temperature'], description: 'desc' }
        ]
      }
    })
    await parserApi.getList()
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/device-configs', { params: { status: 'active' } })
  })

  it('getList normalizes drivers', async () => {
    mockClient.get.mockResolvedValue({
      data: {
        items: [
          { id: 42, type: 'bmp280', display_name: 'BMP280', oem: 'Bosch', category: 'temp', hardware_types: ['i2c'], measure_type: ['temperature'], description: 'desc' }
        ]
      }
    })
    const res = await parserApi.getList()
    expect(res).toHaveLength(1)
    expect(res[0].id).toBe('bmp280')
    expect(res[0].device_config_id).toBe(42)
    expect(res[0].vendor).toBe('Bosch')
    expect(res[0].hardware_types).toEqual(['i2c'])
  })

  it('getList resolves hardware_types via bus_types fallback', async () => {
    mockClient.get.mockResolvedValue({
      data: { items: [{ type: 'bmp280', display_name: 'BMP280', bus_types: ['spi'] }] }
    })
    const res = await parserApi.getList()
    expect(res[0].hardware_types).toEqual(['spi'])
  })

  it('getList resolves hardware_types via hardware_type fallback', async () => {
    mockClient.get.mockResolvedValue({
      data: { items: [{ type: 'bmp280', display_name: 'BMP280', hardware_type: 'I2C' }] }
    })
    const res = await parserApi.getList()
    expect(res[0].hardware_types).toEqual(['i2c'])
  })

  it('getList resolves hardware_types via connection.bus_type fallback', async () => {
    mockClient.get.mockResolvedValue({
      data: { items: [{ type: 'bmp280', display_name: 'BMP280', connection: { bus_type: 'SPI' } }] }
    })
    const res = await parserApi.getList()
    expect(res[0].hardware_types).toEqual(['spi'])
  })

  it('getList resolves hardware_types via protocol fallback', async () => {
    mockClient.get.mockResolvedValue({
      data: { items: [{ type: 'bmp280', display_name: 'BMP280', protocol: 'MODBUS' }] }
    })
    const res = await parserApi.getList()
    expect(res[0].hardware_types).toEqual(['modbus'])
  })

  it('getList resolves measure_types from measure_type string', async () => {
    mockClient.get.mockResolvedValue({
      data: { items: [{ type: 'bmp280', display_name: 'BMP280', measure_type: 'temperature' }] }
    })
    const res = await parserApi.getList()
    expect(res[0].measure_types).toEqual(['temperature'])
  })

  it('getById normalizes', async () => {
    mockClient.get.mockResolvedValue({ data: { type: 'bmp280', display_name: 'BMP280', oem: 'Bosch', hardware_types: ['i2c'] } })
    const res = await parserApi.getById('bmp280')
    expect(res.id).toBe('bmp280')
    expect(res.vendor).toBe('Bosch')
  })

  // 回归: 新部署/空 device_configs 表时, tree 内置驱动兜底 (issue: 创建向导型号列表为空)
  it('getList falls back to built-in drivers from tree when DB device-configs is empty', async () => {
    // DB 列表空数组 + tree 含内置驱动
    mockClient.get.mockResolvedValueOnce({
      data: { items: [], total: 0, page: 1, page_size: 20, code: 200 },
    })
    mockClient.get.mockResolvedValueOnce({
      data: [
        {
          id: 'bosch', name: '博世',
          children: [{
            id: 'temp', name: '温度气压传感器',
            drivers: [
              { type: 'bmp280', model: 'BMP280', display_name: 'BMP280 温度气压传感器', hardware_types: ['i2c', 'spi'], description: 'desc' },
            ],
          }],
        },
      ],
    })
    const res = await parserApi.getList()
    expect(res.length).toBeGreaterThan(0)
    expect(res[0].id).toBe('bmp280')
    expect(res[0].name).toBe('BMP280 温度气压传感器')
    expect(res[0].vendor).toBe('博世')
    expect(res[0].category).toBe('温度气压传感器')
    expect(res[0].hardware_types).toEqual(['i2c', 'spi'])
    expect(res[0].device_config_id).toBeUndefined()
  })

  it('getList keeps DB item and its device_config_id when the same type exists in the tree', async () => {
    const dbItem = {
      id: 42, type: 'bmp280', display_name: 'BMP280 定制模板', oem: 'Bosch', category: 'temp',
      hardware_types: ['i2c'], measure_type: ['temperature'], description: 'db',
    }
    mockClient.get.mockResolvedValueOnce({ data: { items: [dbItem], total: 1, page: 1, page_size: 20 } })
    mockClient.get.mockResolvedValueOnce({
      data: [{
        id: 'generic', name: '通用',
        children: [{ id: 'cat', name: 'Cat', drivers: [{ type: 'bmp280', display_name: 'BMP280', hardware_types: ['i2c', 'spi'] }] }],
      }],
    })
    const res = await parserApi.getList()
    expect(res).toHaveLength(1)
    expect(res[0].id).toBe('bmp280')
    expect(res[0].device_config_id).toBe(42)
    expect(res[0].name).toBe('BMP280 定制模板')
    expect(res[0].vendor).toBe('Bosch')
  })

  it('getList merges DB items with tree built-in drivers, dedup by type', async () => {
    const dbItem = {
      id: 7, type: 'sn3001_rain', display_name: 'SN-3001 光学雨量计', oem: '威盟士',
      hardware_types: ['uart'], measure_type: ['rain'], description: 'db',
    }
    mockClient.get.mockResolvedValueOnce({ data: { items: [dbItem], total: 1, page: 1, page_size: 20 } })
    mockClient.get.mockResolvedValueOnce({
      data: [
        {
          id: 'oem1', name: 'OEM1',
          children: [{ id: 'cat', name: 'Cat', drivers: [{ type: 'bmp280', display_name: 'BMP280', hardware_types: ['i2c'] }] }],
        },
        {
          id: 'oem2', name: 'OEM2',
          children: [{ id: 'cat2', name: 'Cat2', drivers: [{ type: 'sn3001_rain', display_name: 'SN-3001', hardware_types: ['uart'] }] }],
        },
      ],
    })
    const res = await parserApi.getList()
    expect(res).toHaveLength(2)
    const rain = res.find(p => p.id === 'sn3001_rain')
    expect(rain!.device_config_id).toBe(7) // DB 优先
    expect(rain!.name).toBe('SN-3001 光学雨量计')
    const bmp = res.find(p => p.id === 'bmp280')
    expect(bmp!.vendor).toBe('OEM1')
    expect(bmp!.category).toBe('Cat')
  })

  it('getList tolerates tree request failure and still returns DB items', async () => {
    mockClient.get.mockResolvedValueOnce({ data: { items: [{ id: 1, type: 'bmp280', display_name: 'BMP280', hardware_types: ['i2c'] }], total: 1 } })
    mockClient.get.mockRejectedValueOnce(new Error('tree unreachable'))
    const res = await parserApi.getList()
    expect(res).toHaveLength(1)
    expect(res[0].id).toBe('bmp280')
  })

  it('getList throws when both DB and tree requests fail', async () => {
    mockClient.get.mockRejectedValueOnce(new Error('db down'))
    mockClient.get.mockRejectedValueOnce(new Error('tree down'))
    await expect(parserApi.getList()).rejects.toThrow('db down')
  })

  it('normalize lowercases and array-izes hardware_types', async () => {
    mockClient.get.mockResolvedValueOnce({ data: { items: [] } })
    mockClient.get.mockResolvedValueOnce({
      data: [{
        id: 'oem', name: 'OEM',
        children: [{ id: 'cat', name: 'Cat', drivers: [{ type: 'd1', display_name: 'D1', hardware_types: ['I2C', 'SPI'] }] }],
      }],
    })
    const res = await parserApi.getList()
    expect(res).toHaveLength(1)
    expect(res[0].hardware_types).toEqual(['i2c', 'spi'])
  })

  it('tree leaves without parent vendor/category get generic empty values', async () => {
    mockClient.get.mockResolvedValueOnce({ data: { items: [] } })
    mockClient.get.mockResolvedValueOnce({
      data: [{ id: 'flat-oem', name: 'FlatOEM', drivers: [{ type: 'd1', display_name: 'D1', hardware_types: [] }] }],
    })
    const res = await parserApi.getList()
    expect(res).toHaveLength(1)
    expect(res[0].id).toBe('d1')
    expect(res[0].vendor).toBe('FlatOEM')
    expect(res[0].category).toBe('')
  })

  // 回归: 空库时 DB 与 tree 均空 → 返回空列表
  it('getList returns [] when both sources are empty', async () => {
    mockClient.get.mockResolvedValueOnce({ data: { items: [], total: 0 } })
    mockClient.get.mockResolvedValueOnce({ data: [] })
    const res = await parserApi.getList()
    expect(res).toEqual([])
  })
})
