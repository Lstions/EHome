import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockClient = vi.hoisted(() => ({
  get: vi.fn(),
  delete: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
}))

vi.mock('../client', () => ({ default: mockClient }))

import { adcApi, gpioApi, pwmApi, type GPIOConfig, type PWMConfig } from '../periph'

const NODE = 'ESP32-C6-01'

const gpio: GPIOConfig = {
  id: 1,
  node_id: NODE,
  pin: 2,
  direction: 1,
  initial_level: 0,
  label: 'LED',
  enabled: true,
  created_at: '2026-07-14T08:00:00Z',
  updated_at: '2026-07-14T08:00:00Z',
}

const pwm: PWMConfig = {
  id: 2,
  node_id: NODE,
  hardware_id: 'PWM0',
  channel: 0,
  pin: 3,
  frequency: 1000,
  duty: 5000,
  resolution: 14,
  auto_start: false,
  label: 'Fan',
  enabled: true,
  created_at: '2026-07-14T08:00:00Z',
  updated_at: '2026-07-14T08:00:00Z',
}

describe('adcApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('read returns the synchronous raw sample envelope', async () => {
    const sample = { hardware_id: 'ADC1_CH1', channel_id: 19, raw: 2048, max_raw: 4095, bit_width: 12, percent: 50, request_id: 7 }
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: sample })

    await expect(adcApi.read(NODE, 'ADC1_CH1')).resolves.toEqual(sample)
    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/adc/ADC1_CH1/read`)
  })

  it('read rejects an empty payload instead of returning undefined', async () => {
    mockClient.post.mockResolvedValue({ code: 504, message: 'timeout' })
    await expect(adcApi.read(NODE, 'ADC1_CH1')).rejects.toThrow('ADC 读取响应为空')
  })
})

describe('gpioApi', () => {
  beforeEach(() => vi.clearAllMocks())

  // ---- list ----

  it('list unwraps an API envelope and returns the data array', async () => {
    mockClient.get.mockResolvedValue({ code: 200, message: 'ok', data: [gpio] })

    const result = await gpioApi.list(NODE)

    expect(mockClient.get).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio`)
    expect(result).toEqual([gpio])
  })

  it('list ignores a bare array response and requires the envelope', async () => {
    mockClient.get.mockResolvedValue([gpio])

    const result = await gpioApi.list(NODE)

    expect(result).toEqual([])
  })

  it('set reads request_id from the unified envelope data', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: { request_id: 42 } })

    const result = await gpioApi.set(NODE, 2, 1)

    expect(result).toEqual({ request_id: 42 })
  })

  it('list throws when the envelope reports code >= 400', async () => {
    mockClient.get.mockResolvedValue({ code: 500, message: 'boom', data: null })

    await expect(gpioApi.list(NODE)).rejects.toThrow('获取 GPIO 配置失败')
  })

  // ---- create ----

  it('create posts the config and unwraps the returned data', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: gpio })

    const result = await gpioApi.create(NODE, { pin: 2, direction: 1, initial_level: 0, label: 'LED', enabled: true })

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio`, {
      pin: 2,
      direction: 1,
      initial_level: 0,
      label: 'LED',
      enabled: true,
    })
    expect(result).toEqual(gpio)
  })

  // ---- update ----

  it('update sends a PUT with the config body', async () => {
    mockClient.put.mockResolvedValue(undefined)

    await gpioApi.update(NODE, 2, { label: 'LED2' })

    expect(mockClient.put).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio/2`, { label: 'LED2' })
  })

  // ---- delete ----

  it('delete sends a DELETE for the specific pin', async () => {
    mockClient.delete.mockResolvedValue(undefined)

    await gpioApi.delete(NODE, 2)

    expect(mockClient.delete).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio/2`)
  })

  // ---- set ----

  it('set posts the level and returns the request_id used by the WS ACK', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: { request_id: 42 } })

    const result = await gpioApi.set(NODE, 2, 1)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio/2/set`, { level: 1 })
    expect(result).toEqual({ request_id: 42 })
  })

  it('set rejects a response without request_id instead of silently using 0', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: {} })

    await expect(gpioApi.set(NODE, 2, 1)).rejects.toThrow('缺少 request_id')
  })

  // ---- toggle ----

  it('toggle posts { toggle: true } to the set endpoint', async () => {
    mockClient.post.mockResolvedValue(undefined)

    await gpioApi.toggle(NODE, 2)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio/2/set`, { toggle: true })
  })

  // ---- read ----

  it('read posts to the read endpoint and returns the request_id used by the WS ACK', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: { request_id: 43 } })

    const result = await gpioApi.read(NODE, 2)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio/2/read`)
    expect(result).toEqual({ request_id: 43 })
  })
})

describe('pwmApi', () => {
  beforeEach(() => vi.clearAllMocks())

  // ---- list ----

  it('list unwraps an API envelope and returns the data array', async () => {
    mockClient.get.mockResolvedValue({ code: 200, message: 'ok', data: [pwm] })

    const result = await pwmApi.list(NODE)

    expect(mockClient.get).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm`)
    expect(result).toEqual([pwm])
  })

  it('list ignores a bare array response and requires the envelope', async () => {
    mockClient.get.mockResolvedValue([pwm])

    const result = await pwmApi.list(NODE)

    expect(result).toEqual([])
  })

  it('start reads request_id from the unified envelope data', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: { request_id: 7 } })

    const result = await pwmApi.start(NODE, 'PWM0')

    expect(result).toEqual({ request_id: 7 })
  })

  it('list throws when the envelope reports code >= 400', async () => {
    mockClient.get.mockResolvedValue({ code: 404, message: 'not found', data: null })

    await expect(pwmApi.list(NODE)).rejects.toThrow('获取 PWM 配置失败')
  })

  // ---- create ----

  it('create posts the config and unwraps the returned data', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: pwm })

    const result = await pwmApi.create(NODE, { hardware_id: 'PWM0', pin: 3, frequency: 1000, duty: 5000, resolution: 14, auto_start: false, label: 'Fan', enabled: true })

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm`, {
      hardware_id: 'PWM0',
      pin: 3,
      frequency: 1000,
      duty: 5000,
      resolution: 14,
      auto_start: false,
      label: 'Fan',
      enabled: true,
    })
    expect(result).toEqual(pwm)
  })

  it('create strips a client-supplied channel so the backend remains authoritative', async () => {
    mockClient.post.mockResolvedValue({ data: pwm })
    await pwmApi.create(NODE, { hardware_id: 'PWM0', channel: 9, pin: 3 } as Partial<PWMConfig>)
    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm`, {
      hardware_id: 'PWM0',
      pin: 3,
    })
  })

  // ---- update ----

  it('update sends a PUT with the config body', async () => {
    mockClient.put.mockResolvedValue(undefined)

    await pwmApi.update(NODE, 'PWM0', { duty: 8000 })

    expect(mockClient.put).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0`, { duty: 8000 })
  })

  // ---- delete ----

  it('delete sends a DELETE for the hardware resource identity', async () => {
    mockClient.delete.mockResolvedValue(undefined)

    await pwmApi.delete(NODE, 'PWM0')

    expect(mockClient.delete).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0`)
  })

  // ---- start ----

  it('start posts to the start endpoint and returns request_id', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: { request_id: 7 } })

    const result = await pwmApi.start(NODE, 'PWM0')

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/start`)
    expect(result).toEqual({ request_id: 7 })
  })

  // ---- stop ----

  it('stop posts to the stop endpoint and returns request_id', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: { request_id: 8 } })

    const result = await pwmApi.stop(NODE, 'PWM0')

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/stop`)
    expect(result).toEqual({ request_id: 8 })
  })

  // ---- setDuty ----

  it('setDuty posts the duty value and returns request_id', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: { request_id: 9 } })

    const result = await pwmApi.setDuty(NODE, 'PWM0', 7500)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/duty`, { duty: 7500 })
    expect(result).toEqual({ request_id: 9 })
  })

  // ---- setFreq ----

  it('setFreq posts the frequency value and returns request_id', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: { request_id: 10 } })

    const result = await pwmApi.setFreq(NODE, 'PWM0', 2000)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/freq`, { frequency: 2000 })
    expect(result).toEqual({ request_id: 10 })
  })

  // ---- getState ----

  it('getState sends a GET and unwraps the state object', async () => {
    const state = { running: true, duty: 5000, frequency: 1000, request_id: 11 }
    mockClient.get.mockResolvedValue({ code: 200, message: 'ok', data: state })

    const result = await pwmApi.getState(NODE, 'PWM0')

    expect(mockClient.get).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/state`)
    expect(result).toEqual(state)
  })
})
