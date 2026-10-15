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

  /**
   * 回归守卫（2026-10-15 实测缺陷）：**取消确认后开关必须保持原位**。
   *
   * 改前用 `:model-value + @change`：点一下 el-switch 立刻把滑块挪过去，
   * 而 model-value 由设备 fet_status 驱动、取消时没变 ⇒ Vue 不重渲染 ⇒
   * 滑块停在错误位置，与旁边「导通/关断」文字自相矛盾（实机 3 次里复现 1 次，
   * 刷新页面才恢复）。改用 :before-change 后，取消会 resolve(false)，
   * 开关按契约保持原位。
   *
   * 断言口径直接钉在**开关视觉**上（is-checked 不变 + 未发出任何写请求），
   * 而不是只看"有没有弹窗"——那样正好会漏掉这个缺陷。
   */
  it('取消确认后开关保持原位，且不得发生任何写入', async () => {
    const wrapper = mountSwitch({ fet_status: 0 }) // 两个都关断
    const sw = wrapper.findAll('.el-switch')[0]
    expect(sw.classes()).not.toContain('is-checked')

    await sw.trigger('click')            // 请求置为导通 → 弹确认
    await new Promise(r => setTimeout(r, 0))
    const confirmBtn = wrapper.findAll('button').find(b => b.text() === '确认')
    expect(confirmBtn, '未出现确认按钮').toBeTruthy()

    // 走「取消」路径
    const cancelBtn = wrapper.findAll('button').find(b => b.text() === '取消')
    expect(cancelBtn, '未出现取消按钮').toBeTruthy()
    await cancelBtn!.trigger('click')
    await new Promise(r => setTimeout(r, 0))

    expect(store.create, '取消后不得发出任何写入').not.toHaveBeenCalled()
    expect(store.confirm, '取消后不得申请确认令牌').not.toHaveBeenCalled()
    expect(
      wrapper.findAll('.el-switch')[0].classes(),
      '取消后开关滑块未回滚 —— 视觉与 fet_status 脱同步（本次修复的缺陷）',
    ).not.toContain('is-checked')
  })

  /**
   * 回归守卫（2026-10-15 实测缺陷）：**不含 fet_status 的帧不得把 MOS 显示翻成关断**。
   *
   * 根因：latestData 是"任意指令的最新一帧"。启用 5 条轮询后实测帧分布为
   *   0x03 读取基本信息 45%（唯一含 fet_status）/ 0xAA 保护次数 32% /
   *   0x05 硬件版本 8% / 0x0F 综合 8% / 0x04 单体 7%
   * ⇒ 约 47% 的时间当前帧没有 fet_status。改前此时回退到 legacy 字段
   *   （真机不发）⇒ false ⇒ 界面在设备实际充电时显示「充电 MOS 关断」，
   *   且开关每几秒自己翻转、并因 hasFetStatus=false 被禁用。
   *
   * 该帧对 MOS **没有信息** ⇒ 必须保留上一次已知值。
   */
  it('帧不含 fet_status 时保留上次已知 MOS 状态（不得翻成关断）', async () => {
    const wrapper = mountSwitch({ fet_status: 3 }) // 两个都导通
    expect(wrapper.findAll('.el-switch').map(s => s.classes().includes('is-checked')))
      .toEqual([true, true])

    // 模拟设备轮询到一条 0xAA（保护次数）帧 —— 不含 fet_status
    await wrapper.setProps({ data: { protection_count: 5 } })
    await new Promise(r => setTimeout(r, 0))

    expect(
      wrapper.findAll('.el-switch').map(s => s.classes().includes('is-checked')),
      '不含 fet_status 的帧把 MOS 翻成了关断 —— 界面会谎报"未充电"',
    ).toEqual([true, true])

    // 且不应因此禁用开关（设备仍在线、fet 值仍然已知）
    for (const sw of wrapper.findAll('.el-switch')) {
      expect(sw.attributes('disabled')).toBeUndefined()
    }

    // 真正上报关断时才跟随
    await wrapper.setProps({ data: { fet_status: 0 } })
    await new Promise(r => setTimeout(r, 0))
    expect(wrapper.findAll('.el-switch').map(s => s.classes().includes('is-checked')))
      .toEqual([false, false])
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
