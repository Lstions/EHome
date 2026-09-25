/**
 * M3 回归钉子：ChannelTerminal 移动端控制区「清空」孤立成行。
 *
 * ── 缺陷与根因（390px 实测，可用宽 302px）─────────────────────────────────
 * 改前 `.terminal-controls` 高 120px（3 行），最后一行只有「清空」：
 *   行需求 = radio 100 + gap8 + 暂停 71.8 + (gap8 + EP `.el-button + .el-button` margin-left 12)
 *            + 导出 65.7 + (gap8 + 12) + 清空 54 = **339.5px** > 302px ⇒ 溢出 37.5px。
 * 两个叠加原因：
 *   ① 间距翻倍：EP 的 `.el-button + .el-button { margin-left: 12px }` 叠在 flex `gap: 8px` 上，
 *      相邻按钮实际间距 20px（冗余 24px）；
 *   ② 兜底仍不够：即便去掉全部 margin，四控件仍需 315.5px > 302px，
 *      故**一行放不下四个**；flex 逐项换行时最后一项（清空）被挤成孤行。
 * 修法：三按钮收进 `.terminal-actions` 并 `flex-wrap: nowrap` ⇒ 换行只发生在**组**这一级，
 * 「清空」不可能再单独成行；同时显式补回 `gap: 8px` + `margin-left: 12px` 保持桌面 20px 间距。
 *
 * ── 为什么本用例停在「结构与级联」层，而不是几何层 ──────────────────────────
 * happy-dom **无布局引擎**（getBoundingClientRect/scrollWidth 恒 0），量不出真实环绕行为。
 * 故本文件的判据是**修复的机制本身**：三按钮同属一个 `flex-wrap: nowrap` 容器 ——
 * 只要这个契约成立，浏览器就不可能把组内单个按钮换到独立一行；
 * 而「真实像素落位」由 Playwright 探针在真 Chromium 里验收（390×844 / 1440×900 各量一次，
 * 数据见任务交付报告）。两者是**互补**的：这里防机制被改回去，那里防机制虽在但像素没变好。
 * CSS 断言用 @vue/compiler-sfc **真实编译** scoped 样式后读 computed style，
 * 不是 `expect(source).toContain(...)` 式的字符串包含（那种写法对"选择器写错/被覆盖"无感）。
 */
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { compileStyleAsync, parse } from '@vue/compiler-sfc'
import source from '../ChannelTerminal.vue?raw'
import ChannelTerminal from '../ChannelTerminal.vue'
import type { Channel } from '@/api/channel'

vi.mock('@/api/channel', () => ({
  channelApi: { getList: vi.fn().mockResolvedValue([]), terminalWrite: vi.fn() },
}))

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: false,
    connect: vi.fn(),
    subscribe: vi.fn(() => () => {}),
    send: vi.fn(),
  }),
}))

vi.mock('@/utils/logger', () => ({
  logger: { error: vi.fn(), info: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

/** 真实编译 <style scoped>（复现构建期 [data-v-*] 改写），比字符串包含更接近浏览器实际级联。 */
async function compiledScopedCss(raw: string, filename: string): Promise<string> {
  const { descriptor } = parse(raw, { filename })
  const chunks: string[] = []
  for (const block of descriptor.styles) {
    const res = await compileStyleAsync({
      source: block.content,
      filename,
      id: 'data-v-m3test',
      scoped: Boolean(block.scoped),
    })
    expect(res.errors, JSON.stringify(res.errors)).toEqual([])
    chunks.push(res.code)
  }
  return chunks.join('\n')
}

/**
 * 注入编译产物 + 合成同结构 DOM，返回目标元素的计算值。
 * happy-dom 实测：元素**插入文档后**读 getComputedStyle 才会做选择器匹配。
 */
function computedOf(css: string, outerClass: string, targetClass: string | null, attr?: string): string {
  const style = document.createElement('style')
  style.textContent = css
  document.head.appendChild(style)
  const wrap = document.createElement('div')
  wrap.className = outerClass
  wrap.setAttribute('data-v-m3test', '')
  const target = targetClass === null ? wrap : document.createElement('div')
  if (targetClass !== null) {
    target.className = targetClass
    target.setAttribute('data-v-m3test', '')
    wrap.appendChild(target)
  }
  document.body.appendChild(wrap)
  const value = attr
    ? getComputedStyle(target).getPropertyValue(attr)
    : getComputedStyle(target).flexWrap
  style.remove()
  wrap.remove()
  return value.trim()
}

const mkChannel = (id: number): Channel => ({
  id,
  node_id: 'TESTNODE001',
  name: `ch-${id}`,
  hardware_type: 'UART' as Channel['hardware_type'],
  hardware_id: 'UART0',
  bus_config: '000000002580',
  config: {},
})

async function mountTerminal() {
  const wrapper = mount(ChannelTerminal, {
    props: { collectorId: 1, channels: [mkChannel(101)] },
    global: { plugins: [createPinia()] },
    attachTo: document.body,
  })
  await flushPromises()
  return wrapper
}

describe('M3 ChannelTerminal 控制区换行（结构与级联契约）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('三个日志操作按钮同属一个 .terminal-actions 容器（换行的原子单位）', async () => {
    const wrapper = await mountTerminal()
    const group = wrapper.find('.terminal-actions')
    expect(group.exists(), '缺少 .terminal-actions 按钮组 —— 三按钮又回到逐项换行，清空会再次孤行').toBe(true)

    // 按 aria-label 精确取按钮（不用文本匹配，避免"看起来像"的误抓）
    const labels = ['暂停终端日志', '导出终端日志', '清空终端日志']
    for (const aria of labels) {
      const btn = group.find(`button[aria-label="${aria}"]`)
      expect(btn.exists(), `${aria} 不在按钮组内（组外按钮仍会被逐项换行）`).toBe(true)
    }
    // 反证：按钮组之外的顶层控件里不得再散落这三个按钮
    const outside = wrapper.findAll('button[aria-label="清空终端日志"]').filter(b => !b.element.closest('.terminal-actions'))
    expect(outside, '「清空」仍在按钮组之外，修复未生效').toHaveLength(0)
    // 组大小守卫：组内恰好 3 个按钮，防止"只包住部分按钮"造成假绿
    expect(group.findAll('button')).toHaveLength(3)
  })

  it('按钮组声明 flex-wrap: nowrap（组内不可能换行 ⇒ 清空不会独占一行）', async () => {
    // 先要 DOM 里真有这个组，否则「CSS 写了 nowrap 但没被任何元素使用」会假绿。
    const wrapper = await mountTerminal()
    const group = wrapper.find('.terminal-actions')
    expect(group.exists(), 'DOM 中没有 .terminal-actions ⇒ 声明的 nowrap 没有作用对象').toBe(true)

    const css = await compiledScopedCss(source, 'ChannelTerminal.vue')
    const rule = css.match(/\.terminal-actions\[data-v-[a-z0-9]+\]\s*\{[^}]*\}/)
    expect(rule, 'ChannelTerminal.vue 缺少 .terminal-actions 的 scoped 规则').not.toBeNull()
    expect(rule![0]).toContain('flex-wrap: nowrap')
    // DOM 级复核：编译产物注入后同选择器命中的元素计算值必须真的是 nowrap
    expect(computedOf(css, 'terminal-actions', null)).toBe('nowrap')
    // 反证：class 不匹配时取不到该声明（证明测的是选择器而非恒真）
    expect(computedOf(css, 'not-the-group', null)).toBe('')
  })

  it('桌面间距保持 20px（gap 8 + margin-left 12），与改前逐像素一致', async () => {
    // 同样要求组真实存在：否则"规则在但没元素用"会假绿。
    const wrapper = await mountTerminal()
    const group = wrapper.find('.terminal-actions')
    expect(group.exists(), 'DOM 中没有 .terminal-actions ⇒ 桌面间距规则没有作用对象').toBe(true)
    // 组内相邻按钮恰好是「暂停/导出/清空」三个（间距规则作用于它们之间）
    expect(group.findAll('button')).toHaveLength(3)

    const css = await compiledScopedCss(source, 'ChannelTerminal.vue')
    // 组内 gap 8px
    const gapRule = css.match(/\.terminal-actions\[data-v-[a-z0-9]+\]\s*\{[^}]*\}/)
    expect(gapRule![0], '按钮组丢了 gap: 8px ⇒ 桌面按钮间距会比改前窄').toContain('gap: 8px')
    // 相邻按钮 margin-left 12px（EP 原有间距，改前由 EP 规则提供）
    // 注意编译后的真实形状：scoped 属性加在**最后一个复合选择器**上
    // （`.terminal-actions .el-button + .el-button[data-v-xxx]`），故正则不能把
    // [data-v-*] 写死在中段 —— 否则会误判"规则缺失"。
    const mlRule = css.match(/\.terminal-actions\b[^{]*\.el-button\s*\+\s*\.el-button[^{]*\{[^}]*\}/)
    expect(mlRule, '缺少 .terminal-actions 内相邻按钮间距规则').not.toBeNull()
    expect(mlRule![0]).toContain('margin-left: 12px')
    // 级联复核：合成 .el-button + .el-button 结构，第二个按钮的计算 margin-left 必须命中 12px
    const style = document.createElement('style')
    style.textContent = css
    document.head.appendChild(style)
    const wrap = document.createElement('div')
    wrap.className = 'terminal-actions'
    wrap.setAttribute('data-v-m3test', '')
    const b1 = document.createElement('button'); b1.className = 'el-button'
    const b2 = document.createElement('button'); b2.className = 'el-button'
    b1.setAttribute('data-v-m3test', ''); b2.setAttribute('data-v-m3test', '')
    wrap.append(b1, b2)
    document.body.appendChild(wrap)
    expect(getComputedStyle(b2).marginLeft).toBe('12px')
    expect(getComputedStyle(wrap).gap || getComputedStyle(wrap).rowGap).toBe('8px')
    style.remove(); wrap.remove()
  })

  it('触控尺寸契约：按钮仍是 size="small" 高密度档（修复未靠缩小控件换空间）', async () => {
    const wrapper = await mountTerminal()
    const group = wrapper.find('.terminal-actions')
    const btns = group.findAll('button')
    expect(btns.length).toBe(3)
    // 修复只动结构与间距，不得靠"收窄/缩小按钮"腾空间
    for (const b of btns) {
      expect(b.classes(), '按钮不再是小尺寸档').toContain('el-button--small')
    }
    // 通道选择器的固定 220px 宽度不得被本修复改动（任务书允许的另一方案；本实现未采用）
    expect(source).toContain('width: 220px;')
  })
})
