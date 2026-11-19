import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import {
  LIVE_ENTRY_TTL_MS,
  LIVE_RENDER_INTERVAL_MS,
  useEdgeDeviceLiveStore,
} from '@/stores/edgeDeviceLive'
import { WS_EVENT } from '@/events/events'
import type { WebSocketMessage } from '@/stores/websocket'

/**
 * 实时层是「1Hz 渲染契约」的唯一执行点 —— 这些用例钉的就是那条契约本身：
 * 消息速率随便多高，响应式状态每秒最多换一次；后台标签页一次都不换。
 */

const NOW = new Date('2026-11-19T10:00:00.000Z')

function frame(deviceId: number, data: Record<string, number>, collectedAt = NOW.toISOString()): WebSocketMessage {
  return {
    type: WS_EVENT.DATA_UPDATE,
    payload: { edge_device_id: deviceId, data, collected_at: collectedAt },
  }
}

/** 故意构造的畸形载荷（类型层面也不合法，用一次收窄换用例可读性）。 */
function raw(payload: Record<string, unknown>): WebSocketMessage {
  return { type: WS_EVENT.DATA_UPDATE, payload } as unknown as WebSocketMessage
}

function statusEvent(deviceId: number, status: string): WebSocketMessage {
  return {
    type: WS_EVENT.EDGE_DEVICE_STATUS,
    payload: { edge_device_id: deviceId, device_id: deviceId, status },
  }
}

describe('edgeDeviceLive store（1Hz 实时层）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    vi.setSystemTime(NOW)
  })

  afterEach(() => {
    // 显式停表：否则下一条用例的 fake timer 列表里还留着上一条的 interval。
    try {
      useEdgeDeviceLiveStore().stop()
    } catch {
      /* store 未被实例化的用例无需清理 */
    }
    vi.useRealTimers()
  })

  it('把 1 秒内的多帧合并成一次状态变更（消息速率 ≠ 渲染速率）', async () => {
    const store = useEdgeDeviceLiveStore()
    store.start()

    // 同一帧周期内：3 条 data_update + 1 条状态事件 —— 都不应立刻写响应式状态。
    store.ingest(WS_EVENT.DATA_UPDATE, frame(7, { temperature: 21.5 }))
    store.ingest(WS_EVENT.DATA_UPDATE, frame(7, { humidity: 55 }))
    store.ingest(WS_EVENT.DATA_UPDATE, frame(7, { temperature: 22.5 }))
    store.ingest(WS_EVENT.EDGE_DEVICE_STATUS, statusEvent(7, 'offline'))
    expect(store.live).toEqual({})

    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)
    await nextTick()

    expect(store.live[7]).toMatchObject({
      status: 'offline',
      // 后到的值覆盖先到的；不同键累积（与后端「每帧只带本帧物理量」一致）
      lastData: { temperature: 22.5, humidity: 55 },
      lastDataAt: NOW.toISOString(),
    })
  })

  it('data_update 本身就视为在线（不依赖 edge_device_status 事件的到达顺序）', () => {
    const store = useEdgeDeviceLiveStore()
    store.start()

    store.ingest(WS_EVENT.DATA_UPDATE, frame(3, { soc: 88 }))
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)

    // 只发了数据帧、没有任何状态事件 —— 卡片也必须显示在线。
    expect(store.live[3].status).toBe('active')
  })

  it('状态字符串走同一份词汇表：未知值收敛为 unknown，而不是谎报离线', () => {
    const store = useEdgeDeviceLiveStore()
    store.start()

    store.ingest(WS_EVENT.EDGE_DEVICE_STATUS, statusEvent(1, 'warning'))
    store.ingest(WS_EVENT.EDGE_DEVICE_STATUS, statusEvent(2, 'booting'))
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)

    expect(store.live[1].status).toBe('warning')
    expect(store.live[2].status).toBe('unknown')
  })

  it('一秒内消息再多也不会替换 live 快照引用（渲染次数可数）', () => {
    const store = useEdgeDeviceLiveStore()
    store.start()

    store.ingest(WS_EVENT.DATA_UPDATE, frame(9, { pressure: 1000 }))
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)
    const flushed = store.live

    for (let i = 0; i < 20; i += 1) {
      store.ingest(WS_EVENT.DATA_UPDATE, frame(9, { pressure: 1000 + i }))
    }
    // 同一秒内的 20 条消息：引用不变 ⇒ 视图层一次都不会重算。
    expect(store.live).toBe(flushed)

    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)
    expect(store.live).not.toBe(flushed)
    expect(store.live[9].lastData).toEqual({ pressure: 1019 })
  })

  it('后台标签页不产生渲染，回到前台立刻补一拍', async () => {
    let hidden = true
    const hiddenSpy = vi
      .spyOn(document, 'hidden', 'get')
      .mockImplementation(() => hidden)

    const store = useEdgeDeviceLiveStore()
    store.start()

    store.ingest(WS_EVENT.DATA_UPDATE, frame(5, { co2: 420 }))
    vi.advanceTimersByTime(3 * LIVE_RENDER_INTERVAL_MS)
    // 后台：消息照收（缓冲每设备只留一条），但一次也不写响应式状态。
    expect(store.live).toEqual({})

    hidden = false
    document.dispatchEvent(new Event('visibilitychange'))
    await nextTick()
    expect(store.live[5].lastData).toEqual({ co2: 420 })

    hiddenSpy.mockRestore()
  })

  it('onTick 只在可见时随 1Hz 节拍回调', () => {
    const store = useEdgeDeviceLiveStore()
    const tick = vi.fn()
    store.onTick(tick)
    store.start()

    vi.advanceTimersByTime(3 * LIVE_RENDER_INTERVAL_MS)
    // start() 自己补一拍 + 3 拍定时器
    expect(tick).toHaveBeenCalledTimes(4)

    const hiddenSpy = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    vi.advanceTimersByTime(3 * LIVE_RENDER_INTERVAL_MS)
    expect(tick).toHaveBeenCalledTimes(4)
    hiddenSpy.mockRestore()
  })

  it('引用计数：两个消费者时 stop 一次不中断，全部 stop 后彻底停表', () => {
    const store = useEdgeDeviceLiveStore()
    store.start()
    store.start() // 第二个消费者（例如另一个页面组件）

    store.stop()
    store.ingest(WS_EVENT.DATA_UPDATE, frame(11, { light: 300 }))
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)
    expect(store.live[11]?.lastData).toEqual({ light: 300 })

    store.stop()
    store.ingest(WS_EVENT.DATA_UPDATE, frame(11, { light: 999 }))
    vi.advanceTimersByTime(3 * LIVE_RENDER_INTERVAL_MS)
    // 已无消费者：不再 flush，读数停在最后一次（不会"偷偷"继续更新）
    expect(store.live[11].lastData).toEqual({ light: 300 })
  })

  it('无消息超过保留期的条目在下一次 flush 时被清理（读数回落到 HTTP 快照）', () => {
    const store = useEdgeDeviceLiveStore()
    store.start()

    store.ingest(WS_EVENT.DATA_UPDATE, frame(1, { temperature: 20 }))
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)
    expect(store.live[1]).toBeTruthy()

    // 越过 TTL 再来一条**别的设备**的消息：清理只在有更新时跑。
    vi.advanceTimersByTime(LIVE_ENTRY_TTL_MS + LIVE_RENDER_INTERVAL_MS)
    store.ingest(WS_EVENT.DATA_UPDATE, frame(2, { temperature: 30 }))
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)

    expect(store.live[1]).toBeUndefined()
    expect(store.live[2].lastData).toEqual({ temperature: 30 })
  })


  it('快照事件整体替换状态：缺席的设备被移除（删除后不留幽灵行）', () => {
    const store = useEdgeDeviceLiveStore()
    store.start()

    store.ingest(WS_EVENT.DATA_UPDATE, frame(1, { temperature: 20 }))
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)
    expect(Object.keys(store.live)).toEqual(['1'])

    store.ingest(WS_EVENT.EDGE_DEVICES_LIVE, {
      type: WS_EVENT.EDGE_DEVICES_LIVE,
      payload: {
        at: NOW.toISOString(),
        items: [{ id: 2, status: 'pending' }],
      },
    })
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)

    // 设备 1 不在新快照里 ⇒ 实时层不再持有它（页面回落到 HTTP 列表，列表里也已没有它）
    expect(store.live[1]).toBeUndefined()
    expect(store.live[2]).toMatchObject({ status: 'pending', lastData: null, lastDataAt: null })
  })

  it('同一窗口内：快照为基底，逐帧补丁叠在上面（更新鲜的值不被快照盖掉）', () => {
    const store = useEdgeDeviceLiveStore()
    store.start()

    // 快照先到（值 20），随后同一秒内来了一条逐帧读数为 26
    store.ingest(WS_EVENT.EDGE_DEVICES_LIVE, {
      type: WS_EVENT.EDGE_DEVICES_LIVE,
      payload: { at: NOW.toISOString(), items: [{ id: 5, status: 'active', data: { temperature: 20 } }] },
    })
    store.ingest(WS_EVENT.DATA_UPDATE, frame(5, { temperature: 26 }))
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)

    expect(store.live[5].lastData).toEqual({ temperature: 26 })
    expect(store.live[5].status).toBe('active')
  })

  it('忽略没有设备 ID / 非数值读数的消息（不写进永远匹配不上的键）', () => {
    const store = useEdgeDeviceLiveStore()
    store.start()

    store.ingest(WS_EVENT.DATA_UPDATE, raw({ data: { a: 1 } }))
    store.ingest(WS_EVENT.DATA_UPDATE, raw({ edge_device_id: 'x', data: { a: 1 } }))
    store.ingest(WS_EVENT.DATA_UPDATE, raw({ edge_device_id: 4, data: { bad: 'n/a' } }))
    vi.advanceTimersByTime(LIVE_RENDER_INTERVAL_MS)

    expect(store.live).toEqual({})
  })
})
