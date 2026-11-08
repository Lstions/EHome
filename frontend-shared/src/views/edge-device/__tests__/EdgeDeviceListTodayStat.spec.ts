import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import source from '@/views/edge-device/EdgeDeviceList.vue?raw'

/**
 * F14-b：「今日采样帧」统计卡的取数口径（回归锁）。
 * （2026-11-07：卡片由「今日数据」改名「今日采样帧」，口径从行数改为帧数，见 todayCardText 注释。）
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

/** 取「今日采样帧」卡渲染出来的数字（卡片按 label 定位，避免依赖卡片顺序）。
 *
 * 2026-11-07 改名：「今日数据」→「今日采样帧」。
 * 改名的原因不是措辞偏好 —— 后端 /overview 的 data_count_today 已从
 * 「unified_data 今日**行数**」改为「今日**采样帧数**」（COUNT(DISTINCT device_id,timestamp)，
 * handler_overview.go 的 D2）。L1「值不变不入库」生效后，行数会掉到约 1/5，
 * 若沿用「今日数据」这种无单位说法，用户会读成「今天的数据少了 80%」。
 * 标签必须带「帧」，这是**口径标注**，不是文案润色。 */
function todayCardText(wrapper: ReturnType<typeof mount>) {
  const cards = wrapper.findAll('.stat-card')
  for (const card of cards) {
    if (card.text().includes('今日采样帧')) return card.text()
  }
  return ''
}

describe('F14-b 今日采样帧统计卡取数口径', () => {
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
    expect(text, '未找到「今日采样帧」卡').toContain('今日采样帧')
    // 4321 >= 1000，展示层转成 4.3k；关键是**不得是 0**
    expect(text, '今日采样帧卡读错了 envelope 层级（response.x 应为 response.data.x）').not.toContain('>0<')
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

  // ─── 口径标注（2026-11-07，L1「值不变不入库」生效后） ───
  // data_count_today 已从「今日行数」改为「今日采样帧数」（后端 handler_overview.go 的 D2）。
  // 行数 = 一帧里的一个物理量，帧数 = 有过几帧 —— 两者差 4~5 倍。
  // 标签若不写明「帧」，L1 生效后用户会把这卡读成「今天的数据少了 80%」。

  it('卡片标题必须写明「帧」（口径标注，不是文案润色）', () => {
    expect(source, '「今日数据」缺少单位说明').toContain('今日采样帧')
    // 反向：不得再有裸「今日数据（…）」作为卡片标题
    expect(source, '仍存在无单位的「今日数据」标题').not.toMatch(/label=`今日数据（/)
  })

  it('源码注释必须记录口径变化（否则后来人会把帧数当行数用）', () => {
    // 注释是给维护者的唯一线索：没有它，下次有人"优化"这张卡就会重新引入行数口径
    expect(source).toMatch(/采样帧/)
    expect(source).toMatch(/COUNT\(DISTINCT/)
    // 必须点明它来自哪个后端决策项（D2），能顺着找到实现
    expect(source).toMatch(/D2|handler_overview/)
  })

  it('移动端 label 也必须带范围词（F14 在移动端不能失效）', () => {
    // 本卡当前**没有** mobile-label（走桌面 label；MobileStatCardsResponsive.spec.ts
    // 钉死了本页 mobile-label 恰为 3 个）。这里只做防腐：若将来给本卡加了 mobile-label，
    // 它必须带范围词，否则 ≤768px 时用户看不到「全局」。
    const mobileLabels = [...source.matchAll(/:?mobile-label="([^"]*)"/g)].map(m => m[1])
    // 模板串里的常量引用要先解析成字面量（与 MobileStatCardsResponsive 同口径）
    const resolved = mobileLabels.map((raw) =>
      raw
        .replace(/^`|`$/g, '')
        .replace(/\$\{SCOPE_PAGE\}/g, '本页')
        .replace(/\$\{SCOPE_GLOBAL\}/g, '全局'),
    )
    for (const label of resolved) {
      expect(label, '移动端 label「' + label + '」缺范围词').toMatch(/本页|全局|当前筛选/)
    }
  })
})
