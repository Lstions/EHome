import { describe, expect, it } from 'vitest'
import source from '@/views/edge-device/bms/BmsDetailPage.vue?raw'

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
    expect(source, '矮卡片不得被同行高卡拉高（历史踩过 el-row stretch 造大片空白）').toMatch(
      /\.row \{[^}]*align-items:\s*flex-start/,
    )
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
   * 旧版通过 DeviceHeader 提供 返回/同步到HA/编辑/删除；
   * 重设计版改用自建页头，必须把同样的能力补齐 —— 视觉稿（demo）没有这些入口，
   * 直接照搬 demo 会造成静默功能回退。
   */
  it('页头保留设备级操作：同步到 HA / 编辑 / 删除 / 刷新', () => {
    for (const label of ['同步到 HA', '编辑', '删除', '刷新']) {
      expect(source, '页头缺少操作：' + label).toContain(label)
    }
    // 编辑与删除必须真的有对话框承载，不能只是按钮
    expect(source).toContain('editVisible')
    expect(source).toContain('DeviceDeleteDialog')
  })

  it('面包屑「边缘设备管理」是真按钮且可返回列表（不是 cursor:pointer 的假可点）', () => {
    expect(source).toMatch(/<button[^>]*class="crumb-link"[^>]*@click="goBackToList"/)
    expect(source).toContain('router.push(\'/edge-device\')')
  })
})
