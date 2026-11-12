/**
 * 边缘设备**指令域**（自 api/edgeDevice.ts 按域搬出，方法体一字未改）。
 *
 * 边界依据：驱动的指令模板（/drivers/:type/commands）与「本设备逐指令轮询间隔」
 * （/edge-devices/:id/commands，PUT 超过后端 max_polling_commands 会整单 400）是同一
 * 条数据链路的两端：增删/启用指令要先读模板，保存前要读现值。两者共用类型
 * CommandTemplate(WithInterval)，故同域。
 */
import client, { type ApiEnvelope } from '../client'
import type { CommandTemplate, CommandTemplateWithInterval } from './types'

export const commandsApi = {
  // Driver command templates
  async getDriverCommands(deviceType: string): Promise<CommandTemplate[]> {
    const response = await client.get<unknown, ApiEnvelope<CommandTemplate[]>>(`/api/v1/drivers/${deviceType}/commands`)
    const data = response?.data
    return Array.isArray(data) ? data : []
  },

  // Edge device command intervals
  async getCommandIntervals(edgeDeviceId: number): Promise<CommandTemplateWithInterval[]> {
    const response = await client.get<unknown, ApiEnvelope<CommandTemplateWithInterval[]>>(`/api/v1/edge-devices/${edgeDeviceId}/commands`)
    const data = response?.data
    return Array.isArray(data) ? data : []
  },

  async updateCommandIntervals(edgeDeviceId: number, intervals: Record<string, number>): Promise<void> {
    await client.put(`/api/v1/edge-devices/${edgeDeviceId}/commands`, { intervals })
  },
}
