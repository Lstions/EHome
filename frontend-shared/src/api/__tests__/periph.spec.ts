import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockClient = vi.hoisted(() => ({
  get: vi.fn(),
  delete: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
}))

vi.mock('../client', () => ({ default: mockClient }))

import { gpioApi, pwmApi, type GPIOConfig, type PWMConfig } from '../periph'

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

describe('gpioApi', () => {
  beforeEach(() => vi.clearAllMocks())

  // ---- list ----

  it('list unwraps an API envelope and returns the data array', async () => {
    mockClient.get.mockResolvedValue({ code: 200, message: 'ok', data: [gpio] })

    const result = await gpioApi.list(NODE)

    expect(mockClient.get).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio`)
    expect(result).toEqual([gpio])
  })

  it('list accepts a bare array response', async () => {
    mockClient.get.mockResolvedValue([gpio])

    const result = await gpioApi.list(NODE)

    expect(result).toEqual([gpio])
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

  it('set posts the level to the set endpoint', async () => {
    mockClient.post.mockResolvedValue(undefined)

    await gpioApi.set(NODE, 2, 1)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio/2/set`, { level: 1 })
  })

  // ---- toggle ----

  it('toggle posts { toggle: true } to the set endpoint', async () => {
    mockClient.post.mockResolvedValue(undefined)

    await gpioApi.toggle(NODE, 2)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio/2/set`, { toggle: true })
  })

  // ---- read ----

  it('read posts to the read endpoint and unwraps the level', async () => {
    mockClient.post.mockResolvedValue({ code: 200, message: 'ok', data: { level: 1 } })

    const result = await gpioApi.read(NODE, 2)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/gpio/2/read`)
    expect(result).toEqual({ level: 1 })
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

  it('list accepts a bare array response', async () => {
    mockClient.get.mockResolvedValue([pwm])

    const result = await pwmApi.list(NODE)

    expect(result).toEqual([pwm])
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

  it('start posts to the start endpoint', async () => {
    mockClient.post.mockResolvedValue(undefined)

    await pwmApi.start(NODE, 'PWM0')

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/start`)
  })

  // ---- stop ----

  it('stop posts to the stop endpoint', async () => {
    mockClient.post.mockResolvedValue(undefined)

    await pwmApi.stop(NODE, 'PWM0')

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/stop`)
  })

  // ---- setDuty ----

  it('setDuty posts the duty value', async () => {
    mockClient.post.mockResolvedValue(undefined)

    await pwmApi.setDuty(NODE, 'PWM0', 7500)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/duty`, { duty: 7500 })
  })

  // ---- setFreq ----

  it('setFreq posts the frequency value', async () => {
    mockClient.post.mockResolvedValue(undefined)

    await pwmApi.setFreq(NODE, 'PWM0', 2000)

    expect(mockClient.post).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/freq`, { frequency: 2000 })
  })

  // ---- getState ----

  it('getState sends a GET and unwraps the state object', async () => {
    const state = { running: true, duty: 5000, frequency: 1000 }
    mockClient.get.mockResolvedValue({ code: 200, message: 'ok', data: state })

    const result = await pwmApi.getState(NODE, 'PWM0')

    expect(mockClient.get).toHaveBeenCalledWith(`/api/v1/nodes/${NODE}/pwm/PWM0/state`)
    expect(result).toEqual(state)
  })
})
