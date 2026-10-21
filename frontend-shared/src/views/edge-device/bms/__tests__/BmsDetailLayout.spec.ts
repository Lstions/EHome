import { describe, expect, it } from 'vitest'
import source from '@/views/edge-device/bms/BmsDetailPage.vue?raw'

/**
 * 去掉注释后的源码：**否定断言必须基于它**。
 * 本文件既有教训（见「保护明细默认展开」用例）：源码注释里写着「已删除 xxx」时，
 * 直接对全文断言 not.toContain 会把「解释为什么删掉」误判成「还在用」。
 */
const sourceCode = source
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/(^|[^:])\/\/[^\n]*/g, '$1')

/**
 * BMS 详情页布局契约（2026-10-17 重写）。
 *
 * 本页由评审 demo 转正（原 /dev/bms-demo2），替换了 2026-08-17 那版
 * el-row/el-col 硬栅格布局。旧契约测试断言的正是被替换掉的结构
 * （HistoryChartSection 位置、el-col :md=14/10 双列、RealtimeDataList 直挂），
 * 继续保留会让「旧布局」被当成契约 —— 故按新设计重写，而不是删掉。
 *
 * 新版设计契约（对应用户四条明确诉求 + 既定规范）：
 *   ① 保护明细默认展开（无折叠）
 *   ② 流式布局：flex + flex-wrap + flex-basis，矮卡不被高卡拉高（align-items:flex-start）
 *   ③ 实时数据流不抢滚动：跟随模式 + overflow-anchor:none
 *   ④ 轮询指令上限由节点上报（不再硬编码 3 条）
 * 采用 ?raw 源码断言（项目既有模式），锁结构不回归。
 */
describe('BmsDetailPage 布局契约（重设计版）', () => {
  it('用流式 flex 布局，而非 el-row/el-col 硬栅格', () => {
    expect(source).not.toMatch(/<el-col[^>]*:md="1[04]"/)
    expect(source).toContain('class="row')
    expect(source).toMatch(/\.row \{[^}]*display:\s*flex/)
    expect(source).toMatch(/\.row \{[^}]*flex-wrap:\s*wrap/)
    /*
     * 2026-10-20 契约调整：align-items 从"全行 flex-start"改为**分档**。
     *
     * 改前的问题：全行 flex-start 让同排底边参差（实测 MOS 312 vs 保护 279 差 34px、
     * 实时流 461 vs 轮询 446 差 16px）。但**不能**简单改成全行 stretch ——
     * 「受控操作 740px」会把「操作历史 129px」拉成 740px，凭空多 611px 空白，
     * 正是本用例原名警告的坑。定稿：高度相近的并排行 stretch，悬殊行仍 flex-start。
     * 契约必须为两件事同时设防，否则会修一个坏一个。
     */
    expect(source, '基础行仍须 flex-start（防高矮悬殊行被拉出大片空白）').toMatch(
      /\.row \{[^}]*align-items:\s*flex-start/,
    )
    expect(source, '高度相近的并排行须 stretch（消除底边参差）').toMatch(
      /\.row\.mos-protect[^{]*\{[^}]*align-items:\s*stretch/,
    )
    expect(source, '指标卡行须 stretch').toMatch(/\.row\.metrics[^{]*\{[^}]*align-items:\s*stretch/)
  })

  it('诉求①：保护明细默认展开，没有折叠开关', () => {
    // 先剥掉注释再断言：源码里有注释**解释为什么删掉** protectExpanded，
    // 直接对全文断言会把"说明删除原因"误判成"还在用"（本用例第一版就踩了）。
    const code = source
      .replace(/<!--[\s\S]*?-->/g, '')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/(^|[^:])\/\/[^\n]*/g, '$1')
    expect(code, '保护明细不应再有展开/收起状态').not.toContain('protectExpanded')
    expect(code).not.toContain('展开明细')
    expect(code).not.toContain('el-collapse')
    expect(source).toContain('protect-grid')
  })

  it('诉求②：实时数据流不抢滚动（跟随模式 + 回到最新）', () => {
    expect(source).toContain('跟随最新')
    expect(source).toContain('已暂停跟随')
    expect(source).toContain('上滚即暂停')
    expect(source, '必须禁用滚动锚定').toContain('overflow-anchor: none')
  })

  it('诉求③：轮询指令上限由节点上报，不硬编码', () => {
    expect(source).toContain('轮询指令配置')
    expect(source).toContain('max_polling_commands')
    expect(source).toMatch(/已启用/)
  })

  it('滚动交给 MainLayout，本页不自建滚动容器（避免双滚动条）', () => {
    expect(source, '根容器不得再自建 overflow-y:auto 滚动').not.toMatch(
      /\.bms-detail-page \{[^}]*overflow-y:\s*auto/,
    )
    expect(source).not.toMatch(/\.bms-detail-page \{[^}]*height:\s*100%/)
  })

  it('页头/根容器不再带 DEV 水印（已转正）', () => {
    expect(source).not.toContain('dev-watermark')
    expect(source).not.toContain('DEV ·')
  })

  it('必备区块常显：MOS / 保护 / 电芯电压 / 温度 / 实时流 / 轮询 / 受控操作 / 操作历史', () => {
    for (const title of [
      'MOS 直控', '保护状态', '电芯电压分布', '温度探头',
      '实时数据流', '轮询指令配置', '受控操作', '操作历史',
    ]) {
      expect(source, '缺少区块：' + title).toContain(title)
    }
  })

  it('电芯电压分布复用旧页图表组件，不重写一套柱状图', () => {
    expect(source).toContain('BmsCellVoltageChart')
    expect(source).toMatch(/\.cell-chart-host \{[^}]*height:/)
  })

  /**
   * 设备级操作不得因换版而丢失（2026-10-17 转正时确实丢过一次）。
   * 2026-10-21 契约更新：页头改为统一共享组件 DeviceHeader，操作入口**不再由本页渲染按钮**，
   * 而是以事件接到本页既有 handler —— 因此断言从「源码里出现操作文字」改为
   * 「DeviceHeader 接入点齐全 + 指向的 handler/弹窗真实存在」。
   * 否则「统一组件」过程中把设备操作接丢，会变成静默功能回退而且测试全绿。
   */
  it('页头接入统一 DeviceHeader，设备级操作经事件连回本页既有 handler', () => {
    expect(source).toContain('<DeviceHeader')
    expect(source).toMatch(/<DeviceHeader[\s\S]*?:builtin-dialogs="false"/)
    // 五类操作事件缺一不可（返回 / 同步HA / 刷新 / 编辑 / 删除）
    for (const evt of [
      '@back="goBackToList"',
      '@sync-to-h-a="onSyncToHA"',
      '@refresh="refreshAll"',
      '@edit="onEditDevice"',
      '@delete="onDeleteDevice"',
    ]) {
      expect(source, 'DeviceHeader 缺少事件接入：' + evt).toContain(evt)
    }
    // 事件指向的 handler 必须真实存在（不是接到空函数）
    for (const fn of [
      'function goBackToList(',
      'async function onSyncToHA(',
      'async function refreshAll(',
      'function onEditDevice(',
      'function onDeleteDevice(',
    ]) {
      expect(source, '缺少 handler：' + fn).toContain(fn)
    }
    // 编辑与删除必须真的有对话框承载；共享组件内置弹窗关闭（BMS 用自己的删除弹窗选数据处理方式）
    expect(source).toContain('editVisible')
    expect(source).toContain('DeviceDeleteDialog')
  })

  it('页头不再自建 <header class="page-head">，面包屑数据交给共享组件渲染', () => {
    // 统一组件后本页不许再维护页头 DOM/CSS（否则两套页头并存、样式互相顶）
    // 用去注释源码断言：模板注释里就写着「不再自建 <header class="page-head">」
    expect(sourceCode).not.toContain('class="page-head"')
    expect(sourceCode).not.toMatch(/\.page-head\s*\{/)
    expect(sourceCode).not.toContain('class="crumb-link"')
    expect(sourceCode).not.toContain('onHeadMoreCommand')
    // 面包屑首段必须是「真实可回列表」的数据（to 交给 DeviceHeader 用 router.push 承载）
    expect(source).toContain(':breadcrumb="headerBreadcrumb"')
    expect(source).toMatch(/headerBreadcrumb[\s\S]{0,200}?text:\s*'边缘设备管理'[\s\S]{0,80}?to:\s*'\/edge-device'/)
    expect(source).toContain("router.push('/edge-device')")
  })
})
