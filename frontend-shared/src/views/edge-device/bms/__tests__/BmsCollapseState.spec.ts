import { describe, expect, it, beforeEach, vi } from 'vitest'
import { ref, nextTick, h } from 'vue'
import { mount } from '@vue/test-utils'
import { useCollapseState } from '@/composables/useCollapseState'
import CollapseBlock from '@/views/edge-device/bms/CollapseBlock.vue'
import bmsDetailSource from '@/views/edge-device/bms/BmsDetailPage.vue?raw'
import collapseBlockSource from '@/views/edge-device/bms/CollapseBlock.vue?raw'

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
    collapse.toggle('stream')
    deviceId.value = 14
    await nextTick()
    // 14 号设备仍是「历史趋势展开」，15 号设备的 stream 不影响它
    expect(collapse.isOpen('charts')).toBe(true)
    expect(collapse.isOpen('stream')).toBe(false)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY(15)) || '[]')).toEqual(['stream'])
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
    for (const blockId of ['charts', 'stream', 'history']) {
      expect(bmsDetailSource).toContain(`block-id="${blockId}"`)
    }
    // 关键反回退：不能写成 :open="true" 之类的强制展开
    expect(bmsDetailSource).not.toMatch(/block-id="charts"[\s\S]{0,200}?open="true"/)
  })

  it('折叠态摘要来自真实数据（压差/总电压、实时条数、历史条数），不是静态文案', () => {
    expect(bmsDetailSource).toContain('const chartsCollapseSummary = computed(')
    expect(bmsDetailSource).toContain('const streamCollapseSummary = computed(')
    expect(bmsDetailSource).toContain('const historyCollapseSummary = computed(')
  })

  it('窄屏隐藏卡片内重复标题（折叠头已经写了区块名），桌面媒体查询外不生效', () => {
    expect(bmsDetailSource).toMatch(/\.stream-card > \.card-head > \.card-title,[\s\S]{0,120}?\.history-card > \.card-head > \.card-title \{ display: none; \}/)
  })
})

describe('新增诊断卡接入（4 张图 + 折叠头摘要）', () => {
  it('4 张新卡都挂上了：保护触发历史 / 温度趋势 / SOC 与电流 / 轮询间隔分布', () => {
    for (const tag of ['ProtectionHistoryCard', 'TempTrendCard', 'SocCurrentCard']) {
      expect(bmsDetailSource).toContain(`<${tag} :device-id="deviceId" :online="deviceOnline"`)
    }
    // 2026-10-20 契约 §2.5：轮询间隔分布卡改为「父页下发 commands」以消除 commands ×2（R7），
    // 模板变成多行并带 commands / parent-loading 两个新 props。
    expect(bmsDetailSource).toContain('<PollIntervalCard')
    expect(bmsDetailSource).toContain(':commands="pollTemplates"')
    expect(bmsDetailSource).toContain(':parent-loading="pollTemplatesLoading"')
  })

  it('4 张新卡默认折叠，折叠 id 与记忆键一一对应', () => {
    for (const blockId of ['protection-history', 'temp-trend', 'soc-current', 'poll-interval']) {
      expect(bmsDetailSource).toContain(`block-id="${blockId}"`)
      expect(bmsDetailSource).toContain(`collapse.isOpen('${blockId}')`)
      expect(bmsDetailSource).toContain(`collapse.toggle('${blockId}')`)
    }
  })

  it('摘要由子组件 emit 出来（父页不重复请求同一份历史数据）', () => {
    expect(bmsDetailSource).toContain('const diagSummary = reactive<Record<string, string>>(')
    for (const key of ['protection', 'temp', 'soc', 'poll']) {
      expect(bmsDetailSource).toContain(`diagSummary.${key}`)
    }
    // 事件监听必须挂上，否则折叠态只剩标题、没有一行结论
    for (const handler of ['onProtectionSummary', 'onTempSummary', 'onSocSummary', 'onPollSummary']) {
      expect(bmsDetailSource).toContain(`@summary="${handler}"`)
    }
  })

  it('新卡在桌面独占一行（.row > .card 的 basis:auto 撑不满），窄屏折叠块占满一行', () => {
    expect(bmsDetailSource).toContain('.diag-row > .card            { flex: 1 1 100%; }')
    expect(bmsDetailSource).toContain('.diag-row > .collapse-block { flex: 1 1 100%; min-width: 0; }')
  })

  it('新卡在窄屏隐藏卡片头（折叠头已给标题）；组件内容区工具行不受影响', () => {
    expect(bmsDetailSource).toContain('.diag-card > .card-head { display: none; }')
  })

  it('面包屑随页头统一交给 DeviceHeader（UA 清零断言已迁到 DeviceHeader.spec.ts）', () => {
    /*
     * 2026-10-21 契约迁移：BMS 页不再自建面包屑按钮，旧 .crumb-link 的
     * 「appearance:none / font:inherit / 无 ellipsis」断言迁到真正渲染它的
     * DeviceHeader 上（见 shared/__tests__/DeviceHeader.spec.ts 的「面包屑」用例）。
     * 这里只守卫本页侧的两件事：旧 DOM/CSS 不得复活、面包屑数据必须继续传下去。
     */
    expect(bmsDetailSource).not.toContain('class="crumb-link"')
    expect(bmsDetailSource).not.toMatch(/\.crumb-link\s*\{/)
    // 数据必须传给共享组件，否则面包屑会静默消失
    expect(bmsDetailSource).toContain('"headerBreadcrumb"')
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