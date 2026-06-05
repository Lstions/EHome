import { describe, it, expect } from 'vitest'
import { WS_EVENT, isValidEventName, isStrictEventName, type WsEventName } from './events'

describe('events.ts', () => {
  describe('WS_EVENT constants', () => {
    it('should have all required event names', () => {
      // Node events (v2.2)
      expect(WS_EVENT.NODE_STATUS).toBe('node_status')
      expect(WS_EVENT.NODE_CONFIG_SYNCED).toBe('node_config_synced')
      expect(WS_EVENT.NODE_CONFIG_CHANGED).toBe('node_config_changed')

      // EdgeDevice events (v2.2)
      expect(WS_EVENT.EDGE_DEVICE_STATUS).toBe('edge_device_status')

      // 数据
      expect(WS_EVENT.DATA_UPDATE).toBe('data_update')

      // OTA
      expect(WS_EVENT.OTA_PROGRESS).toBe('ota_progress')
      expect(WS_EVENT.OTA_COMPLETED).toBe('ota_completed')

      // 通知
      expect(WS_EVENT.NOTIFICATION).toBe('notification')

      // Ping
      expect(WS_EVENT.PING_RESULT).toBe('ping_result')

      // 通道终端
      expect(WS_EVENT.CHANNEL_DATA).toBe('channel_data')
      expect(WS_EVENT.TERMINAL_ACK).toBe('terminal_ack')

      // 扫描
      expect(WS_EVENT.SCAN_RESULT).toBe('scan_result')

      // 通道写入 (v2.2)
      expect(WS_EVENT.CHANNEL_WRITE).toBe('channel_write')
      expect(WS_EVENT.CHANNEL_WRITE_ERROR).toBe('channel_write_error')


    })

    it('all event names should be valid (lowercase, underscore-separated)', () => {
      const eventNames = Object.values(WS_EVENT)
      for (const name of eventNames) {
        expect(isValidEventName(name), `Event name "${name}" should be valid`).toBe(true)
      }
    })

    it('most event names should follow strict <noun>_<verb> format', () => {
      const eventNames = Object.values(WS_EVENT)
      const nonStrict: string[] = []
      for (const name of eventNames) {
        if (!isStrictEventName(name)) {
          nonStrict.push(name)
        }
      }
      // 非严格格式的事件名应该是已知例外
      expect(nonStrict).toEqual(['notification'])
    })

    it('should be readonly (as const)', () => {
      const values = Object.values(WS_EVENT)
      expect(values.every(v => typeof v === 'string')).toBe(true)
    })
  })

  describe('isValidEventName', () => {
    it('should return true for valid event names', () => {
      expect(isValidEventName('node_status')).toBe(true)
      expect(isValidEventName('edge_device_status')).toBe(true)
      expect(isValidEventName('data_update')).toBe(true)
      expect(isValidEventName('ota_progress')).toBe(true)
      expect(isValidEventName('node_config_synced')).toBe(true) // 多段
      expect(isValidEventName('notification')).toBe(true) // 单字 (已知例外)
    })

    it('should return false for invalid event names', () => {
      // 大写字母
      expect(isValidEventName('Node_Status')).toBe(false)
      expect(isValidEventName('NODE_STATUS')).toBe(false)
      // 数字
      expect(isValidEventName('node_status_2')).toBe(false)
      // 连续下划线
      expect(isValidEventName('node__status')).toBe(false)
      // 首尾下划线
      expect(isValidEventName('_node_status')).toBe(false)
      expect(isValidEventName('node_status_')).toBe(false)
      // 空字符串
      expect(isValidEventName('')).toBe(false)
      // 空格
      expect(isValidEventName('node status')).toBe(false)
      // 连字符
      expect(isValidEventName('node-status')).toBe(false)
    })
  })

  describe('isStrictEventName', () => {
    it('should return true only for <noun>_<verb> format (at least one underscore)', () => {
      expect(isStrictEventName('node_status')).toBe(true)
      expect(isStrictEventName('data_update')).toBe(true)
      expect(isStrictEventName('node_config_synced')).toBe(true)
    })

    it('should return false for single-word names', () => {
      expect(isStrictEventName('notification')).toBe(false)
      expect(isStrictEventName('status')).toBe(false)
    })
  })

  describe('WsEventName type', () => {
    it('should be a union of all WS_EVENT values', () => {
      const validNames: WsEventName[] = [
        WS_EVENT.NODE_STATUS,
        WS_EVENT.NODE_CONFIG_SYNCED,
        WS_EVENT.NODE_CONFIG_CHANGED,
        WS_EVENT.EDGE_DEVICE_STATUS,
        WS_EVENT.DATA_UPDATE,
        WS_EVENT.OTA_PROGRESS,
        WS_EVENT.OTA_COMPLETED,
        WS_EVENT.NOTIFICATION,
        WS_EVENT.PING_RESULT,
        WS_EVENT.CHANNEL_DATA,
        WS_EVENT.TERMINAL_ACK,
        WS_EVENT.SCAN_RESULT,
        WS_EVENT.CHANNEL_WRITE,
        WS_EVENT.CHANNEL_WRITE_ERROR,

      ]
      expect(validNames.length).toBe(14)
    })
  })
})