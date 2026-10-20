import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import tempSource from '../InverterTempCard.vue?raw'
import energySource from '../InverterEnergyCard.vue?raw'
import mpptSource from '../InverterMpptCard.vue?raw'
import InverterTempCard from '../InverterTempCard.vue'
import InverterEnergyCard from '../InverterEnergyCard.vue'
import InverterMpptCard from '../InverterMpptCard.vue'

/**
 * 逆变器三卡「窄屏压缩」布局契约（2026-10-20，生产实测缺陷）。
 *
 * 缺陷（393×851 实测，el-card 高度）：温度与风扇 788px、发电量统计 492px、MPPT 通道 230px。
 * 根因：响应式仍按桌面结构落地 —— 温度被旧 `@media(max-width:480px)` 压成 **1 列**
 * （7 个温度各占一整行）、发电量 auto-fill(200px) 在 345px 卡宽下只剩 1 列（4 项竖排）、
 * MPPT 每个电气量各占一整行。三卡合计 1510px ≈ 1.8 屏。
 *
 * 修法（全部只在 `@media (max-width: 768px)` 内；桌面 >768px 的 CSS 一行未动）：
 *   · 温度：2 列网格，每格「标签 / 数值°C」上下排、min-height 48px；风扇保持「一行一个风扇」；
 *   · 发电量：2×2 网格，每格「图标 + 标签 + 数值 kWh」横排（数值 16px，保证 5~7 位整数 + 单位完整可见）；
 *   · MPPT：名称+状态一行，电压/电流/功率 三列一行。
 *
 * 本 spec 是**源码契约**测试（组件样式写在 SFC scoped CSS 里，happy-dom 不解析 @media），
 * 用 ?raw 断言「窄屏规则存在且被 media 包住」「桌面规则仍在」「不得用 ellipsis 截断数据」，
 * 再用 mount 断言空数据/非数字时 fail-open（不隐藏、不抛错、单位仍在）。
 */

/** 按花括号配对切出唯一的窄屏 media 块；返回块文本与「块外」剩余源码（= 桌面规则区）。 */
export function splitMobileBlock(source: string): { block: string; rest: string; marker: number } {
  const markerText = '@media (max-width: 768px)'
  const marker = source.indexOf(markerText)
  if (marker < 0) return { block: '', rest: source, marker: -1 }
  const open = source.indexOf('{', marker)
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === '{') depth += 1
    else if (source[i] === '}') {
      depth -= 1
      if (depth === 0) {
        return {
          block: source.slice(marker, i + 1),
          rest: source.slice(0, marker) + source.slice(i + 1),
          marker,
        }
      }
    }
  }
  // 花括号不配对属于源码损坏，直接交给断言暴露（不要静默当成空块）
  return { block: source.slice(marker), rest: source.slice(0, marker), marker }
}

const temp = splitMobileBlock(tempSource)
const energy = splitMobileBlock(energySource)
const mppt = splitMobileBlock(mpptSource)

describe('splitMobileBlock 自检（保证后面的断言不是空转）', () => {
  it('能把 media 块与桌面规则切干净，且不改动源码顺序语义', () => {
    const sample = [
      '.a { color: red; }',
      '@media (max-width: 768px) {',
      '  .a { grid-template-columns: repeat(2, minmax(0, 1fr)); }',
      '  .b { min-height: 48px; }',
      '}',
      '.c { color: blue; }',
    ].join('\n')
    const r = splitMobileBlock(sample)
    expect(r.marker).toBeGreaterThan(0)
    expect(r.block).toContain('@media (max-width: 768px)')
    expect(r.block).toContain('min-height: 48px')
    expect(r.block).not.toContain('.c {')
    expect(r.rest).toContain('.a { color: red; }')
    expect(r.rest).toContain('.c { color: blue; }')
    expect(r.rest).not.toContain('min-height: 48px')
  })

  it('没有 media 块时返回原串、marker = -1（fail-closed：后续断言会红而不是假绿）', () => {
    const r = splitMobileBlock('.a { color: red; }')
    expect(r.marker).toBe(-1)
    expect(r.block).toBe('')
    expect(r.rest).toBe('.a { color: red; }')
  })
})

describe('三卡都有唯一的窄屏断点 @media (max-width: 768px)', () => {
  for (const [name, src] of [
    ['InverterTempCard', tempSource],
    ['InverterEnergyCard', energySource],
    ['InverterMpptCard', mpptSource],
  ] as const) {
    it(name + ' 恰有一个 768px 断点，且是 scoped style 的最后一条规则', () => {
      const count = src.split('@media (max-width: 768px)').length - 1
      expect(count, name + ' 应恰有 1 个 768px 断点').toBe(1)
      const parsed = splitMobileBlock(src)
      // 媒体块收尾之后只剩 </style>：保证它位于所有桌面基础规则之后（同优先级下覆盖桌面值）
      const afterBlock = src.slice(parsed.marker + parsed.block.length).trim()
      expect(afterBlock).toBe('</style>')
      // 反向守卫：块外不得再有第二个窄屏/响应式 media 块
      // （注释里可以提到历史 @media(max-width:480px)，但真规则不许出现在块外）
      expect(parsed.rest).not.toContain('@media (max-width')
    })
  }
})

describe('InverterTempCard 窄屏 2 列网格（7 个温度不再各占一行）', () => {
  it('温度网格在窄屏为 2 列、且 min-height 48px（每格标签/数值上下排）', () => {
    expect(temp.block).toMatch(/\.temp-grid\s*\{[^}]*grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/)
    expect(temp.block).toMatch(/\.temp-item\s*\{[^}]*min-height:\s*48px/)
    expect(temp.block).toMatch(/\.temp-label\s*\{/)
    expect(temp.block).toMatch(/\.temp-value\s*\{/)
  })

  it('风扇仍是一行一个风扇（行式布局），不是被压成 2 列网格', () => {
    expect(temp.block).toMatch(/\.fan-grid\s*\{[^}]*grid-template-columns:\s*1fr/)
    expect(temp.block).toMatch(/\.fan-item\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*row/)
    expect(temp.block).toMatch(/\.fan-speed-bar\s*\{[^}]*flex:\s*1 1 auto/)
  })

  it('旧 @media(max-width:480px) 的 1 列规则已并入 768px 块（480 块不得残留）', () => {
    expect(tempSource).not.toContain('max-width: 480px')
    // 反向守卫：1 列温度网格是本次缺陷本身，任何地方都不许再出现（除风扇 fan-grid 的 1fr）
    expect(tempSource).not.toMatch(/\.temp-grid\s*\{[^}]*grid-template-columns:\s*1fr/)
  })

  it('桌面温度网格仍是 auto-fill minmax(140px,1fr)、数值 18px（逐像素契约）', () => {
    expect(temp.rest).toMatch(/\.temp-grid\s*\{[^}]*grid-template-columns:\s*repeat\(auto-fill,\s*minmax\(140px,\s*1fr\)\)/)
    expect(temp.rest).toMatch(/\.temp-value\s*\{\s*font-size:\s*18px/)
    // 移动端 2 列规则不得泄漏到媒体块之外
    expect(temp.rest).not.toMatch(/repeat\(2,\s*minmax\(0,\s*1fr\)\)/)
  })

  it('温度级别配色仍由 temp-normal/warning/danger 承载（不因压缩丢语义色）', () => {
    for (const cls of ['temp-normal', 'temp-warning', 'temp-danger']) {
      expect(tempSource).toContain('.' + cls)
    }
    // 移动端块不重复定义颜色：配色只有一份定义（主题 token）
    expect(temp.block).not.toMatch(/\.temp-(normal|warning|danger)\s*\{[^}]*color:/)
  })

  it('温度数值带单位 °C，非数字值显示 —（fail-open，不隐藏数值位）', () => {
    expect(tempSource).toContain('°C')
    const wrapper = mount(InverterTempCard, { props: { latestData: null } })
    expect(wrapper.find('.el-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('设备未返回温度数据')

    const partial = mount(InverterTempCard, { props: { latestData: { pv_temp: 'abc', max_temp: 35.5 } } })
    const items = partial.findAll('.temp-item')
    expect(items).toHaveLength(2)
    expect(items[0].find('.temp-value').text()).toBe('—')
    expect(items[1].find('.temp-value').text()).toBe('35.5°C')
  })
})

describe('InverterEnergyCard 窄屏 2×2 网格（4 项不再竖排）', () => {
  it('发电量容器在窄屏为 2 列、卡片 min-width 放开（否则 190px 会撑破 2 列）', () => {
    expect(energy.block).toMatch(/\.energy-card-container\s*\{[^}]*grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/)
    expect(energy.block).toMatch(/\.energy-card\s*\{[^}]*min-width:\s*0/)
    expect(energy.block).toMatch(/\.energy-value\s*\{[^}]*font-size:\s*16px/)
  })

  it('桌面仍是 auto-fill minmax(200px,1fr) 且 4 卡横排（逐像素契约）', () => {
    expect(energy.rest).toMatch(/\.energy-card-container\s*\{[^}]*grid-template-columns:\s*repeat\(auto-fill,\s*minmax\(200px,\s*1fr\)\)/)
    expect(energy.rest).toMatch(/\.energy-card\s*\{[^}]*min-width:\s*190px/)
    expect(energy.rest).not.toMatch(/repeat\(2,\s*minmax\(0,\s*1fr\)\)/)
  })

  it('数值带单位 kWh 且模板结构未变（不隐藏、不改竖排为折叠）', () => {
    expect(energySource).toContain('<span class="energy-unit">kWh</span>')
    expect(energySource).toContain('v-for="item in energyItems"')
  })

  it('4 项在空数据/非数字下仍全部可见并显示 —（fail-open）', () => {
    const empty = mount(InverterEnergyCard, { props: { latestData: null } })
    expect(empty.findAll('.energy-card')).toHaveLength(4)
    expect(empty.findAll('.energy-unit')).toHaveLength(4)
    expect(empty.findAll('.energy-unit')[3].text()).toBe('kWh')
    expect(empty.findAll('.energy-value').map((el) => el.text()).filter((t) => t.includes('—'))).toHaveLength(4)

    const nan = mount(InverterEnergyCard, { props: { latestData: { daily_energy: 'abc' } } })
    expect(nan.findAll('.energy-value')[0].text()).toContain('—')
    expect(nan.findAll('.energy-value')[0].text()).not.toContain('NaN')
  })
})

describe('InverterMpptCard 窄屏「名称+状态一行 / 三列一行」', () => {
  it('通道卡：header 一行 + 三个电气量一行三列', () => {
    expect(mppt.block).toMatch(/\.mppt-body\s*\{[^}]*flex-direction:\s*row/)
    expect(mppt.block).toMatch(/\.mppt-metric\s*\{[^}]*flex:\s*1 1 0/)
    expect(mppt.block).toMatch(/\.mppt-metric\s*\{[^}]*flex-direction:\s*column/)
    expect(mppt.block).toMatch(/\.mppt-header\s*\{[^}]*margin-bottom:\s*6px/)
  })

  it('可以收紧本组件自己的内层 el-card 内距（:deep，且非 :has 全局选择器）', () => {
    expect(mppt.block).toMatch(/\.mppt-card\s*:deep\(\.el-card__body\)\s*\{[^}]*padding:\s*8px 10px/)
    expect(mpptSource).not.toContain(':has(')
  })

  it('桌面仍是竖排 3 行 + min-width 200px（逐像素契约）', () => {
    expect(mppt.rest).toMatch(/\.mppt-body\s*\{\s*display:\s*flex;\s*flex-direction:\s*column;\s*gap:\s*8px;\s*\}/)
    expect(mppt.rest).toMatch(/\.mppt-metric\s*\{\s*display:\s*flex;\s*justify-content:\s*space-between/)
    expect(mppt.rest).toMatch(/\.mppt-card\s*\{\s*min-width:\s*200px/)
    expect(mppt.rest).not.toContain('flex-direction: row; gap: 6px')
  })

  it('旧 @media(max-width:480px) 已并入 768px 块', () => {
    expect(mpptSource).not.toContain('max-width: 480px')
  })

  it('每列都带单位（V/A/功率瓦或 kW），空数据时仍有可见空态（fail-open）', () => {
    const wrapper = mount(InverterMpptCard, {
      props: { data: { pv1_voltage: 320.4, pv1_current: 5.25, pv1_power: 1682.1 } },
    })
    const card = wrapper.find('.mppt-card')
    expect(card.findAll('.mppt-label').map((el) => el.text())).toEqual(['电压', '电流', '功率'])
    expect(card.text()).toContain('320.4V')
    expect(card.text()).toContain('5.25A')
    expect(card.text()).toContain('1.68kW')
    expect(card.find('.el-tag').text()).toBe('运行中')

    const empty = mount(InverterMpptCard, { props: { data: null } })
    expect(empty.find('.el-empty').exists()).toBe(true)
    expect(empty.text()).toContain('无MPPT通道数据')
  })
})

describe('三卡一律不得用 ellipsis/行钳截断数值（本次硬规则）', () => {
  for (const [name, src] of [
    ['InverterTempCard', tempSource],
    ['InverterEnergyCard', energySource],
    ['InverterMpptCard', mpptSource],
  ] as const) {
    it(name + ' 无 text-overflow: ellipsis / -webkit-line-clamp', () => {
      expect(src).not.toMatch(/text-overflow:\s*ellipsis/)
      expect(src).not.toMatch(/-webkit-line-clamp/)
    })
  }

  it('发电量数值维持 overflow: visible + text-overflow: clip（宁可溢出也不吞数字）', () => {
    expect(energy.rest).toMatch(/\.energy-value\s*\{[^}]*overflow:\s*visible/)
    expect(energy.rest).toMatch(/\.energy-value\s*\{[^}]*text-overflow:\s*clip/)
  })
})
