import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { compileStyleAsync, parse } from '@vue/compiler-sfc'
import DeviceConfigList from '@/views/config/DeviceConfigList.vue'
import source from '@/views/config/DeviceConfigList.vue?raw'

/**
 * 样式契约辅助：用 @vue/compiler-sfc 真实编译 <style scoped>（复现构建期的
 * [data-v-*] 选择器改写），再注入 DOM 读 getComputedStyle —— 比字符串包含更接近
 * 浏览器实际会应用的声明。真实几何/可达性由 e2e 门禁在真浏览器里验收。
 */
async function compiledScopedCss(raw: string, filename: string): Promise<string> {
  const { descriptor } = parse(raw, { filename })
  const chunks: string[] = []
  for (const block of descriptor.styles) {
    const res = await compileStyleAsync({
      source: block.content,
      filename,
      id: 'data-v-testscope',
      scoped: Boolean(block.scoped),
    })
    expect(res.errors, JSON.stringify(res.errors)).toEqual([])
    chunks.push(res.code)
  }
  return chunks.join('\n')
}

/**
 * 注入编译产物 + 合成同结构 DOM（<div class="parentClass" data-v-testscope><div class="el-pagination">），
 * 返回内层元素的计算 flex-wrap。
 *
 * happy-dom 实测：只有在元素**插入文档后**读 getComputedStyle 才会做选择器匹配；
 * 若用"回调构造 DOM"或"插入前先读一次"，返回的是空串（不会命中已注入的样式表）。
 * 因此这里固定 DOM 形状、固定读取顺序。反证：把 parentClass 换成不匹配的值时
 * 返回值必须是空串 —— 说明该函数真的在走选择器匹配而非恒返回 wrap。
 */
function measureFlexWrap(css: string, parentClass: string, childClass: string | null = 'el-pagination'): string {
  const style = document.createElement('style')
  style.textContent = css
  document.head.appendChild(style)
  const wrap = document.createElement('div')
  wrap.className = parentClass
  wrap.setAttribute('data-v-testscope', '')
  const target = childClass === null ? wrap : document.createElement('div')
  if (childClass !== null) {
    target.className = childClass
    wrap.appendChild(target)
  }
  document.body.appendChild(wrap)
  const value = getComputedStyle(target).flexWrap
  style.remove()
  wrap.remove()
  return value
}
const { mockGetList } = vi.hoisted(() => ({
  mockGetList: vi.fn(() => Promise.resolve({
    list: [
      {
        id: 1,
        name: 'UART 温湿度模板',
        description: '',
        device_type: 'temp_humidity',
        hardware_type: 'uart',
        config: {},
        is_default: false,
        status: 'active',
        created_at: '',
        updated_at: '',
      },
    ],
    total: 1,
    page: 1,
    page_size: 12,
  })),
}))

vi.mock('@/api/deviceConfig', () => ({
  deviceConfigApi: {
    getList: mockGetList,
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    setDefault: vi.fn(),
  },
}))

vi.mock('element-plus', () => ({
  ElMessage: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
  ElMessageBox: { confirm: vi.fn() },
}))

const stubs = {
  DeviceConfigForm: true,
  EmptyState: true,
  'el-card': { template: '<section class="el-card"><slot /><slot name="header" /></section>' },
  'el-icon': { template: '<i><slot /></i>' },
  'el-input': { template: '<input />' },
  'el-select': { template: '<select><slot /></select>' },
  'el-option': true,
  'el-button': { template: '<button @click="$emit(\'click\')"><slot /></button>' },
  'el-tag': { template: '<span><slot /></span>' },
  'el-dropdown': { template: '<div><slot /><slot name="dropdown" /></div>' },
  'el-dropdown-menu': true,
  'el-dropdown-item': true,
  'el-pagination': true,
  'el-empty': true,
  'el-dialog': { template: '<div><slot /><slot name="footer" /></div>' },
}

describe('DeviceConfigList.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders four statistic cards after loading template statistics', async () => {
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    expect(mockGetList).toHaveBeenCalledWith({
      device_type: undefined,
      hardware_type: undefined,
      page: 1,
      page_size: 12,
    })
    expect(wrapper.findAll('.stats-row .stat-card')).toHaveLength(4)
    expect(wrapper.text()).toContain('模板总数')
    expect(wrapper.text()).toContain('本页启用')
  })

  it('declares a four-column compact grid for mobile statistics', () => {
    expect(source).toContain('@media (max-width: 768px)')
    expect(source).toContain('.stats-row { grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; }')
  })

  // 360px 实测: card__body 内容宽 310px，.filter-bar 可用 270px；三个动作按钮
  // 合计 261px + 2×8px gap = 277px > 270px。.filter-bar / .filter-left 均有 flex-wrap，
  // 缺了 .filter-right 时「导入」被推到内容区左界之外（x=9.95 < 21）且祖先链
  // scrollWidth === clientWidth（真实裁切，非可滚动溢出）。
  it('toolbar action group allows wrapping so no action is clipped on narrow viewports', () => {
    const start = source.indexOf('.filter-right {')
    expect(start).toBeGreaterThan(-1)
    const block = source.slice(start, source.indexOf('}', start) + 1)
    expect(block).toContain('flex-wrap: wrap;')
  })

  it('wrapping the toolbar actions does not drop any of the three actions', async () => {
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    const actions = wrapper.find('.filter-right').findAll('button')
    expect(actions.map(button => button.text().replace(/\s+/g, ''))).toEqual(['导入', '导出', '新建模板'])
  })

  // ── 窄屏下卡片操作区换行（F9 同类遗漏的回归钉子） ─────────────────────────
  //
  // 根因：.card-footer 是 display:flex + justify-content:flex-end + gap:8px 且**无 flex-wrap**。
  // 360px 视口实测：.el-card__body 内容宽 270px，四个操作（预览 69.02 + 克隆 69.02 +
  // 编辑 69.02 + 下拉 39，含 item 间距共 294.05px）放不下，右对齐把「预览」推出左界：
  // x=16.95 < body.x=21（裁 4.05px）；而 body/card/grid/el-main 的
  // scrollWidth === clientWidth（不可回滚）⇒ **真实裁切**，不是可滚动溢出。
  // F9 只补了 .filter-right，这是同一根因漏掉的另一个元素。
  //
  // 为什么是样式契约而不是真实布局断言（规范允许的例外，须标注理由）：
  // happy-dom **无布局引擎** —— getBoundingClientRect() 恒为 0、scrollWidth/clientWidth
  // 恒为 0，写不出"按钮是否落在容器内"的行为断言。故此处退化为**样式契约**，
  // 但用 @vue/compiler-sfc 真实编译 scoped CSS 并注入 DOM 读 computed style
  // （而非字符串包含），真浏览器几何与可达性由 e2e 门禁覆盖。
  describe('窄屏下卡片操作区可换行（样式契约，happy-dom 无布局引擎的例外）', () => {
    it('.card-footer 自身声明 flex-wrap，按钮不再被推出卡片内容区', async () => {
      const css = await compiledScopedCss(source, 'DeviceConfigList.vue')
      const rule = css.match(/\.card-footer\[data-v-[a-z0-9]+\]\s*\{[^}]*\}/)
      expect(rule, 'DeviceConfigList.vue 缺少 .card-footer 的 scoped 规则').not.toBeNull()
      expect(rule![0]).toContain('flex-wrap: wrap')
      // DOM 级复核：注入编译产物后，同选择器命中的元素计算值必须真的是 wrap。
      // .card-footer 是**自身**选择器（不是后代），故 childClass 传 null 直接测元素自身。
      expect(measureFlexWrap(css, 'card-footer', null)).toBe('wrap')
      // 反证：class 不匹配时必须取不到该声明（证明测的是选择器而非恒真）
      expect(measureFlexWrap(css, 'not-the-container')).toBe('')
    })

    it('F9 的 .filter-right 契约不回退', async () => {
      const css = await compiledScopedCss(source, 'DeviceConfigList.vue')
      const rule = css.match(/\.filter-right\[data-v-[a-z0-9]+\]\s*\{[^}]*\}/)
      expect(rule, 'DeviceConfigList.vue 缺少 .filter-right 的 scoped 规则').not.toBeNull()
      expect(rule![0]).toContain('flex-wrap: wrap')
    })
  })
})
