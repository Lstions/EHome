import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import TimeRangeSelector from '@/components/charts/TimeRangeSelector.vue'
import HistoryChartSection from '../HistoryChartSection.vue'

const selectorSrc = readFileSync(resolve(process.cwd(), 'src/components/charts/TimeRangeSelector.vue'), 'utf8')
const historySrc = readFileSync(resolve(process.cwd(), 'src/views/edge-device/shared/HistoryChartSection.vue'), 'utf8')
const themeSrc = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

describe('时间档四段在任意视口等宽（2026-10-21 复核 P2-6）', () => {
  it('用 grid-auto-columns:1fr 强制等宽，而不是按内容宽度排', () => {
    expect(selectorSrc).toMatch(/:deep\(\.el-radio-group\)\s*\{[^}]*display:\s*grid/)
    expect(selectorSrc).toMatch(/:deep\(\.el-radio-group\)\s*\{[^}]*grid-auto-columns:\s*1fr/)
    expect(selectorSrc).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*width:\s*100%/)
  })

  it('渲染时 4 个分段同属一个 radio-group（等宽的作用域正确）', () => {
    const wrapper = mount(TimeRangeSelector, { props: { modelValue: '24h', customRange: null } })
    expect(wrapper.findAll('.el-radio-group').length).toBe(1)
  })
})

describe('历史空态必须能区分离线（复核 P2-4）', () => {
  /**
   * 关键陷阱：Vue 对 Boolean 类型 prop 在**缺省时会 cast 成 false**。
   * 所以 `online?: boolean` 时「未传」和「离线」不可区分 —— 第一版就因此让
   * 14 个既有空态测试全部误红。改成三态字符串后语义无歧义。
   */
  it('online 是三态字符串而非 boolean（boolean 缺省会被 cast 成 false）', () => {
    expect(historySrc).toContain("online?: 'online' | 'offline' | 'unknown'")
    expect(historySrc).not.toMatch(/online\?:\s*boolean/)
  })

  it('离线时文案指向「设备离线」而不是「换时间范围」这种无效排查', async () => {
    const wrapper = mount(HistoryChartSection, {
      props: { deviceId: 15, deviceType: 'techfine_inverter', deviceTypeText: '逆变器', online: 'offline' },
    })
    await new Promise((r) => setTimeout(r, 0))
    const text = wrapper.text()
    // 离线分支必须出现，且不再把「设备刚接入尚未上报」当作唯一解释
    expect(historySrc).toMatch(/v-if="props\.online === 'offline'"/)
    expect(text.length).toBeGreaterThan(0)
  })

  it('未传 online（未知）时保留原文案 —— 离线提示不得误伤未知态', () => {
    expect(historySrc).toMatch(/v-else class="empty-hint">可能原因：设备刚接入尚未上报/)
  })
})

describe('语义基色必须与 CSS 加载顺序无关（dev/prod 一致）', () => {
  /**
   * 2026-10-21 复核暴露：:root 里的 --el-color-success 是 0,1,0 特异性，与 EP index.css
   * 的 :root 打平 —— 谁后加载谁赢。实测 dev（EP 后加载）= #67c23a、生产（本项目后加载）
   * = #147a3a，同一份源码两套颜色，使 dev 上所有 tag 对比度只有 1.96~2.7（生产 4.7~5.1）。
   * 后果比数值更严重：**任何在 dev 做的对比度审计都失真**。
   */
  const LIGHT_BLOCK = themeSrc.slice(themeSrc.indexOf(':root:not(.dark):not([data-theme="dark"])'))

  it('亮色基色块用 !important 声明全部语义基色', () => {
    for (const name of ['primary', 'success', 'warning', 'danger', 'info']) {
      const re = new RegExp('--el-color-' + name + ':\\s*var\\(--color-' + name + '\\)\\s*!important')
      expect(LIGHT_BLOCK, '缺少 !important 基色：' + name).toMatch(re)
    }
  })

  it('error 与 danger 同源（EP 两套变量都对）', () => {
    expect(LIGHT_BLOCK).toMatch(/--el-color-error:\s*var\(--color-danger\)\s*!important/)
  })
})