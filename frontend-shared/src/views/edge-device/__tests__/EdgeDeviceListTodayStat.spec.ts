import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import source from '@/views/edge-device/EdgeDeviceList.vue?raw'

/**
 * F14-b：「今日数据」统计卡的取数口径（回归锁）。
 *
 * 实测缺陷（2026-09-15）：该卡片的值有两层独立的错，叠加后**恒定显示 0**。
 *
 *   ① 后端 /overview 的 gin.H 里从来没有 data_count_today 字段
 *      （全仓 grep -rn data_count_today backend/ = 0 命中）；
 *   ② 前端读的是 `response.data_count_today`，但 axios 拦截器返回的是**整个 envelope**
 *      （client.ts:66-74 `return response.data`，即 {code,data,message}），
 *      所以正确层级是 `response.data.data_count_today`。
 *
 * 为什么必须用**真实 envelope 形状**的 mock：仓库里原有的 mock 写的是
 *   `Promise.resolve({ data_count_today: 0 })`
 * ——它既不是 envelope 也不是 payload，是一个**任何读法都返回 0** 的形状。
 * 这种 mock 会把缺陷锁死在绿色里：无论代码读 `response.x` 还是 `response.data.x`，
 * 断言「显示 0」都成立。本文件因此改成喂真实形状，并断言**非 0**值能显示出来。
 *
 * See docs/分析/后续工作计划与方案-2026-09-15.md §1.7 F14.
 */

const { mockGet } = vi.hoisted(() => ({ mockGet: vi.fn() }))

vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
    getCachedList: vi.fn(() => ({ items: [], total: 0 })),
  }),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ connected: false, connect: vi.fn(), subscribe: vi.fn(() => vi.fn()) }),
}))
vi.mock('@/api/edgeDevice', () => ({
  compactEdgeDeviceList: (items: unknown) => Array.isArray(items) ? items : [],
  edgeDeviceApi: {
    getList: vi.fn(() => Promise.resolve({ items: [], total: 0 })),
    create: vi.fn(), update: vi.fn(), delete: vi.fn(),
    getLogicalDeviceInfo: vi.fn(() => Promise.resolve(null)),
    getCandidates: vi.fn(() => Promise.resolve([])),
    getDriverCommands: vi.fn(() => Promise.resolve([])),
  },
}))
vi.mock('@/api/channel', () => ({
  compactChannelList: (items: unknown) => Array.isArray(items) ? items : [],
  channelApi: { getList: vi.fn(() => Promise.resolve([])), update: vi.fn() },
}))
vi.mock('@/api/deviceConfig', () => ({ deviceConfigApi: { getList: vi.fn(() => Promise.resolve({ list: [] })) } }))
vi.mock('@/api/parser', () => ({ parserApi: { getList: vi.fn(() => Promise.resolve([])) } }))
vi.mock('@/api/client', () => ({ default: { get: mockGet } }))

const stubs = {
  SkeletonCard: { template: '<div />' },
  EmptyState: { template: '<div />' },
  // CountUp 退化成同步打印，但**必须保留 decimals 格式化**：
  // 真实组件会把 4.321 渲染成 '4.3'，stub 若原样打印就会让断言看着像产品错。
  CountUp: {
    props: ['value', 'decimals', 'suffix'],
    template: '<span class="count-up-probe">{{ Number(value).toFixed(decimals || 0) }}{{ suffix || "" }}</span>',
  },
}

/** 后端 /overview 的真实响应形状：envelope 包 payload */
function envelope(payload: Record<string, unknown>) {
  return { code: 200, message: 'ok', data: payload }
}

async function mountWithOverview(payload: Record<string, unknown> | null) {
  mockGet.mockReset()
  mockGet.mockImplementation(() =>
    payload === null ? Promise.reject(new Error('network down')) : Promise.resolve(envelope(payload)),
  )
  const wrapper = mount(EdgeDeviceList, { global: { stubs } })
  await flushPromises()
  await flushPromises()
  return wrapper
}

/** 取「今日数据」卡渲染出来的数字（卡片按 label 定位，避免依赖卡片顺序） */
function todayCardText(wrapper: ReturnType<typeof mount>) {
  const cards = wrapper.findAll('.stat-card')
  for (const card of cards) {
    if (card.text().includes('今日数据')) return card.text()
  }
  return ''
}

describe('F14-b 今日数据统计卡取数口径', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('后端返回真实 envelope 时，卡片显示 data.data_count_today（而不是被 || 0 吞掉）', async () => {
    const wrapper = await mountWithOverview({
      nodes: { total: 1, online: 1, offline: 0 },
      edge_devices: { total: 1, online: 1, offline: 0 },
      latest_data: [],
      data_count_today: 4321,
    })
    const text = todayCardText(wrapper)
    expect(text, '未找到「今日数据」卡').toContain('今日数据')
    // 4321 >= 1000，展示层转成 4.3k；关键是**不得是 0**
    expect(text, '今日数据卡读错了 envelope 层级（response.x 应为 response.data.x）').not.toContain('>0<')
    expect(text).toContain('4.3k')
  })

  it('分母守卫：mock 喂的确实是 envelope 形状（防止测试自己退化成恒 0 假绿）', async () => {
    const wrapper = await mountWithOverview({ data_count_today: 7 })
    const text = todayCardText(wrapper)
    expect(text, '7 应原样显示（<1000 不加千分位）').toContain('7')
  })

  it('字段缺失时不得伪装成合法的 0：应显示占位符而不是 0', async () => {
    // 这正是本次缺陷的形态：后端没这个字段。旧代码 `response.data_count_today || 0`
    // 把「字段不存在」和「今天真的是 0 条」渲染成同一个东西，用户无从分辨。
    const wrapper = await mountWithOverview({
      nodes: { total: 0, online: 0, offline: 0 },
      edge_devices: { total: 0, online: 0, offline: 0 },
      latest_data: [],
    })
    const text = todayCardText(wrapper)
    expect(text, '字段缺失应显示 — 而不是 0').toContain('—')
  })

  it('源码层：不得再读 response.data_count_today（层级错误）', () => {
    expect(source).not.toContain('response.data_count_today')
    expect(source).toContain('response.data?.data_count_today')
  })
})
