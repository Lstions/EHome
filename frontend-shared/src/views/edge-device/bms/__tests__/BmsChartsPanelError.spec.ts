import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import source from '@/views/edge-device/bms/BmsChartsPanel.vue?raw'
import BmsChartsPanel from '@/views/edge-device/bms/BmsChartsPanel.vue'

/**
 * BmsChartsPanel 失败可见 / 并发闸门 / 取消契约（BMS 慢修复契约 §2.3 单测）。
 *
 * 锁四件事（对应根因报告 R3/R4）：
 *  ① 两张主图都走页面级并发闸门（withHistorySlot），不再与诊断卡一起打满后端；
 *  ② 历史请求超时 30s（不是全局 10s），且失败会「原样重试 1 次」而不是立刻降级；
 *  ③ cells(16 类)/metrics(9 类) 超过降级上限 4 ⇒ **一个逐测点降级请求都不许发**（请求风暴）；
 *  ④ 失败态与「暂无数据」一眼可分（文案含「加载失败」+ 重试按钮）；卸载时 abort 在途请求。
 */

const { clientGet, handleError } = vi.hoisted(() => ({
  clientGet: vi.fn(),
  handleError: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  default: { get: (...args: unknown[]) => clientGet(...args) },
  ApiError: class ApiError extends Error {},
  isApiErrorCode: () => false,
}))

vi.mock('@/utils/feedback', () => ({
  feedback: {
    handleError: (...args: unknown[]) => handleError(...args),
    error: vi.fn(),
    success: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))

// happy-dom 无 canvas：LineChart 必须 mock（真实 echarts.init 会抛）
vi.mock('@/components/charts/LineChart.vue', async () => {
  const { defineComponent: dc, h: rh } = await import('vue')
  return {
    default: dc({
      name: 'LineChart',
      props: {
        series: { type: Array, default: () => [] },
        height: { type: String, default: '' },
        yAxisMin: Number,
        yAxisMax: Number,
        showArea: Boolean,
      },
      setup(props) {
        return () =>
          rh('div', {
            class: 'line-chart-stub',
            'data-height': props.height,
            'data-series-count': String((props.series as unknown[]).length),
          })
      },
    }),
  }
})

const CELL_CATS = Array.from({ length: 16 }, (_, i) => 'cell_voltage_' + (i + 1))

function timeoutError(): Error & { code: string } {
  const e = new Error('timeout of 30000ms exceeded') as Error & { code: string }
  e.code = 'ECONNABORTED'
  return e
}

function canceledError(): Error & { code: string } {
  const e = new Error('canceled') as Error & { code: string }
  e.code = 'ERR_CANCELED'
  return e
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

/** batch 侧返回：只对 requested cats 里在 withData 中的产点，其余返回空数组 */
function installBatchSuccess(withData: string[]) {
  clientGet.mockImplementation(async (_url: string, cfg: { params?: { categories?: string } }) => {
    const requested = String(cfg?.params?.categories ?? '').split(',').filter(Boolean)
    return {
      code: 0,
      message: 'ok',
      data: requested.map((cat) => ({
        category: cat,
        data: withData.includes(cat)
          ? [{ created_at: '2026-10-20T01:00:00.000Z', value: 1 }]
          : [],
      })),
    }
  })
}

function installAlwaysFail() {
  clientGet.mockImplementation(async () => {
    throw timeoutError()
  })
}

/** 失败路径要等「原样重试」的 400ms 退避结束 */
async function settleAfterRetry(): Promise<void> {
  await flushPromises()
  await sleep(650)
  await flushPromises()
  await nextTick()
}

const originalMatchMedia = window.matchMedia
function setViewportDesktop() {
  const stub = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })
  ;(window as unknown as { matchMedia: typeof window.matchMedia }).matchMedia =
    stub as unknown as typeof window.matchMedia
}

beforeEach(() => {
  clientGet.mockReset()
  handleError.mockReset()
  setViewportDesktop()
})

afterEach(() => {
  ;(window as unknown as { matchMedia: typeof window.matchMedia }).matchMedia = originalMatchMedia
})

function mountPanel() {
  return mount(BmsChartsPanel, { props: { deviceId: 14, cellCount: 16, online: true } })
}

describe('失败可见：两张图都显示「加载失败 + 重试」，不静默变「暂无数据」', () => {
  it('全部请求失败：错误态可见、文案含「加载失败」、有重试按钮', async () => {
    installAlwaysFail()
    const wrapper = mountPanel()
    await settleAfterRetry()

    const cards = wrapper.findAll('.charts-row .chart-card')
    const cellCard = cards[0]
    const trendCard = cards[1]

    const cellError = cellCard.find('.chart-state.is-error')
    expect(cellError.exists(), '电芯图应显示错误态').toBe(true)
    expect(cellError.text()).toContain('历史数据加载失败')
    expect(cellError.text()).toContain('重试')
    expect(cellCard.text()).not.toContain('暂无电芯电压历史数据')

    // 运行趋势（桌面 5 个子图）：全部是错误态，不是「暂无该项历史数据」
    const trendErrors = trendCard.findAll('.trend-sub-state.is-error')
    expect(trendErrors).toHaveLength(5)
    for (const err of trendErrors) {
      expect(err.text()).toContain('历史数据加载失败')
      expect(err.text()).not.toContain('暂无')
    }
    expect(trendCard.find('.retry-btn').exists(), '运行趋势应有重试按钮').toBe(true)
  })

  it('失败不放大成降级风暴：16/9 类均超上限 ⇒ 只有 batch 首取+重试，0 个逐测点请求', async () => {
    installAlwaysFail()
    mountPanel()
    await settleAfterRetry()

    const urls = clientGet.mock.calls.map((call) => String(call[0]))
    const batchCalls = urls.filter((u) => u.endsWith('/historical-batch'))
    const singleCalls = urls.filter((u) => u.endsWith('/historical'))
    expect(batchCalls).toHaveLength(4) // 2 张图 × (首取 + 原样重试 1 次)
    expect(singleCalls, '超过降级上限 4 类时不得逐测点降级').toHaveLength(0)
    // showError 语义保持：内部仍有统一 toast 出口（每张图一次）
    expect(handleError).toHaveBeenCalledTimes(2)
  })
})

describe('重试与空态：错误态可恢复，与「暂无数据」区分', () => {
  it('点「重试」成功后错误态消失、图表出现', async () => {
    installAlwaysFail()
    const wrapper = mountPanel()
    await settleAfterRetry()

    const cellCard = wrapper.findAll('.charts-row .chart-card')[0]
    expect(cellCard.find('.chart-state.is-error').exists()).toBe(true)

    installBatchSuccess(CELL_CATS) // 服务端恢复
    await cellCard.find('.chart-state.is-error button').trigger('click')
    await flushPromises()
    await nextTick()
    await flushPromises()

    expect(cellCard.find('.chart-state.is-error').exists()).toBe(false)
    expect(cellCard.find('.chart-state').exists()).toBe(false)
    expect(cellCard.find('.line-chart-stub').exists(), '重试成功后应出图').toBe(true)
  })

  it('请求成功但无数据：显示「暂无」空态，不是错误态', async () => {
    installBatchSuccess([]) // 所有 category 都返回空 data
    const wrapper = mountPanel()
    await flushPromises()
    await flushPromises()
    await nextTick()

    const cellCard = wrapper.findAll('.charts-row .chart-card')[0]
    expect(cellCard.find('.chart-state.is-error').exists()).toBe(false)
    expect(cellCard.find('.chart-state').text()).toBe('暂无电芯电压历史数据')
  })
})

describe('取消：卸载时 abort 在途请求', () => {
  it('每张图一个 AbortController，unmount 后 signal 全部 aborted', async () => {
    const signals: Array<AbortSignal | undefined> = []
    clientGet.mockImplementation((_url: string, cfg: { signal?: AbortSignal }) => {
      signals.push(cfg.signal)
      return new Promise((_resolve, reject) => {
        cfg.signal?.addEventListener('abort', () => reject(canceledError()))
      })
    })
    const wrapper = mountPanel()
    await nextTick()
    expect(signals).toHaveLength(2)
    expect(signals.every((s) => s && !s.aborted)).toBe(true)

    wrapper.unmount()
    expect(signals.every((s) => s?.aborted), '卸载必须在途请求全部 abort').toBe(true)
    await flushPromises()
  })
})

describe('源码契约：并发闸门 / 30s 超时 / abort / 错误态', () => {
  const code = source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/<!--[\s\S]*?-->/g, '')

  it('两个 fetch 都走 withHistorySlot（与诊断卡共用页面级并发 ≤3）', () => {
    expect(code).toContain("from '@/composables/useHistoryQueue'")
    expect((code.match(/withHistorySlot\(/g) ?? []).length).toBeGreaterThanOrEqual(2)
  })

  it('两张图的 useHistoryData 都传 timeoutMs: 30000（不是全局 10s）', () => {
    expect((code.match(/timeoutMs: 30000/g) ?? []).length).toBe(2)
  })

  it('每类图各一个 AbortController：重复取数与卸载时 abort 旧请求', () => {
    expect((code.match(/new AbortController\(\)/g) ?? []).length).toBe(2)
    expect(code).toContain('cellController?.abort()')
    expect(code).toContain('metricController?.abort()')
    expect(code).toMatch(/onUnmounted\(/)
  })

  it('错误态文案与「暂无数据」区分，并提供重试', () => {
    expect(code).toContain('历史数据加载失败，请稍后重试')
    expect(code).toContain('重试')
    expect(code).toContain('暂无电芯电压历史数据')
  })
})
