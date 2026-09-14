import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { Window } from 'happy-dom'
import { parse, compileStyleAsync } from '@vue/compiler-sfc'
import { readFileSync } from 'node:fs'
import Monitor from '../Monitor.vue'

// Mock API
vi.mock('@/api/monitor', () => ({
  getMetricsSummary: vi.fn(() =>
    Promise.resolve({
      code: 200,
      data: {
        http: { requests_total: 100, requests_in_flight: 2 },
        mqtt: { messages_received: 50, messages_sent: 30, connection_errors: 0 },
        device: { online: 3, offline: 1 },
        node: { online: 2, offline: 0 },
        data: { points_collected: 5000, points_stored: 4990 },
        websocket: { connections_active: 4, messages_total: 200 },
        control: {
          operations_total: 20, active: 2, queued: 1, succeeded: 15, failed: 2,
          unknown: 1, unresolved_unknown: 1, cancelled: 0, outbox_pending: 1,
          outbox_leased: 0, capability_stale_nodes: 1, audit_write_failures: 0,
        },
      },
    })
  ),
}))

// Mock router
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

/** el-progress 替身：把 :color 写到 data-color 上，便于断言真实取到的主题色。 */
const ProgressStub = defineComponent({
  name: 'ElProgress',
  props: {
    percentage: { type: Number, default: 0 },
    strokeWidth: { type: Number, default: 6 },
    color: { type: [String, Array, Function], default: '' },
  },
  setup(props) {
    return () => h('div', {
      class: 'el-progress',
      'data-color': typeof props.color === 'string' ? props.color : '',
    })
  },
})

// Stub Element Plus components
// 注意：**不** stub PageHeader —— I-6 的断言必须打在 PageHeader 的**真实**渲染结果上
// （.page-header h2 的文本），stub 掉就退化成"我 stub 了我自己"，摘除组件也不会变红。
// 范式同 views/profile/__tests__/Profile.spec.ts:54「PageHeader 是项目内组件，让真实组件渲染」。
const stubs = {
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-row': { template: '<div class="el-row"><slot /></div>' },
  'el-col': { template: '<div class="el-col"><slot /></div>' },
  // emits 必须声明：否则父组件的 @click 会同时经 $attrs 落到根 <button>（原生监听）
  // 与 $emit('click') 两条路径，点击一次触发两遍 handler —— 会让重试次数断言假绿/假红。
  'el-button': {
    emits: ['click'],
    template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>',
  },
  'el-select': { template: '<select class="el-select" @change="$emit(\'change\')"><slot /></select>' },
  'el-option': { template: '<option />' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-descriptions': { template: '<div class="el-descriptions"><slot /></div>' },
  'el-descriptions-item': { template: '<div class="el-desc-item"><slot /></div>' },
  // color 透传到 DOM：用于断言主题切换时进度条颜色随之变化（真实取值，非源码字符串断言）
  'el-progress': ProgressStub,
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  // 真实 ElAlert：有 #title 插槽时用插槽，否则回落到 title prop。替身必须同样支持，
  // 否则错误态内的「重试」按钮会被吞掉，等于把失败态断言做成空断言。
  'el-alert': {
    props: ['title', 'type'],
    template:
      '<div class="el-alert" :class="`el-alert--${type}`">' +
      '<div class="el-alert__content"><slot name="title">{{ title }}</slot></div>' +
      '</div>',
  },
}

/**
 * 主题 token 测试夹具：与 src/styles/theme.css 的 :root / html.dark 取值一致
 * （--color-success 亮 #67c23a / 暗 #85ce61，--color-danger 亮 #f56c6c / 暗 #f78989）。
 */
const THEME_STYLE_ID = 'monitor-spec-theme-tokens'
function installThemeTokens() {
  document.getElementById(THEME_STYLE_ID)?.remove()
  const style = document.createElement('style')
  style.id = THEME_STYLE_ID
  style.textContent = [
    ':root { --color-primary: #409eff; --color-success: #67c23a; --color-warning: #e6a23c; --color-danger: #f56c6c; --color-info: #909399; }',
    'html.dark { --color-primary: #409eff; --color-success: #85ce61; --color-warning: #ebb563; --color-danger: #f78989; --color-info: #a6a9ad; }',
  ].join('\n')
  document.head.appendChild(style)
}

/** 读取四个进度条实际渲染出的颜色（外层用 stub 透传 :color）。 */
function renderedProgressColors(wrapper: ReturnType<typeof mount>): string[] {
  return wrapper.findAll('.el-progress').map(el => el.attributes('data-color') || '')
}

/**
 * 390px 窄屏页头契约（**真实级联**，非源码字符串包含）。
 *
 * happy-dom 有 CSS 级联：@media 规则在**新建**的 Window 里按 innerWidth 求值
 * （实测 w.matchMedia('(max-width: 768px)').matches === true，computed flexWrap === 'wrap'）。
 * 但**当前**测试窗口的 @media 不会在改 innerWidth 后重算（实测 1024→390 仍为 false），
 * 故这里新开一个 390px Window、注入 @vue/compiler-sfc 真实编译后的 scoped CSS，
 * 再按 PageHeader 的真实 DOM 形状（.page-header > .page-header-left/-right）合成节点读计算值。
 *
 * 为什么不断言"像素没溢出"：happy-dom **无布局引擎**，getBoundingClientRect/scrollWidth
 * 恒为 0（仓内既有约定，见 views/config/__tests__/DeviceConfigList.spec.ts:164 的同类说明）。
 * 因此这里断言的是"390px 下让页头能换行、操作区独占一行"的**级联事实**
 * —— 这是横向溢出的**前置条件**；真像素几何由主控的 e2e 门禁覆盖。
 * 若把本用例里的 :deep(.page-header) 换行规则删掉，flexWrap 会变回 'nowrap'，用例变红。
 */
async function compiledScopedCss(file: string): Promise<string> {
  const { descriptor } = parse(readFileSync(file, 'utf8'), { filename: file })
  const chunks: string[] = []
  for (const block of descriptor.styles) {
    const res = await compileStyleAsync({
      source: block.content,
      filename: file,
      id: 'data-v-narrow',
      scoped: Boolean(block.scoped),
    })
    expect(res.errors, JSON.stringify(res.errors)).toEqual([])
    chunks.push(res.code)
  }
  return chunks.join('\n')
}


/**
 * happy-dom 的 Window 自带**另一套** DOM 类型（happy-dom/lib/nodes/...），与 tsconfig 的
 * lib.dom 不是同一套：直接把它的节点传给 getComputedStyle/appendChild 会触发 TS2345
 * （两边各有私有成员，互不兼容）。这里做**一次**收窄到标准 DOM 类型的外观，下游全部用
 * 标准类型 —— 不需要 any，也不必在每个调用点撒 as。
 */
interface NarrowWindow {
  innerWidth: number
  document: Document
  getComputedStyle: (el: Element) => CSSStyleDeclaration
  matchMedia: (query: string) => { matches: boolean }
}

function newNarrowWindow(width: number): NarrowWindow {
  const w = new Window({ width, height: 844 }) as unknown as NarrowWindow
  w.innerWidth = width
  return w
}

describe('Monitor.vue 页头 390px 窄屏契约（真实级联）', () => {
  it('页头在 390px 下可换行、操作区独占一行，不把标题挤成逐字竖排', async () => {
    const css = await compiledScopedCss('src/views/monitor/Monitor.vue')

    const w = newNarrowWindow(390)
    const style = w.document.createElement('style')
    style.textContent = css
    w.document.head.appendChild(style)

    const make = (cls: string, parent: Element) => {
      const el = w.document.createElement('div')
      el.className = cls
      el.setAttribute('data-v-narrow', '')
      parent.appendChild(el)
      return el
    }
    const root = make('monitor-container', w.document.body)
    const header = make('page-header', root)
    const left = make('page-header-left', header)
    const right = make('page-header-right', header)

    // 前置条件：390px 真的命中了 <=768px 媒体查询（否则下面的断言全是空断言）
    expect(w.matchMedia('(max-width: 768px)').matches).toBe(true)

    const cs = (el: Element) => w.getComputedStyle(el)
    expect(cs(header).flexWrap).toBe('wrap')       // 标题行与操作区分行
    expect(cs(left).flex).toBe('1 1 0%')           // 标题可收缩，不被操作区挤出
    expect(cs(right).width).toBe('100%')           // 操作区占满第二行
    expect(cs(right).justifyContent).toBe('flex-end')
  })

  it('反证：不匹配的 class 取不到该声明（证明测的是选择器而非恒真）', async () => {
    const css = await compiledScopedCss('src/views/monitor/Monitor.vue')
    const w = newNarrowWindow(390)
    const style = w.document.createElement('style')
    style.textContent = css
    w.document.head.appendChild(style)

    const root = w.document.createElement('div')
    root.className = 'monitor-container'
    root.setAttribute('data-v-narrow', '')
    w.document.body.appendChild(root)
    const notHeader = w.document.createElement('div')
    notHeader.className = 'not-the-header'
    notHeader.setAttribute('data-v-narrow', '')
    root.appendChild(notHeader)

    expect(w.getComputedStyle(notHeader).flexWrap).toBe('')
  })
})

describe('Monitor.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    document.documentElement.classList.remove('dark')
    installThemeTokens()
  })

  afterEach(() => {
    document.documentElement.classList.remove('dark')
    document.getElementById(THEME_STYLE_ID)?.remove()
  })

  it('renders monitor container', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.monitor-container').exists()).toBe(true)
  })

  it('renders toolbar actions', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.toolbar-actions').exists()).toBe(true)
    expect(wrapper.text()).toContain('系统监控')
  })

  // ─── I-6 页头统一：PageHeader 承担唯一标题，页内不得再有第二个「系统监控」 ───────
  //
  // 断言打在**渲染结果**上（PageHeader 真实渲染出的 .page-header h2），
  // 不是源码里有没有 "PageHeader" 字符串 —— 后者会被注释或未使用的 import 骗过。
  // 变异自证：摘掉 Monitor.vue 里的 <PageHeader> 后本用例必须变红。
  it('renders PageHeader as the single page title', () => {
    const wrapper = mount(Monitor, { global: { stubs } })

    expect(wrapper.find('.page-header').exists()).toBe(true)
    expect(wrapper.find('.page-header h2').text()).toBe('系统监控')

    // 去重：原工具栏内的 <h2>系统监控</h2> 必须已移除，而不是与 PageHeader 并存。
    const headings = wrapper.findAll('h1, h2')
      .filter(h => h.text().includes('系统监控'))
    expect(headings).toHaveLength(1)
    // 旧的 .toolbar 容器一并消失（PageHeader 取代了它，不是叠加）
    expect(wrapper.find('.toolbar').exists()).toBe(false)
  })

  it('标题是可读文本而非 emoji-only 字形（F17 既有守卫，改挂到 PageHeader 上）', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    const title = wrapper.find('.page-header h2').text()
    expect(title).toBe('系统监控')
    // 守卫本体：标题必须是真实文字，不得退化成纯 emoji / 符号
    expect(/[\u4e00-\u9fa5A-Za-z0-9]/.test(title)).toBe(true)
    expect(title).toMatch(/^[^\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]*$/u)
  })

  it('renders refresh button', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    const btn = wrapper.find('button')
    expect(btn.exists()).toBe(true)
    expect(btn.text()).toContain('刷新')
  })

  it('renders stat cards section', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.stat-cards').exists()).toBe(true)
  })

  it('loads metrics on mount', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    // After API resolves, stat values should be visible
    expect(wrapper.text()).toContain('100') // http.requests_total
    expect(wrapper.text()).toContain('3')   // device.online
  })

  it('displays HTTP requests total', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('HTTP')
    expect(wrapper.text()).toContain('请求总数')
  })

  it('displays device online/offline status', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('设备在线状态')
  })

  it('displays node online/offline status', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('节点在线状态')
  })

  it('renders detail panels section', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    // 首次响应未落定 → 骨架屏（三态之一），此刻不得渲染任何具体数值
    expect(wrapper.find('[data-test="monitor-loading"]').exists()).toBe(true)
    expect(wrapper.find('.detail-panels').exists()).toBe(false)

    await flushPromises()
    expect(wrapper.find('.detail-panels').exists()).toBe(true)
    expect(wrapper.find('[data-test="monitor-loading"]').exists()).toBe(false)
  })

  it('shows durable control health and attention counts', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('控制面健康')
    expect(wrapper.text()).toContain('未处置 UNKNOWN')
    expect(wrapper.text()).toContain('能力快照过期')
    expect(wrapper.find('.control-alert').text()).toContain('2 项需要关注')
  })

  it('renders HTTP monitoring panel', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('HTTP 监控')
  })

  it('renders MQTT monitoring panel', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('MQTT 监控')
  })

  it('renders WebSocket monitoring panel', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('WebSocket')
  })

  it('renders footer with last update time', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.footer-info').exists()).toBe(true)
  })

  it('formats large numbers with K/M suffix', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    // 5000 points → "5.00K"
    expect(wrapper.text()).toContain('K')
  })

  it('进度条颜色是语义 token 引用，不随主题重新计算也不含硬编码色值（F17）', async () => {
    document.documentElement.classList.remove('dark')
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    const lightColors = renderedProgressColors(wrapper)
    // 四个进度条：设备在线/离线、节点在线/离线
    expect(lightColors).toEqual([
      'var(--color-success)', 'var(--color-danger)',
      'var(--color-success)', 'var(--color-danger)',
    ])

    // 切换到暗色（与 stores/theme.ts 一致：html.dark + data-theme）
    document.documentElement.classList.add('dark')
    document.documentElement.setAttribute('data-theme', 'dark')
    await flushPromises()

    // 契约（规范 §3.6.2）：传给 el-progress 的是 var(--color-*) 字符串，
    // 由**浏览器**按当前主题解析 —— 组件无需在主题切换时重新计算，
    // 这比「JS 解析 + MutationObserver」更强：取值不经 JS 中转，
    // 不可能出现「JS 与 CSS 各算一套」的偏差。
    expect(renderedProgressColors(wrapper)).toEqual(lightColors)

    // 守卫：将来有人改回静态十六进制常量，这里必须变红。
    // 分工：happy-dom 不做 var() 替换，故本层只能断言「props 是 token 引用」；
    // 「该 token 在亮暗下确实取不同值」由 src/styles/__tests__/SidebarThemeTokens.spec.ts
    // 的 token 解析断言与 .tmp-probe 下的真实浏览器探针覆盖。
    for (const c of lightColors) {
      expect(c, '进度条配色必须是 var(--color-*) 语义 token').toMatch(/^var\(--color-[a-z]+\)$/)
      expect(c).not.toMatch(/^#|^rgb/)
    }
    wrapper.unmount()
  })

  it('暗色下挂载、再切回亮色，进度条配色仍是同一组 token 引用', async () => {
    document.documentElement.classList.add('dark')
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(renderedProgressColors(wrapper)).toEqual([
      'var(--color-success)', 'var(--color-danger)',
      'var(--color-success)', 'var(--color-danger)',
    ])

    document.documentElement.classList.remove('dark')
    document.documentElement.removeAttribute('data-theme')
    await flushPromises()
    // 组件不持有主题状态副本，故不依赖任何回调时序：token 引用恒定。
    expect(renderedProgressColors(wrapper)).toEqual([
      'var(--color-success)', 'var(--color-danger)',
      'var(--color-success)', 'var(--color-danger)',
    ])
    wrapper.unmount()
  })

  it('源码不再引用 getThemeColors/THEME_COLORS（F17：进度条配色已完全交给 CSS 变量）', async () => {
    const src = (await import('../Monitor.vue?raw')).default as string
    expect(src).not.toContain('getThemeColors')
    expect(src).not.toContain('THEME_COLORS')
    // 反向守卫：必须真的用了语义 token，否则上面的 not.toContain 在「整个删掉配色」时也会通过
    expect(src.match(/var\(--color-[a-z]+\)/g)?.length ?? 0).toBeGreaterThanOrEqual(4)
  })

  // ─── KPI 范围标注（审计 Q3 三页均无范围标注 / §4.3 统计卡 MUST） ───

  it('四个 KPI 都带范围词，且范围与后端统计口径一致', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()

    const cards = wrapper.findAll('.stat-card')
    expect(cards).toHaveLength(4)

    // ① 每张卡都必须能读出范围词（§4.3.1 MUST）
    for (const card of cards) {
      expect(card.find('.stat-label').text(), '缺少范围词: ' + card.find('.stat-label').text())
        .toMatch(/本页|当前筛选|全局|进程启动以来/)
    }

    // ② 口径校验：计数器类 KPI 是 Prometheus Counter（进程启动以来累计），
    //    设备/节点在线数是全表 COUNT（全局）。范围词必须与之一致，否则是"标注了但标错"。
    const labelOf = (text: string) => cards.find(c => c.text().includes(text))!.find('.stat-label').text()
    expect(labelOf('HTTP 请求总数')).toContain('进程启动以来')
    expect(labelOf('数据点采集总数')).toContain('进程启动以来')
    expect(labelOf('设备在线状态')).toContain('全局')
    expect(labelOf('节点在线状态')).toContain('全局')
  })

  it('范围词不与数值/标签重叠：是标签内的独立元素而非拼接字符串', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()

    // 范围词必须是可单独取样式与断行的元素（窄屏要换行，§4.2.5 防逐字竖排），
    // 因此不能只把文字塞进标签字符串里。
    const scopes = wrapper.findAll('.stat-scope')
    expect(scopes).toHaveLength(4)
    for (const s of scopes) {
      expect(s.text().length).toBeGreaterThan(0)
      // 范围词不能为空标签（空 chip 是"渲染失败"的观感）
      expect(s.text().trim()).not.toBe('')
    }
  })

  it('源码不再用 -- 作未知占位（§3.4.5 统一为 —）', async () => {
    const monitorSource = (await import('../Monitor.vue?raw')).default as string
    expect(monitorSource).not.toMatch(/ref\('--'\)/)
    expect(monitorSource).not.toMatch(/lastUpdateTime\s*=\s*'--'/)
    // 未拉取到指标前，「最后更新」不得伪造成一个具体时刻
    expect(monitorSource).toContain("ref(UNKNOWN)")
  })

  // ─── F28 / U-1 家族第 4 处：接口失败伪装成「值为 0」 ───
  //
  // 缺陷：metrics 为 null 时全文件 40 处 `|| 0` 兜底，于是「接口 500」与
  // 「接口成功但系统真的空闲」在 DOM 上**逐字段相同**（主控实测两组 values 均为
  // ["0","0/0","0/0","0","0","0","0","0"] 且无任何错误态）。
  // 下列用例把「A 与 B 必须可区分」钉成断言。

  describe('F28 失败态不得伪装成 0（A/B 可区分）', () => {
    const loadOk = async () => {
      const { getMetricsSummary } = await import('@/api/monitor')
      return vi.mocked(getMetricsSummary)
    }

    /** 成功但全 0：完整 shape，避免"全 0"因缺字段而名不副实。 */
    const ALL_ZERO = {
      code: 200,
      data: {
        timestamp: Date.now(),
        http: { requests_total: 0, requests_in_flight: 0 },
        mqtt: { messages_received: 0, messages_sent: 0, connection_errors: 0 },
        device: { online: 0, offline: 0 },
        node: { online: 0, offline: 0 },
        data: { points_collected: 0, points_stored: 0 },
        ota: { upgrades_total: 0 },
        websocket: { connections_active: 0, messages_total: 0 },
        control: {
          operations_total: 0, active: 0, queued: 0, succeeded: 0, failed: 0,
          unknown: 0, unresolved_unknown: 0, cancelled: 0, outbox_pending: 0,
          outbox_leased: 0, capability_stale_nodes: 0, audit_write_failures: 0,
        },
      },
    }

    /** KPI 取值：去掉全部空白，口径与主控探针的 "0/0" 完全一致。 */
    const statTexts = (wrapper: ReturnType<typeof mount>) =>
      wrapper.findAll('.stat-value').map(n => n.text().replace(/\s+/g, ''))

    /** 与真实断言同口径的快照：只取 DOM 事实。 */
    const snapshot = (wrapper: ReturnType<typeof mount>) => ({
      statValues: statTexts(wrapper),
      controlValues: wrapper.findAll('.control-metric strong').map(n => n.text().trim()),
      hasError: wrapper.find('[data-test="monitor-error"]').exists(),
      hasDetailError: wrapper.find('[data-test="monitor-detail-error"]').exists(),
      hasDetailPanels: wrapper.find('.detail-panels').exists(),
      hasSkeleton: wrapper.find('[data-test="monitor-loading"]').exists(),
    })

    it('接口 500：错误态可见 + 配套重试，且 KPI 显示「—」而不是 0', async () => {
      const api = await loadOk()
      api.mockRejectedValueOnce(new Error('Request failed with status code 500'))

      const wrapper = mount(Monitor, { global: { stubs } })
      await flushPromises()

      // 1) 常驻错误态（不是一闪而过的 toast）
      const alert = wrapper.find('[data-test="monitor-error"]')
      expect(alert.exists()).toBe(true)
      expect(alert.text()).toContain('获取监控数据失败')
      expect(alert.text()).toContain('500')

      // 2) 错误态**内部**的重试入口（页头「手动刷新」不算：探针的 looseRetry 正是在此误报）
      const retry = wrapper.find('[data-test="monitor-retry"]')
      expect(retry.exists()).toBe(true)
      expect(retry.text()).toBe('重试')
      // 重试入口必须是错误区域的后代，而不是页面上任意一个按钮
      expect(alert.element.contains(retry.element)).toBe(true)

      // 3) KPI 是未知占位「—」，绝不是 0
      expect(statTexts(wrapper)).toEqual(['—', '—/—', '—/—', '—'])

      // 4) 详情区整块进入错误态，不再渲染一排 0
      expect(wrapper.find('[data-test="monitor-detail-error"]').exists()).toBe(true)
      expect(wrapper.find('.detail-panels').exists()).toBe(false)

      // 5) 失败时不得断言"控制面正常"，也不得给出"0 项需要关注"
      expect(wrapper.find('[data-test="monitor-control-tag"]').text()).toBe('状态未知')
      expect(wrapper.text()).not.toContain('项需要关注')
      expect(wrapper.text()).not.toContain('正常')

      // 6) 页脚必须说明数据已过期
      expect(wrapper.find('[data-test="monitor-stale"]').exists()).toBe(true)
      expect(wrapper.text()).not.toContain('暂无数据')
    })

    it('接口成功但全 0：正常显示 0，绝不出现错误态（不误伤真实数据）', async () => {
      const api = await loadOk()
      api.mockResolvedValueOnce(ALL_ZERO)

      const wrapper = mount(Monitor, { global: { stubs } })
      await flushPromises()

      expect(wrapper.find('[data-test="monitor-error"]').exists()).toBe(false)
      expect(wrapper.find('[data-test="monitor-detail-error"]').exists()).toBe(false)
      expect(wrapper.find('.detail-panels').exists()).toBe(true)
      // 0 是「确实是 0」：四张 KPI 卡照常渲染 0 / 0/0（= 主控探针里的 values 口径）
      expect(statTexts(wrapper)).toEqual(['0', '0/0', '0/0', '0'])
      expect(wrapper.findAll('.control-metric strong').map(n => n.text().trim()))
        .toEqual(Array(9).fill('0'))
    })

    it('核心判据：失败的 DOM 与「成功但全 0」的 DOM 必须可区分', async () => {
      const api = await loadOk()

      api.mockRejectedValueOnce(new Error('Request failed with status code 500'))
      const failed = mount(Monitor, { global: { stubs } })
      await flushPromises()

      api.mockResolvedValueOnce(ALL_ZERO)
      const zero = mount(Monitor, { global: { stubs } })
      await flushPromises()

      expect(snapshot(failed)).not.toEqual(snapshot(zero))

      // 逐字段给出可核对的差异（失败态必须有错误组件；成功全 0 态必须没有）
      expect(snapshot(failed).hasError).toBe(true)
      expect(snapshot(zero).hasError).toBe(false)
      expect(snapshot(failed).hasDetailError).toBe(true)
      expect(snapshot(zero).hasDetailError).toBe(false)
      expect(snapshot(failed).controlValues).not.toEqual(snapshot(zero).controlValues)
      // 「一片 0」只允许出现在真·成功分支
      expect(snapshot(zero).statValues).toEqual(['0', '0/0', '0/0', '0'])
      expect(snapshot(failed).statValues).toEqual(['—', '—/—', '—/—', '—'])

      failed.unmount()
      zero.unmount()
    })

    it('重试入口真实可用：第二次成功后错误态消失并恢复真实数值', async () => {
      const api = await loadOk()
      api.mockRejectedValueOnce(new Error('Request failed with status code 500'))

      const wrapper = mount(Monitor, { global: { stubs } })
      await flushPromises()
      expect(wrapper.find('[data-test="monitor-error"]').exists()).toBe(true)
      expect(api, '挂载后应只拉取一次（自动刷新定时器未到期）').toHaveBeenCalledTimes(1)

      api.mockResolvedValueOnce({
        ...ALL_ZERO,
        data: {
          ...ALL_ZERO.data,
          http: { requests_total: 123456, requests_in_flight: 3 },
          device: { online: 3, offline: 1 },
        },
      })
      await wrapper.find('[data-test="monitor-retry"]').trigger('click')
      await flushPromises()

      expect(api).toHaveBeenCalledTimes(2)
      expect(wrapper.find('[data-test="monitor-error"]').exists()).toBe(false)
      expect(wrapper.find('[data-test="monitor-detail-error"]').exists()).toBe(false)
      expect(wrapper.find('.detail-panels').exists()).toBe(true)
      // 123456 → 123.46K，证明格式化路径未被未知态污染
      expect(statTexts(wrapper)).toEqual(['123.46K', '3/4', '0/0', '0'])
    })

    it('200 但 envelope 无 data（后端异常包装）同样进入失败态，不得当成"成功且全 0"', async () => {
      const api = await loadOk()
      api.mockResolvedValueOnce({ code: 200, message: 'ok' } as never)

      const wrapper = mount(Monitor, { global: { stubs } })
      await flushPromises()

      expect(wrapper.find('[data-test="monitor-error"]').exists()).toBe(true)
      expect(statTexts(wrapper)).toEqual(['—', '—/—', '—/—', '—'])
    })

    it('源码守卫：模板里不得再出现「指标值 || 0」兜底（缺陷本体）', async () => {
      const src = (await import('../Monitor.vue?raw')).default as string
      // 指标取值一律经 metric()/metricText()，模板中不再有 || 0
      // 注意：文件内含 <template #header>/<template #title> 等内层插槽，
      // 首个 '</template>' 只是最早那个内层块的闭合 —— 必须切到**最后一个**。
      const templateBody = src.slice(src.indexOf('<template>'), src.lastIndexOf('</template>'))
      // 只有**插值**里的 || 0 才是缺陷本体（把未知渲染成 0）。
      // :class="{ attention: (x || 0) > 0 }" 是布尔判定，不产生任何可见数值，保留。
      const interpolations = templateBody.match(/\{\{[\s\S]*?\}\}/g) ?? []
      expect(interpolations.length).toBeGreaterThan(20)
      for (const expr of interpolations) {
        expect(expr, '插值不得用 || 0 兜底未知值: ' + expr)
          .not.toMatch(/\|\||\?\?\s*0/)
      }
      // 反向守卫：metric 必须真的用上了，否则"删掉全部数值"也能让上一行通过
      expect(templateBody.match(/metric(Text)?\(/g)?.length ?? 0).toBeGreaterThanOrEqual(25)
      // 未知态必须落到 UNKNOWN 常量，而不是新造占位符
      expect(src).toContain("import { UNKNOWN, metricOrDash } from '@/utils/format'")
    })
  })

  // ─── F31 残余：接口挂起时 DOM 必须留下可机械判定的「刷新中」痕迹 ───
  //
  // 缺陷：fetchMetrics 只在 finally 置 isLoading=false（从不置 true），于是
  // 「首屏成功 → 接口挂起 → 点手动刷新」这一中间态在 DOM 上**既无骨架也无错误态**，
  // .stat-value 显示的是上一次成功的陈旧真值 —— 门禁会把陈旧值当新鲜值，
  // 在一份不代表终态的 DOM 上做裁切/溢出判定。
  // 下列用例把「请求在飞」钉成 DOM 事实（用 deferred 控制 resolve 时机，
  // 不使用任何 waitForTimeout）。
  describe('F31 残余：刷新中标记必须可机械判定', () => {
    const OK = (requestsTotal: number) => ({
      code: 200,
      data: {
        timestamp: Date.now(),
        http: { requests_total: requestsTotal, requests_in_flight: 0 },
        mqtt: { messages_received: 50, messages_sent: 30, connection_errors: 0 },
        device: { online: 3, offline: 1 },
        node: { online: 2, offline: 0 },
        data: { points_collected: 5000, points_stored: 4990 },
        ota: { upgrades_total: 0 },
        websocket: { connections_active: 4, messages_total: 200 },
        control: {
          operations_total: 20, active: 2, queued: 1, succeeded: 15, failed: 2,
          unknown: 1, unresolved_unknown: 0, cancelled: 0, outbox_pending: 1,
          outbox_leased: 0, capability_stale_nodes: 0, audit_write_failures: 0,
        },
      },
    })

    /** 手动控制 resolve 时机的 deferred：resolve 之前请求一直「在飞」。 */
    const deferred = <T>() => {
      let resolve!: (value: T) => void
      let reject!: (reason?: unknown) => void
      const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
      return { promise, resolve, reject }
    }

    const loadApi = async () => {
      const { getMetricsSummary } = await import('@/api/monitor')
      return vi.mocked(getMetricsSummary)
    }

    /** 与门禁同口径的 DOM 事实（选择器即门禁将要使用的判据）。 */
    const refreshFacts = (wrapper: ReturnType<typeof mount>) => ({
      refreshingMarker: wrapper.find('[data-test="monitor-refreshing"]').exists(),
      ariaBusy: wrapper.find('.monitor-container').attributes('aria-busy'),
      skeleton: wrapper.find('[data-test="monitor-loading"]').exists(),
      hasDetailPanels: wrapper.find('.detail-panels').exists(),
      statValues: wrapper.findAll('.stat-value').map(n => n.text().replace(/\s+/g, '')),
    })

    it('① 首屏加载中：刷新中标记为真，且首屏骨架仍渲染（F28 行为不变）', async () => {
      const api = await loadApi()
      const first = deferred<ReturnType<typeof OK>>()
      api.mockReturnValueOnce(first.promise)

      const wrapper = mount(Monitor, { global: { stubs } })
      await nextTick()

      const facts = refreshFacts(wrapper)
      expect(facts.refreshingMarker, '首屏请求在飞时必须可机械观测').toBe(true)
      expect(facts.ariaBusy, 'aria-busy 必须表达「忙」').toBe('true')
      // 反向守卫：首屏骨架不得因新增标记而消失（F28 契约）
      expect(facts.skeleton).toBe(true)

      first.resolve(OK(100))
      await flushPromises()
    })

    it('② 首屏加载完成：刷新中标记消失，骨架消失，渲染真实值', async () => {
      const api = await loadApi()
      const first = deferred<ReturnType<typeof OK>>()
      api.mockReturnValueOnce(first.promise)

      const wrapper = mount(Monitor, { global: { stubs } })
      await nextTick()
      expect(refreshFacts(wrapper).refreshingMarker).toBe(true)

      first.resolve(OK(100))
      await flushPromises()

      const facts = refreshFacts(wrapper)
      expect(facts.refreshingMarker, '落定后不得残留「刷新中」').toBe(false)
      expect(facts.ariaBusy).not.toBe('true')
      expect(facts.skeleton).toBe(false)
      expect(facts.statValues[0]).toBe('100')
    })

    it('③ 已加载后再触发刷新：标记重新出现，且不显示骨架（保留陈旧值）', async () => {
      const api = await loadApi()
      const first = deferred<ReturnType<typeof OK>>()
      api.mockReturnValueOnce(first.promise)

      const wrapper = mount(Monitor, { global: { stubs } })
      await nextTick()
      first.resolve(OK(100))
      await flushPromises()
      expect(refreshFacts(wrapper).refreshingMarker).toBe(false)

      // 接口挂起：第二次请求永不落定（deferred 不 resolve）
      const second = deferred<ReturnType<typeof OK>>()
      api.mockReturnValueOnce(second.promise)

      await wrapper.find('.toolbar-actions button').trigger('click')
      await nextTick()

      const facts = refreshFacts(wrapper)
      expect(api).toHaveBeenCalledTimes(2)
      expect(facts.refreshingMarker, '刷新在飞时必须重新置真').toBe(true)
      expect(facts.ariaBusy).toBe('true')
      // 需求 4：刷新中**不**显示骨架/遮罩，保留陈旧值是可接受设计（F28 已裁决）
      expect(facts.skeleton, '刷新中不得退回首屏骨架').toBe(false)
      expect(facts.hasDetailPanels, '陈旧值仍应可见').toBe(true)
      expect(facts.statValues[0], '此刻显示的仍是上一次的真值').toBe('100')

      second.resolve(OK(200))
      await flushPromises()
    })

    it('④ 刷新完成：标记再次消失，数值更新为新鲜值', async () => {
      const api = await loadApi()
      const first = deferred<ReturnType<typeof OK>>()
      api.mockReturnValueOnce(first.promise)

      const wrapper = mount(Monitor, { global: { stubs } })
      await nextTick()
      first.resolve(OK(100))
      await flushPromises()

      const second = deferred<ReturnType<typeof OK>>()
      api.mockReturnValueOnce(second.promise)
      await wrapper.find('.toolbar-actions button').trigger('click')
      await nextTick()
      expect(refreshFacts(wrapper).refreshingMarker).toBe(true)

      second.resolve(OK(200))
      await flushPromises()

      const facts = refreshFacts(wrapper)
      expect(facts.refreshingMarker, '刷新落定后必须复位').toBe(false)
      expect(facts.ariaBusy).not.toBe('true')
      expect(facts.statValues[0], '数值已更新为新鲜值').toBe('200')
    })

    it('失败路径同样复位标记（不得卡在「刷新中」）', async () => {
      const api = await loadApi()
      const first = deferred<ReturnType<typeof OK>>()
      api.mockReturnValueOnce(first.promise)

      const wrapper = mount(Monitor, { global: { stubs } })
      await nextTick()
      first.resolve(OK(100))
      await flushPromises()

      const second = deferred<ReturnType<typeof OK>>()
      api.mockReturnValueOnce(second.promise)
      await wrapper.find('.toolbar-actions button').trigger('click')
      await nextTick()
      expect(refreshFacts(wrapper).refreshingMarker).toBe(true)

      second.reject(new Error('Request failed with status code 500'))
      await flushPromises()

      expect(refreshFacts(wrapper).refreshingMarker).toBe(false)
      expect(wrapper.find('[data-test="monitor-error"]').exists()).toBe(true)
    })

    it('源码守卫：根容器同时绑定 aria-busy 与 data-test 刷新标记', async () => {
      const src = (await import('../Monitor.vue?raw')).default as string
      expect(src).toContain(":aria-busy=\"isRefreshing\"")
      expect(src).toContain("'monitor-refreshing'")
      // 反向守卫：置真必须发生在 fetchMetrics 内（否则任何异步都会点亮标记）
      const fetchBody = src.slice(src.indexOf('const fetchMetrics = async () => {'))
      expect(fetchBody.slice(0, fetchBody.indexOf('try {'))).toContain('isRefreshing.value = true')
    })
  })

  it('restarts polling when the refresh interval changes', async () => {
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval')
    const wrapper = mount(Monitor, { global: { stubs } })
    const callsAfterMount = setIntervalSpy.mock.calls.length

    await wrapper.find('.el-select').trigger('change')

    expect(setIntervalSpy.mock.calls.length).toBeGreaterThan(callsAfterMount)
    setIntervalSpy.mockRestore()
  })
})
