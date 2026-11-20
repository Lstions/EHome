// src/api/periph.ts — GPIO + PWM 外设控制 API
import client, { type ApiEnvelope } from './client'
export interface CommandAck { request_id: number }

/** 外设命令必须带 request_id，WS 回执靠它关联；缺失时立刻失败，避免界面永久停在"正在写入"。 */
function requireAck<T extends { request_id: number }>(data: T | undefined, action: string): T {
  if (!data || typeof data.request_id !== 'number') throw new Error(`${action}响应缺少 request_id，设备回执无法关联`)
  return data
}

// === GPIO ===

export interface GPIOConfig {
  id?: number
  node_id: string
  pin: number
  direction: number   // 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
  initial_level: number
  label: string
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export const gpioApi = {
  async list(nodeId: string): Promise<GPIOConfig[]> {
    const resp = await client.get<unknown, ApiEnvelope<GPIOConfig[]>>(`/api/v1/nodes/${nodeId}/gpio`)
    if (resp.code && resp.code >= 400) throw new Error('获取 GPIO 配置失败')
    return resp.data ?? []
  },

  async create(nodeId: string, data: Partial<GPIOConfig>): Promise<GPIOConfig> {
    const resp = await client.post<unknown, ApiEnvelope<GPIOConfig>>(`/api/v1/nodes/${nodeId}/gpio`, data)
    return resp.data
  },

  async update(nodeId: string, pin: number, data: Partial<GPIOConfig>): Promise<void> {
    await client.put(`/api/v1/nodes/${nodeId}/gpio/${pin}`, data)
  },

  async delete(nodeId: string, pin: number): Promise<void> {
    await client.delete(`/api/v1/nodes/${nodeId}/gpio/${pin}`)
  },

  async set(nodeId: string, pin: number, level: 0 | 1): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/gpio/${pin}/set`, { level })
    return requireAck(resp?.data, 'GPIO 写入')
  },

  async toggle(nodeId: string, pin: number): Promise<void> {
    await client.post(`/api/v1/nodes/${nodeId}/gpio/${pin}/set`, { toggle: true })
  },

  async read(nodeId: string, pin: number): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/gpio/${pin}/read`)
    return requireAck(resp?.data, 'GPIO 读取')
  },
}

// === PWM ===

export interface PWMConfig {
  id?: number
  node_id: string
  hardware_id: string
  channel: number
  pin: number
  frequency: number    // Hz
  duty: number         // 0-10000 (0.00%-100.00%)
  resolution: number   // bits (4-20, default 14)
  auto_start: boolean
  label: string
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export const pwmApi = {
  async list(nodeId: string): Promise<PWMConfig[]> {
    const resp = await client.get<unknown, ApiEnvelope<PWMConfig[]>>(`/api/v1/nodes/${nodeId}/pwm`)
    if (resp.code && resp.code >= 400) throw new Error('获取 PWM 配置失败')
    return resp.data ?? []
  },

  async create(nodeId: string, data: Partial<PWMConfig>): Promise<PWMConfig> {
    const payload = { ...data }
    delete payload.channel
    const resp = await client.post<unknown, ApiEnvelope<PWMConfig>>(`/api/v1/nodes/${nodeId}/pwm`, payload)
    return resp.data
  },

  async update(nodeId: string, hardwareId: string, data: Partial<PWMConfig>): Promise<void> {
    await client.put(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}`, data)
  },

  async delete(nodeId: string, hardwareId: string): Promise<void> {
    await client.delete(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}`)
  },

  async start(nodeId: string, hardwareId: string): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/start`)
    return requireAck(resp?.data, 'PWM 启动')
  },

  async stop(nodeId: string, hardwareId: string): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/stop`)
    return requireAck(resp?.data, 'PWM 停止')
  },

  async setDuty(nodeId: string, hardwareId: string, duty: number): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/duty`, { duty })
    return requireAck(resp?.data, 'PWM 占空比')
  },

  async setFreq(nodeId: string, hardwareId: string, frequency: number): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/freq`, { frequency })
    return requireAck(resp?.data, 'PWM 频率')
  },

  async getState(nodeId: string, hardwareId: string): Promise<{ running: boolean; duty: number; frequency: number; request_id: number }> {
    const resp = await client.get<unknown, ApiEnvelope<{ running: boolean; duty: number; frequency: number; request_id: number }>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/state`)
    return requireAck(resp.data, 'PWM 状态')
  },
}

// === ADC ===

export interface ADCReadResult {
  hardware_id: string
  channel_id: number
  raw: number
  max_raw: number
  bit_width: number
  percent: number
  request_id: number
}

export const adcApi = {
  /** One synchronous raw sample; backend waits for the device PeriphRsp. */
  async read(nodeId: string, hardwareId: string): Promise<ADCReadResult> {
    const resp = await client.post<unknown, ApiEnvelope<ADCReadResult>>(
      `/api/v1/nodes/${nodeId}/adc/${encodeURIComponent(hardwareId)}/read`,
    )
    if (!resp?.data) throw new Error('ADC 读取响应为空')
    return resp.data
  },
}
