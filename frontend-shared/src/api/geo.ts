import client from './client'

/**
 * 坐标来源 API（2026-11-14）。
 *
 * 用户裁决：日出日落规则的纬度/经度/时区只有两个来源 ——
 *   1. `/system/geo/ip`：服务端按外网出口 IP 定位（含 IANA 时区）；
 *   2. `/system/geo/cities`：手动选择"设备城市"（内置城市目录，每城自带时区）。
 *
 * 时区**随位置一起返回**，界面上没有时区输入框。这两个接口在 v1 鉴权组内，
 * 走与业务接口相同的 client（401 时由既有拦截器处理）。
 */

export interface GeoIPLocation {
  latitude: number
  longitude: number
  /** IANA 时区（由 IP 服务返回，前端不解释） */
  timezone: string
  city?: string
  region?: string
  country?: string
  /** 恒为 ip，便于直接把结果写进 location_source */
  source: 'ip'
}

export interface GeoCity {
  name: string
  pinyin: string
  country: string
  country_code: string
  latitude: number
  longitude: number
  timezone: string
}

type Envelope<T> = { code: number; data: T; message?: string }

/** 拦截器可能已解包 envelope，也可能原样返回；与 api/automation.ts 同口径。 */
async function unwrap<T>(p: Promise<unknown>): Promise<T> {
  const res = (await p) as unknown as Envelope<T> | T
  if (res && typeof res === 'object' && 'data' in (res as Envelope<T>) && 'code' in (res as Envelope<T>)) {
    return (res as Envelope<T>).data
  }
  return res as T
}

export const geoApi = {
  /** 解析部署机外网出口位置；失败时抛出（调用方提示改用手动城市）。 */
  async resolveIP(): Promise<GeoIPLocation> {
    return unwrap<GeoIPLocation>(client.get('/api/v1/system/geo/ip'))
  },

  /** 按中文名/拼音搜索城市；空串返回人口最多的前 N 个。 */
  async searchCities(query: string, limit = 20): Promise<GeoCity[]> {
    const data = await unwrap<{ items?: GeoCity[] }>(
      client.get('/api/v1/system/geo/cities', { params: { q: query, limit } }),
    )
    return Array.isArray(data?.items) ? data.items : []
  },
}
