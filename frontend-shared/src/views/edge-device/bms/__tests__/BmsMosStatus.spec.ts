import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import source from '../BmsMosStatus.vue?raw'
import BmsMosStatus from '../BmsMosStatus.vue'

/**
 * F17：BMS MOS 状态图标配色必须走语义 token（规范 §3.6.2 —— 色值取自 token 层）。
 *
 * 此前用 THEME_COLORS.success/info 的十六进制亮色常量，
 * 暗色主题下图标颜色不变（不响应主题）。
 *
 * 分工说明：happy-dom（vitest 环境）**不做** var() 替换，
 * 因此这一层能给出的最强断言是「传给 el-icon 的 :color 确实是 var(--color-*) 引用」；
 * 「该 token 在亮/暗下确实取不同值」由 src/styles/__tests__/SidebarThemeTokens.spec.ts
 * 的 token 解析断言与 .tmp-probe 下的真实浏览器探针覆盖。
 * 全局 el-icon stub（src/test-setup.ts）把 color 透传到 style 上。
 */
describe('BmsMosStatus 语义 token 配色', () => {
  const mountWith = (data: Record<string, number> | null) =>
    mount(BmsMosStatus, { props: { data }, global: { plugins: [createPinia()] } })

  it('充电/放电 MOS 导通时图标用 var(--color-success)，断开时用 var(--color-info)', () => {
    const both = mountWith({ fet_status: 3 })
    const icons = both.findAll('.el-icon')
    expect(icons.length).toBeGreaterThanOrEqual(2)
    expect(icons[0].attributes('style')).toContain('var(--color-success)')
    expect(icons[1].attributes('style')).toContain('var(--color-success)')

    const off = mountWith({ fet_status: 0 })
    const offIcons = off.findAll('.el-icon')
    expect(offIcons[0].attributes('style')).toContain('var(--color-info)')
    expect(offIcons[1].attributes('style')).toContain('var(--color-info)')
  })

  it('无 FET 状态数据时提示图标用 var(--color-info)', () => {
    const wrapper = mountWith(null)
    const icons = wrapper.findAll('.el-icon')
    expect(icons.length).toBeGreaterThan(0)
    expect(icons[icons.length - 1].attributes('style')).toContain('var(--color-info)')
  })

  it('所有图标配色都是 var(--color-*) 引用，不含任何硬编码色值（防回退守卫）', () => {
    for (const data of [{ fet_status: 3 }, { fet_status: 0 }, null]) {
      const wrapper = mountWith(data)
      const icons = wrapper.findAll('.el-icon')
      expect(icons.length, 'stub 未渲染出图标，断言会假绿').toBeGreaterThan(0)
      for (const icon of icons) {
        const style = icon.attributes('style') || ''
        expect(style, '图标缺少 color').toMatch(/color:\s*var\(--color-[a-z]+\)/)
        expect(style, '图标配色含硬编码色值：' + style).not.toMatch(/#[0-9a-fA-F]{3,8}|rgb\(/)
      }
    }
  })
})

/**
 * 2026-10-15（用户诉求）：MOS 从"只读展示 + 去受控操作里找"改为**页面内直接开关**。
 *
 * 本组测试钉死两件事：
 *  ① **极性**（最容易错、且历史上真错过）：
 *     协议参数是 charge_software_closed / discharge_software_closed ——「软件关闭位」，
 *     与 fet_status 的「导通 = 1」**反相**。把开关切到"导通"必须提交 false。
 *     历史事故：模拟器把 E1 写入字节直接当 fet_status 存，回读恒不匹配。
 *  ② 高风险动作仍走确认（后端强制单次令牌，前端不能绕过）。
 */
describe('BmsMosStatus 直接开关（极性 + 确认）', () => {
  let store: ReturnType<typeof useDeviceOperationStore>
  // ⚠ 必须把**同一个** pinia 实例交给挂载点。初次写成了 mount 里再 createPinia()，
  // 于是组件解析到另一个 store 实例，桩函数没被调用、断言假失败。
  let pinia: ReturnType<typeof createPinia>

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    store = useDeviceOperationStore(pinia)
    store.confirm = vi.fn().mockResolvedValue({ token: 'tok-1', expires_at: '' }) as never
    store.create = vi.fn().mockResolvedValue(undefined) as never
  })

  const mountSwitch = (data: Record<string, number>, offline = false) =>
    mount(BmsMosStatus, {
      props: { data, deviceId: 1, offline },
      global: { plugins: [pinia] },
    })

  it('fet_status 位语义：bit0=充电、bit1=放电，导通为 1', () => {
    expect(mountSwitch({ fet_status: 1 }).findAll('.el-switch')[0].classes()).toContain('is-checked')
    expect(mountSwitch({ fet_status: 1 }).findAll('.el-switch')[1].classes()).not.toContain('is-checked')
    expect(mountSwitch({ fet_status: 2 }).findAll('.el-switch')[1].classes()).toContain('is-checked')
  })

  it('把"关断"的充电 MOS 切为导通 ⇒ 提交 charge_software_closed=false（反相，不得写反）', async () => {
    const wrapper = mountSwitch({ fet_status: 0 }) // 两个 MOS 都关断
    await wrapper.findAll('.el-switch')[0].trigger('click') // 请求把充电 MOS 置为导通
    // 高风险动作必须先弹确认；确认后才会真正派发
    const confirmBtn = wrapper.findAll('button').find(b => b.text() === '确认')
    expect(confirmBtn, '未出现确认按钮：高风险动作确认环节丢失').toBeTruthy()
    await confirmBtn!.trigger('click')
    await new Promise(r => setTimeout(r, 0))

    expect(store.create).toHaveBeenCalled()
    const params = (store.create as unknown as { mock: { calls: unknown[][] } }).mock.calls[0][2] as Record<string, unknown>
    // 核心断言：导通 ⇒ 软件关闭位 = false
    expect(params.charge_software_closed, '极性写反：导通应提交 software_closed=false').toBe(false)
    // 未操作的放电 MOS 保持其当前实际状态（关断 ⇒ true）
    expect(params.discharge_software_closed).toBe(true)
    expect(params.priority).toBe('user')
  })

  it('把"导通"的放电 MOS 切为关断 ⇒ 提交 discharge_software_closed=true', async () => {
    const wrapper = mountSwitch({ fet_status: 3 }) // 两个都导通
    await wrapper.findAll('.el-switch')[1].trigger('click')
    const confirmBtn = wrapper.findAll('button').find(b => b.text() === '确认')
    await confirmBtn!.trigger('click')
    await new Promise(r => setTimeout(r, 0))

    const params = (store.create as unknown as { mock: { calls: unknown[][] } }).mock.calls[0][2] as Record<string, unknown>
    expect(params.discharge_software_closed, '关断应提交 software_closed=true').toBe(true)
    expect(params.charge_software_closed, '未操作的充电 MOS 应保持导通 ⇒ false').toBe(false)
  })

  it('离线时开关禁用（不给"按得动但发不出去"的按钮）', () => {
    const wrapper = mountSwitch({ fet_status: 3 }, true)
    for (const sw of wrapper.findAll('.el-switch')) {
      expect(sw.attributes('disabled')).toBeDefined()
    }
  })

  it('源码守卫：极性只在 swClosed() 里做，模板/参数不得内联取反', () => {
    expect(source).toContain('function swClosed(')
    // 参数构造必须经 swClosed；出现 "charge_software_closed: !" 即为内联取反
    expect(source).not.toMatch(/charge_software_closed:\s*!/)
    expect(source).not.toMatch(/discharge_software_closed:\s*!/)
  })
})
