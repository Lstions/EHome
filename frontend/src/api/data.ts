import client from './client'

// Backend models (source of truth)
interface BackendCollector {
  id: number
  device_id: string
  model: string
  firmware_version: string
  status: string
  config_version: string
  config_status: string
  last_seen: string | null
  uptime_seconds: number
  created_at: string
  updated_at: string
}

interface BackendDevice {
  id: number
  name: string
  type: string
  parser_id: string
  channel_id: number
  status: string
  created_at: string
  updated_at: string
}

export interface Overview {
  collectors: {
    total: number
    online: number
    offline: number
  }
  devices: {
    total: number
    online: number
    offline: number
  }
  latest_data: Array<{
    device_id: number
    device_name: string
    collector_name: string
    data: Record<string, any>
    collected_at: string
    raw_data?: string
  }>
}

export const dataApi = {
  async getOverview(): Promise<Overview> {
    // Backend has no /overview endpoint; synthesize from /collectors + /devices
    const [collectorsRes, devicesRes] = await Promise.allSettled([
      client.get<unknown, BackendCollector[]>('/api/v1/collectors'),
      client.get<unknown, BackendDevice[]>('/api/v1/devices'),
    ])

    const collectors = collectorsRes.status === 'fulfilled'
      ? (Array.isArray(collectorsRes.value) ? collectorsRes.value : (collectorsRes.value as any).data || [])
      : []
    const devices = devicesRes.status === 'fulfilled'
      ? (Array.isArray(devicesRes.value) ? devicesRes.value : (devicesRes.value as any).data || [])
      : []

    const onlineCollectors = collectors.filter((c: BackendCollector) => c.status === 'online').length
    const onlineDevices = devices.filter((d: BackendDevice) => d.status === 'active').length

    return {
      collectors: {
        total: collectors.length,
        online: onlineCollectors,
        offline: collectors.length - onlineCollectors,
      },
      devices: {
        total: devices.length,
        online: onlineDevices,
        offline: devices.length - onlineDevices,
      },
      latest_data: devices.slice(0, 20).map((d: BackendDevice) => ({
        device_id: d.id,
        device_name: d.name,
        collector_name: '',
        data: {},
        collected_at: d.updated_at || d.created_at,
      })),
    }
  },

  async getCollectorDevicesData(collectorId: number, _params?: {
    start_time: string
    end_time: string
  }): Promise<any> {
    // Backend: GET /api/v1/collectors/:device_id/data?limit=100
    const response = await client.get<unknown, any>(`/api/v1/collectors/${collectorId}/data`, {
      params: { limit: 100 }
    })
    return (response as any).data || response
  }
}
