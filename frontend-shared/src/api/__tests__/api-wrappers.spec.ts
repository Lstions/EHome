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

  it('initialize posts the first-run administrator setup data', async () => {
    const setupData = { id: 1, username: 'admin' }
    const request = {
      credential: 'selector.secret',
      username: 'admin',
      password: 'strong-password-123',
      email: 'admin@example.test',
    }
    mockClient.post.mockResolvedValue({ data: setupData })
    const result = await authApi.initialize(request)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/auth/initialize', request)
    expect(result).toEqual(setupData)
  })

  it('logout posts to /api/v1/auth/logout', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await authApi.logout()
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/auth/logout', {})
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

// ─── channel ───
import { channelApi } from '../channel'

describe('channelApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('getList with nodeId passes params', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { items: [{ id: 1 }], total: 1 } })
    const res = await channelApi.getList(5)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/channels', { params: { node_id: 5 } })
    expect(res).toEqual({ items: [{ id: 1 }], total: 1 })
  })

  it('getList without params', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { items: [] } })
    const res = await channelApi.getList()
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/channels', { params: {} })
    expect(res).toEqual({ items: [] })
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

  it('list calls get with params', async () => {
    mockClient.get.mockResolvedValue({ data: { data: [], total: 0 } })
    await dataSourceApi.list({ page: 1, page_size: 10 })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/data-sources', { params: { page: 1, page_size: 10 } })
  })

  it('get calls get with id', async () => {
    mockClient.get.mockResolvedValue({ data: { data: { id: 1 } } })
    await dataSourceApi.get(1)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/data-sources/1')
  })

  it('create posts data', async () => {
    mockClient.post.mockResolvedValue({ data: { data: { id: 1 } } })
    await dataSourceApi.create({ device_id: 1, category: 'test', name: 'ds1' })
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/data-sources', { device_id: 1, category: 'test', name: 'ds1' })
  })

  it('update puts data', async () => {
    mockClient.put.mockResolvedValue({ data: {} })
    await dataSourceApi.update(1, { name: 'updated' })
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/data-sources/1', { name: 'updated' })
  })

  it('delete calls delete', async () => {
    mockClient.delete.mockResolvedValue(undefined)
    await dataSourceApi.delete(1)
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/data-sources/1')
  })

  it('activate posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await dataSourceApi.activate(1)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/data-sources/1/activate')
  })

  it('deactivate posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await dataSourceApi.deactivate(1)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/data-sources/1/deactivate')
  })

  it('reset posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await dataSourceApi.reset(1)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/data-sources/1/reset')
  })

  it('getHealth passes limit param', async () => {
    mockClient.get.mockResolvedValue({ data: { data: [] } })
    await dataSourceApi.getHealth(1, 10)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/data-sources/1/health', { params: { limit: 10 } })
  })

  it('getFailoverLogs passes deviceId and limit', async () => {
    mockClient.get.mockResolvedValue({ data: { data: [] } })
    await dataSourceApi.getFailoverLogs(5, 20)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/devices/5/failover-logs', { params: { limit: 20 } })
  })
})

// ─── deviceConfig ───
import { deviceConfigApi } from '../deviceConfig'

describe('deviceConfigApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('getList returns response.data', async () => {
    const listRes = { list: [{ id: 1 }], total: 1, page: 1, page_size: 20 }
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
    mockClient.get.mockResolvedValue({ code: 200, data: { list: [{ id: 1 }], total: 1, page: 1, page_size: 100 }, message: 'ok' })
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

  it('getDriverList returns list from envelope.data.list', async () => {
    const list = [{ type: 'bmp280', model: 'BMP280' }]
    mockClient.get.mockResolvedValue({ data: { list } })
    const res = await getDriverList()
    expect(res).toEqual(list)
  })

  it('getDriverList returns envelope.data if no .list', async () => {
    const list = [{ type: 'bmp280' }]
    mockClient.get.mockResolvedValue({ data: list })
    const res = await getDriverList()
    expect(res).toEqual(list)
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

  it('getList with bare array response', async () => {
    mockClient.get.mockResolvedValue([{ id: 1, name: 'dev1', status: 'online' }])
    const res = await edgeDeviceApi.getList()
    expect(res.total).toBe(1)
    expect(res.items[0].status).toBe('online')
  })

  it('getList drops malformed entries before normalization', async () => {
    mockClient.get.mockResolvedValue([
      undefined,
      null,
      { name: 'missing-id' },
      { id: 2, name: 'valid-device', status: 'active' },
    ])

    const res = await edgeDeviceApi.getList()

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

  it('getList with envelope .data as array', async () => {
    mockClient.get.mockResolvedValue({ data: [{ id: 1, status: 'active' }] })
    const res = await edgeDeviceApi.getList()
    expect(res.total).toBe(1)
    expect(res.items[0].status).toBe('active')
  })

  it('getList with unexpected format returns empty', async () => {
    mockClient.get.mockResolvedValue({ data: null })
    const res = await edgeDeviceApi.getList()
    expect(res).toEqual({ total: 0, items: [] })
  })

  it('getList passes params', async () => {
    mockClient.get.mockResolvedValue([])
    await edgeDeviceApi.getList({ node_id: 5, status: 'online' })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/edge-devices', { params: { node_id: 5, status: 'online' } })
  })

  it('create forwards device_config_id required by the backend', async () => {
    mockClient.post.mockResolvedValue({ id: 9 })

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

  it('getDetail with envelope data', async () => {
    mockClient.get.mockResolvedValue({ data: { id: 1, name: 'dev1', status: 'online' } })
    const res = await edgeDeviceApi.getDetail(1)
    expect(res.id).toBe(1)
    expect(res.status).toBe('online')
  })

  it('getDetail with bare object', async () => {
    mockClient.get.mockResolvedValue({ id: 1, status: 'warning' })
    const res = await edgeDeviceApi.getDetail(1)
    expect(res.status).toBe('warning')
  })

  it('create with response.id', async () => {
    mockClient.post.mockResolvedValue({ id: 1 })
    const res = await edgeDeviceApi.create({ name: 'new' })
    expect(res).toEqual({ id: 1 })
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

  it('executeOperation posts', async () => {
    const execRes = { code: 200, data: { status: 'ok', operation: 'read' }, message: '' }
    mockClient.post.mockResolvedValue({ data: execRes.data })
    const res = await edgeDeviceApi.executeOperation(1, 'read', { register: 0 })
    expect(res).toEqual(execRes.data)
  })

  it('getOperationHistory returns array', async () => {
    mockClient.get.mockResolvedValue([{ id: 1 }, { id: 2 }])
    const res = await edgeDeviceApi.getOperationHistory(1, 10)
    expect(res).toHaveLength(2)
  })

  it('getOperationHistory with envelope', async () => {
    mockClient.get.mockResolvedValue({ data: [{ id: 1 }] })
    const res = await edgeDeviceApi.getOperationHistory(1)
    expect(res).toHaveLength(1)
  })

  it('getOperationHistory with unexpected format', async () => {
    mockClient.get.mockResolvedValue(null)
    const res = await edgeDeviceApi.getOperationHistory(1)
    expect(res).toEqual([])
  })

  it('changeAddress posts', async () => {
    mockClient.post.mockResolvedValue({ data: { success: true } })
    await edgeDeviceApi.changeAddress(1, 10)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/edge-devices/1/change-address', { new_address: 10 })
  })

  it('normalize maps unknown status', async () => {
    mockClient.get.mockResolvedValue([{ id: 1, status: 'weird_status' }])
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].status).toBe('unknown')
  })

  it('normalize maps empty status to offline', async () => {
    mockClient.get.mockResolvedValue([{ id: 1 }])
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].status).toBe('offline')
  })

  it('normalize falls back device_type from type', async () => {
    mockClient.get.mockResolvedValue([{ id: 1, type: 'sensor' }])
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].device_type).toBe('sensor')
  })

  it('normalize uses channel.hardware_type fallback', async () => {
    mockClient.get.mockResolvedValue([{ id: 1, channel: { hardware_type: 'i2c', hardware_id: 'I2C0' } }])
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].hardware_type).toBe('i2c')
    expect(res.items[0].hardware_id).toBe('I2C0')
  })

  it('normalize uses node info', async () => {
    mockClient.get.mockResolvedValue([{ id: 1, node: { id: 5, name: 'node5' } }])
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].node).toEqual({ id: 5, name: 'node5' })
  })

  it('normalize omits node when no name', async () => {
    mockClient.get.mockResolvedValue([{ id: 1, node: { id: 5 } }])
    const res = await edgeDeviceApi.getList()
    expect(res.items[0].node).toBeUndefined()
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

  it('getList with bare array wraps it', async () => {
    mockClient.get.mockResolvedValue([{ id: 1, node_id: 'n1' }])
    const res = await nodeApi.getList()
    expect(res.items).toHaveLength(1)
    expect(res.total).toBe(1)
  })

  it('getList with envelope .data.items', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [{ id: 1 }], total: 1, page: 1, page_size: 20 } })
    const res = await nodeApi.getList()
    expect(res.total).toBe(1)
  })

  it('getList with envelope .data as array', async () => {
    mockClient.get.mockResolvedValue({ data: [{ id: 1 }] })
    const res = await nodeApi.getList({ page: 2, page_size: 10 })
    expect(res.items).toHaveLength(1)
    expect(res.page).toBe(2)
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

  it('getDetail with bare object', async () => {
    mockClient.get.mockResolvedValue({ id: 1, node_id: 'n1' })
    const res = await nodeApi.getDetail(1)
    expect(res.id).toBe(1)
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
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/ota/start', { node_id: 1, firmware_id: 5, force: true })
  })

  it('startOTA default force=false', async () => {
    mockClient.post.mockResolvedValue({ code: 200, data: { ota_record_id: 2, status: 'started' }, message: 'ok' })
    await nodeApi.startOTA(1, 5)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/ota/start', { node_id: 1, firmware_id: 5, force: false })
  })

  it('getOTAProgress returns data', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: { id: 1, progress: 50 }, message: 'ok' })
    const res = await nodeApi.getOTAProgress(1, 1)
    expect(res.progress).toBe(50)
  })

  it('getOTAHistory returns array', async () => {
    mockClient.get.mockResolvedValue({ code: 200, data: [{ id: 1 }], message: 'ok' })
    const res = await nodeApi.getOTAHistory(1)
    expect(res).toHaveLength(1)
  })

  it('cancelOTA posts', async () => {
    mockClient.post.mockResolvedValue(undefined)
    await nodeApi.cancelOTA(1, 1)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/ota/cancel/1')
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

  it('getList normalizes drivers', async () => {
    mockClient.get.mockResolvedValue({
      data: {
        list: [
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

  it('getList with envelope.data directly (no .list)', async () => {
    mockClient.get.mockResolvedValue({
      data: [{ type: 'bme680', display_name: 'BME680', vendor: 'Bosch' }]
    })
    const res = await parserApi.getList()
    expect(res).toHaveLength(1)
  })

  it('getList resolves hardware_types via bus_types fallback', async () => {
    mockClient.get.mockResolvedValue({
      data: { list: [{ type: 'bmp280', display_name: 'BMP280', bus_types: ['spi'] }] }
    })
    const res = await parserApi.getList()
    expect(res[0].hardware_types).toEqual(['spi'])
  })

  it('getList resolves hardware_types via hardware_type fallback', async () => {
    mockClient.get.mockResolvedValue({
      data: { list: [{ type: 'bmp280', display_name: 'BMP280', hardware_type: 'I2C' }] }
    })
    const res = await parserApi.getList()
    expect(res[0].hardware_types).toEqual(['i2c'])
  })

  it('getList resolves hardware_types via connection.bus_type fallback', async () => {
    mockClient.get.mockResolvedValue({
      data: { list: [{ type: 'bmp280', display_name: 'BMP280', connection: { bus_type: 'SPI' } }] }
    })
    const res = await parserApi.getList()
    expect(res[0].hardware_types).toEqual(['spi'])
  })

  it('getList resolves hardware_types via protocol fallback', async () => {
    mockClient.get.mockResolvedValue({
      data: { list: [{ type: 'bmp280', display_name: 'BMP280', protocol: 'MODBUS' }] }
    })
    const res = await parserApi.getList()
    expect(res[0].hardware_types).toEqual(['modbus'])
  })

  it('getList resolves measure_types from measure_type string', async () => {
    mockClient.get.mockResolvedValue({
      data: { list: [{ type: 'bmp280', display_name: 'BMP280', measure_type: 'temperature' }] }
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
})

// ─── unifiedData ───
import { unifiedDataApi } from '../unifiedData'

describe('unifiedDataApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('query calls get with params', async () => {
    mockClient.get.mockResolvedValue({ data: { data: [], total: 0 } })
    await unifiedDataApi.query({ device_pk: 1, category: 'temp' })
    expect(mockClient.get).toHaveBeenCalledWith('/unified-data', { params: { device_pk: 1, category: 'temp' } })
  })

  it('getLatest with category', async () => {
    mockClient.get.mockResolvedValue({ data: { data: {} } })
    await unifiedDataApi.getLatest(1, 'temp')
    expect(mockClient.get).toHaveBeenCalledWith('/unified-data/latest', { params: { device_pk: 1, category: 'temp' } })
  })

  it('getLatest without category', async () => {
    mockClient.get.mockResolvedValue({ data: { data: {} } })
    await unifiedDataApi.getLatest(1)
    expect(mockClient.get).toHaveBeenCalledWith('/unified-data/latest', { params: { device_pk: 1 } })
  })

  it('getHistorical passes params', async () => {
    mockClient.get.mockResolvedValue({ data: { data: [] } })
    await unifiedDataApi.getHistorical({ device_pk: 1, start_time: '2024-01-01', end_time: '2024-12-31' })
    expect(mockClient.get).toHaveBeenCalledWith('/unified-data/historical', {
      params: { device_pk: 1, start_time: '2024-01-01', end_time: '2024-12-31' },
    })
  })

  it('getAggregated passes params', async () => {
    mockClient.get.mockResolvedValue({ data: { data: [] } })
    await unifiedDataApi.getAggregated({ device_pk: 1, category: 'temp', interval: 'hour', start_time: '2024-01-01', end_time: '2024-12-31' })
    expect(mockClient.get).toHaveBeenCalledWith('/unified-data/aggregated', {
      params: { device_pk: 1, category: 'temp', interval: 'hour', start_time: '2024-01-01', end_time: '2024-12-31' },
    })
  })

  it('getCategories calls get', async () => {
    mockClient.get.mockResolvedValue({ data: { data: [] } })
    await unifiedDataApi.getCategories()
    expect(mockClient.get).toHaveBeenCalledWith('/unified-data/categories')
  })
})
