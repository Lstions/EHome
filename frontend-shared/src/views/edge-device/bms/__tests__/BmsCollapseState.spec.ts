import { describe, expect, it, beforeEach, vi } from 'vitest'
import { ref, nextTick, h } from 'vue'
import { mount } from '@vue/test-utils'
import { useCollapseState } from '@/composables/useCollapseState'
import CollapseBlock from '@/views/edge-device/bms/CollapseBlock.vue'
import bmsDetailSource from '@/views/edge-device/bms/BmsDetailPage.vue?raw'
import collapseBlockSource from '@/views/edge-device/bms/CollapseBlock.vue?raw'

/**
 * 去掉注释后的页面源码：**否定断言必须基于它**。
 * 页面注释里写着「已移除轮询间隔分布/实时数据流」时，直接对全文 not.toContain
 * 会把「解释为什么删掉」误判成「还在用」（BmsDetailLayout.spec.ts 已有同款教训）。
 * 注意：只剥注释，不剥字符串 —— 用户可见文案里的遗留另见报告（不在此做断言）。
 */
const bmsDetailCode = bmsDetailSource
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/(^|[^:])\/\/[^\n]*/g, '$1')

/**
 * BMS 移动端改版第 5 步：诊断类区块折叠机制（2026-10-20）。
 *
 * 这三条是这一层最容易悄悄坏掉、而且坏了页面「看起来还正常」的东西：
 *  1) 折叠记忆按设备隔离 + 只存展开项 → 坏了表现为「换设备后展开状态串台」或「刷新后全丢」
 *  2) 桌面零包装 → 坏了表现为桌面少几个 flex 项（`.row > .card` 选择器失配），整页布局塌
 *  3) 默认折叠 + 摘要常显 → 坏了表现为诊断区又变回 8 屏长
 */

const STORAGE_KEY = (id: number | string) => 'bms-collapse:' + id

describe('useCollapseState：按设备隔离的折叠记忆', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('默认全部收起（不给默认展开项）；toggle 后只把已展开项写进 localStorage', async () => {
    const deviceId = ref<number | string>(14)
    const collapse = useCollapseState(() => deviceId.value)
    expect(collapse.isOpen('charts')).toBe(false)
    collapse.toggle('charts')
    expect(collapse.isOpen('charts')).toBe(true)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY(14)) || '[]')).toEqual(['charts'])
    collapse.toggle('charts')
    expect(collapse.isOpen('charts')).toBe(false)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY(14)) || '[]')).toEqual([])
  })

  it('换设备后读取该设备自己的记忆（不串台）', async () => {
    const deviceId = ref<number | string>(14)
    const collapse = useCollapseState(() => deviceId.value)
    collapse.toggle('charts')
    deviceId.value = 15
    await nextTick()
    expect(collapse.isOpen('charts')).toBe(false)
    collapse.toggle('history')
    deviceId.value = 14
    await nextTick()
    // 14 号设备仍是「历史趋势展开」，15 号设备的 history 不影响它
    expect(collapse.isOpen('charts')).toBe(true)
    expect(collapse.isOpen('history')).toBe(false)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY(15)) || '[]')).toEqual(['history'])
  })

  it('localStorage 不可用（隐私模式/配额满）时退化为内存态，不抛错也不锁死折叠', async () => {
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota') })
    try {
      const collapse = useCollapseState(() => 14)
      expect(collapse.isOpen('charts')).toBe(false)
      collapse.toggle('charts')
      expect(collapse.isOpen('charts')).toBe(true)
      collapse.toggle('charts')
      expect(collapse.isOpen('charts')).toBe(false)
    } finally {
      getItem.mockRestore()
      setItem.mockRestore()
    }
  })

  it('deviceId 缺失/为 0 时不写脏 key', async () => {
    const collapse = useCollapseState(() => 0)
    collapse.toggle('charts')
    expect(collapse.isOpen('charts')).toBe(true)
    expect(localStorage.getItem('bms-collapse:0')).toBeNull()
  })
})

describe('CollapseBlock：桌面零包装、窄屏折叠头', () => {
  const slot = () => h('div', { class: 'inner-card' }, '内容')

  it('桌面（collapsible=false）不产生任何包装节点 —— 否则父页 `.row > .card` 直接子选择器全部失配', () => {
    const wrapper = mount(CollapseBlock, {
      props: { blockId: 'history', title: '操作历史', collapsible: false, open: false },
      slots: { default: slot },
    })
    // 插槽内容直接成为根片段：DOM 上不多任何一层包装节点
    expect(wrapper.find('.collapse-block').exists()).toBe(false)
    expect(wrapper.find('.collapse-head').exists()).toBe(false)
    expect(wrapper.find('.collapse-body').exists()).toBe(false)
    expect(wrapper.find('.inner-card').exists()).toBe(true)
    expect(wrapper.html()).toBe('<div class="inner-card">内容</div>')
  })

  it('窄屏：标题 + 一行摘要 + aria 状态，收起时正文 display:none 但组件保活', async () => {
    const wrapper = mount(CollapseBlock, {
      props: { blockId: 'charts', title: '历史趋势', summary: '压差 12mV · 总电压 51.76V', collapsible: true, open: false },
      slots: { default: slot },
    })
    const head = wrapper.find('.collapse-head')
    expect(head.exists()).toBe(true)
    expect(head.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('.collapse-title').text()).toBe('历史趋势')
    expect(wrapper.find('.collapse-summary').text()).toContain('压差 12mV')
    // 正文不是 v-if 移除：图表组件要保活（展开时不重新取数/不重新 init）
    expect(wrapper.find('.inner-card').exists()).toBe(true)
    expect((wrapper.find('.collapse-body').element as HTMLElement).style.display).toBe('none')

    await head.trigger('click')
    expect(wrapper.emitted('toggle')).toHaveLength(1)

    await wrapper.setProps({ open: true })
    expect(head.attributes('aria-expanded')).toBe('true')
    expect((wrapper.find('.collapse-body').element as HTMLElement).style.display).toBe('')
  })
})

describe('BmsDetailPage 接入契约（源码层）', () => {
  it('折叠状态按路由 deviceId 记忆（不是写死或全局单例）', () => {
    expect(bmsDetailSource).toContain('useCollapseState(() => deviceId.value)')
  })

  it('诊断类区块走 CollapseBlock 且默认折叠（不传 open 默认值 → collapse.isOpen 初始 false）', () => {
    for (const blockId of ['charts', 'history']) {
      expect(bmsDetailSource).toContain(`block-id="${blockId}"`)
    }
    // 关键反回退：不能写成 :open="true" 之类的强制展开
    expect(bmsDetailSource).not.toMatch(/block-id="charts"[\s\S]{0,200}?open="true"/)
    // 2026-10-21 用户要求删除「实时数据流」⇒ 它的折叠块与记忆键都不得复活
    expect(bmsDetailCode, '「实时数据流」折叠块已按用户要求删除，不得复活').not.toContain('block-id="stream"')
    expect(bmsDetailCode).not.toContain("collapse.isOpen('stream')")
  })

  it('折叠态摘要来自真实数据（压差/总电压、历史条数），不是静态文案', () => {
    expect(bmsDetailSource).toContain('const chartsCollapseSummary = computed(')
    expect(bmsDetailSource).toContain('const historyCollapseSummary = computed(')
    // 实时数据流摘要随区块删除：不得留下悬空的 streamCollapseSummary
    expect(bmsDetailCode, '数据流已删除，streamCollapseSummary 不得复活').not.toContain('streamCollapseSummary')
  })

  it('窄屏隐藏卡片内重复标题（折叠头已经写了区块名），桌面媒体查询外不生效', () => {
    // 操作历史仍在，规则必须保留
    expect(bmsDetailSource).toContain('.history-card > .card-head { display: none; }')
    // .stream-card（实时数据流）已删除：不得留下指向不存在组件的隐藏规则
    expect(bmsDetailCode).not.toMatch(/\.stream-card\b/)
  })
})

describe('新增诊断卡接入（3 张图 + 折叠头摘要；轮询间隔分布已删除）', () => {
  it('3 张诊断卡都挂上了：保护触发历史 / 温度趋势 / SOC 与电流（轮询间隔分布已删除）', () => {
    for (const tag of ['ProtectionHistoryCard', 'TempTrendCard', 'SocCurrentCard']) {
      expect(bmsDetailSource).toContain(`<${tag} :device-id="deviceId" :online="deviceOnline"`)
    }
    // 2026-10-21 用户要求删除「轮询间隔分布」：组件与其父页下发 props（§2.5 的 commands / parent-loading）都不得复活。
    expect(bmsDetailCode, '轮询间隔分布卡已删除，不得复活').not.toContain('<PollIntervalCard')
    expect(bmsDetailCode).not.toContain(':commands="pollTemplates"')
    expect(bmsDetailCode).not.toContain(':parent-loading="pollTemplatesLoading"')
  })

  it('3 张诊断卡默认折叠，折叠 id 与记忆键一一对应（轮询间隔分布已删除）', () => {
    for (const blockId of ['protection-history', 'temp-trend', 'soc-current']) {
      expect(bmsDetailSource).toContain(`block-id="${blockId}"`)
      expect(bmsDetailSource).toContain(`collapse.isOpen('${blockId}')`)
      expect(bmsDetailSource).toContain(`collapse.toggle('${blockId}')`)
    }
    expect(bmsDetailCode, '轮询间隔分布折叠块已删除，不得复活').not.toContain('block-id="poll-interval"')
    expect(bmsDetailCode).not.toContain("collapse.isOpen('poll-interval')")
    expect(bmsDetailCode).not.toContain("collapse.toggle('poll-interval')")
  })

  it('摘要由子组件 emit 出来（父页不重复请求同一份历史数据）', () => {
    expect(bmsDetailSource).toContain('const diagSummary = reactive<Record<string, string>>(')
    for (const key of ['protection', 'temp', 'soc']) {
      expect(bmsDetailSource).toContain(`diagSummary.${key}`)
    }
    // 事件监听必须挂上，否则折叠态只剩标题、没有一行结论
    for (const handler of ['onProtectionSummary', 'onTempSummary', 'onSocSummary']) {
      expect(bmsDetailSource).toContain(`@summary="${handler}"`)
    }
    // 轮询间隔分布已删除：摘要键与监听器都不得留下悬空引用
    expect(bmsDetailCode).not.toContain('diagSummary.poll')
    expect(bmsDetailCode).not.toContain('onPollSummary')
  })

  it('新卡在桌面独占一行（.row > .card 的 basis:auto 撑不满），窄屏折叠块占满一行', () => {
    expect(bmsDetailSource).toContain('.diag-row > .card            { flex: 1 1 100%; }')
    expect(bmsDetailSource).toContain('.diag-row > .collapse-block { flex: 1 1 100%; min-width: 0; }')
  })

  it('新卡在窄屏隐藏卡片头（折叠头已给标题）；组件内容区工具行不受影响', () => {
    expect(bmsDetailSource).toContain('.diag-card > .card-head { display: none; }')
  })

  it('详情页不再自建面包屑（统一用顶部框架面包屑）', () => {
    /*
     * 2026-10-21 用户要求：详情页不应再增加面包屑，改用最上方框架的（MainLayout）。
     * 因此本页侧只守卫一件事：旧 DOM/CSS 不得复活，且不得再向共享组件传 breadcrumb。
     */
    expect(bmsDetailSource).not.toContain('class="crumb-link"')
    expect(bmsDetailSource).not.toMatch(/\.crumb-link\s*\{/)
    expect(bmsDetailSource).not.toContain('headerBreadcrumb')
    expect(bmsDetailSource).not.toContain(':breadcrumb=')
  })

  it('快照兜底失败不直出 axios 原文（timeout of 10000ms exceeded 属技术噪声）', () => {
    expect(bmsDetailSource).toContain('const snapshotErrorText = computed(')
    expect(bmsDetailSource).toMatch(/\{\{ snapshotErrorText \}\}/)
    expect(bmsDetailSource).toMatch(/timeout/i)
  })

  it('折叠块作为 .row 的 flex 子项必须占满整行（实测曾只有 157/321px）', () => {
    expect(collapseBlockSource).toContain('flex: 1 1 100%;')
  })

  it('操作历史卡头窄屏隐藏（折叠头「最近 N 条」与卡内「审计线索 · 最近 N 条」重复）', () => {
    expect(bmsDetailSource).toContain('.history-card > .card-head { display: none; }')
  })
})

/**
 * 2026-10-21 用户要求：BMS 页不再显示「轮询间隔分布」与「实时数据流」两块。
 * 删除本身容易，难的是后续重构又悄悄加回来 —— 故用**结构化标识**
 * （组件标签 / 折叠 id / 状态键 / CSS 类名）做反向契约，而不是中文文案
 * （注释、历史说明、错误提示里的旧词会造成假红；这也是本仓既有的教训）。
 */
describe('已移除区块的反向契约（轮询间隔分布 / 实时数据流）', () => {
  it('BMS 页不得再挂载 PollIntervalCard / RealtimeDataList（组件已移除）', () => {
    expect(bmsDetailCode, '轮询间隔分布卡已按用户要求删除').not.toContain('<PollIntervalCard')
    expect(bmsDetailCode, '实时数据流组件已按用户要求删除').not.toContain('<RealtimeDataList')
  })

  it('数据流/轮询分布相关的组合式 API、折叠与摘要标识不得复活', () => {
    for (const marker of [
      'realtimeDataItems',
      'clearRealtimeData',
      'block-id="stream"',
      "collapse.isOpen('stream')",
      'streamCollapseSummary',
      'block-id="poll-interval"',
      'diagSummary.poll',
      'onPollSummary',
    ]) {
      expect(bmsDetailCode, '已删除标识复活：' + marker).not.toContain(marker)
    }
  })

  it('实时数据流的交互/CSS 标识不得复活（.stream-empty 仍被温度空态复用，故只查 .stream-card）', () => {
    for (const marker of ['跟随最新', '已暂停跟随', '上滚即暂停']) {
      expect(bmsDetailCode, '已删除的交互标识复活：' + marker).not.toContain(marker)
    }
    expect(bmsDetailCode, '.stream-card 规则不得复活').not.toMatch(/\.stream-card\b/)
    expect(bmsDetailCode).not.toMatch(/class="card-title">\s*实时数据流/)
  })

  it('分母守卫：保留的区块与轮询指令行必须仍在（负向断言不是对空文件成立）', () => {
    for (const kept of ['历史趋势', '保护触发历史', '温度趋势', 'SOC 与电流', '轮询指令配置', '受控操作', '操作历史']) {
      expect(bmsDetailSource, '缺少保留区块：' + kept).toContain(kept)
    }
    // 轮询指令区仍保留，但自 2026-10-22 第七轮起改为共享组件 CommandFrequencySection
    // （私有 .poll-interval 行已删除 —— 改用组件标签做分母守卫，负向断言不得对空文件成立）
    expect(bmsDetailSource).toContain('<CommandFrequencySection')
  })
})