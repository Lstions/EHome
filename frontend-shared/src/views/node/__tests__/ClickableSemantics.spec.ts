import { describe, it, expect } from 'vitest'
import nodeListSource from '@/views/node/NodeList.vue?raw'
import nodeOverviewSource from '@/views/node/NodeOverview.vue?raw'
import nodeDetailSource from '@/views/node/NodeDetail.vue?raw'
import edgeDeviceListSource from '@/views/edge-device/EdgeDeviceList.vue?raw'

// ── 本文件覆盖：页内可点击非原生容器补语义（规范 §3.1.3 MUST）──
// 同仓正确范式：components/common/StatCard.vue:2-10
//   role / tabindex / aria-label / @keydown.enter.space
//
// 取舍（主要任务 vs 装饰性）：
//   · 修：卡片整体可点击进详情（NodeList .collector-card）、页内 tab 切换
//     （NodeOverview .tab-item）、点击复制的值（EdgeDeviceList .fact-value.copyable）
//   · 修（I-9 A 类，2026-09-15）：NodeOverview 的 ph-edit / copy-icon / mini-edit
//     与 NodeDetail 的 edit-icon —— 它们不是"父级已可点击"的装饰图标，而是**动作本身**
//     的唯一入口（重命名 / 复制 / 开始编辑）。改前用 <el-icon @click> 承载，键盘完全不可达。
//     现改为真 <button> 承载：自带焦点、Enter/Space 激活、读屏角色，并带行内标识的
//     aria-label（同类图标在一页内重复 N 次，没有名字就无法区分）。

describe('页内可点击容器语义（规范 §3.1.3 MUST）', () => {
  describe('NodeList.vue — .collector-card 卡片整体可点击进详情', () => {
    it('卡片补了 role="button" / tabindex="0" / aria-label', () => {
      const card = nodeListSource.match(/<el-card[\s\S]*?class="collector-card"[\s\S]*?>/)
      expect(card).not.toBeNull()
      const tag = card![0]
      expect(tag).toContain('role="button"')
      expect(tag).toContain('tabindex="0"')
      expect(tag).toMatch(/:aria-label="[^"]*查看节点/)
    })

    it('卡片补了 Enter / Space 键盘激活（与 @click 行为一致）', () => {
      const card = nodeListSource.match(/<el-card[\s\S]*?class="collector-card"[\s\S]*?>/)
      const tag = card![0]
      expect(tag).toContain('@keydown.enter.prevent="goToDetail(node.node_id)"')
      expect(tag).toContain('@keydown.space.prevent="goToDetail(node.node_id)"')
    })

    it('保留原有的鼠标 @click 行为', () => {
      const card = nodeListSource.match(/<el-card[\s\S]*?class="collector-card"[\s\S]*?>/)
      expect(card![0]).toContain('@click="goToDetail(node.node_id)"')
    })

    it('焦点可见环用真实布局盒（outline），不用可能被裁剪的 ::after', () => {
      expect(nodeListSource).toContain('.collector-card:focus-visible')
      const block = nodeListSource.match(/\.collector-card:focus-visible\s*\{[^}]*\}/)
      expect(block).not.toBeNull()
      expect(block![0]).toContain('outline')
      expect(block![0]).not.toContain('::after')
    })
  })

  describe('NodeOverview.vue — .tab-item 页内 tab 切换', () => {
    it('tab 项补了 role="tab" / tabindex="0" / aria-selected', () => {
      const tab = nodeOverviewSource.match(/<div\s+v-for="tab in tabs"[\s\S]*?>/)
      expect(tab).not.toBeNull()
      const tag = tab![0]
      expect(tag).toContain('role="tab"')
      expect(tag).toContain('tabindex="0"')
      expect(tag).toContain(':aria-selected="activeTab === tab.label"')
    })

    it('tab 项补了 Enter / Space 键盘激活', () => {
      const tab = nodeOverviewSource.match(/<div\s+v-for="tab in tabs"[\s\S]*?>/)
      const tag = tab![0]
      expect(tag).toContain('@keydown.enter.prevent="activateTab(tab.label)"')
      expect(tag).toContain('@keydown.space.prevent="activateTab(tab.label)"')
      expect(tag).toContain('@click="activateTab(tab.label)"')
    })

    it('焦点环存在且未引入 ::after 热区', () => {
      expect(nodeOverviewSource).toContain('.tab-item:focus-visible')
    })
  })

  describe('EdgeDeviceList.vue — .fact-value.copyable 点击复制', () => {
    it('复制目标补了 role="button" / tabindex="0" / aria-label', () => {
      const span = edgeDeviceListSource.match(/<span\s+class="fact-value copyable"[\s\S]*?>/)
      expect(span).not.toBeNull()
      const tag = span![0]
      expect(tag).toContain('role="button"')
      expect(tag).toContain('tabindex="0"')
      expect(tag).toMatch(/:aria-label="[^"]*复制所属节点/)
    })

    it('复制目标补了 Enter / Space 键盘激活', () => {
      const span = edgeDeviceListSource.match(/<span\s+class="fact-value copyable"[\s\S]*?>/)
      const tag = span![0]
      expect(tag).toContain('@keydown.enter.prevent=')
      expect(tag).toContain('@keydown.space.prevent=')
      expect(tag).toContain('@click="copyText(')
    })

    it('样式里只有一处 .fact-value.copyable（没有新增重复定义）', () => {
      const matches = edgeDeviceListSource.match(/\.fact-value\.copyable\s*\{/g) || []
      expect(matches).toHaveLength(1)
      expect(edgeDeviceListSource).toContain('.fact-value.copyable:focus-visible')
    })
  })

  describe('I-9 A 类：图标动作入口改为真 <button>（重命名 / 复制 / 开始编辑）', () => {
    // 这些图标曾是 <el-icon @click>（只有鼠标可靠，键盘不可达）。改为 <button> 后，
    // 焦点、Enter/Space 激活、读屏角色由原生按钮提供，无需手写 role/tabindex/keydown。
    // 下面断言：button 承载同一个 handler、保留原 class、可访问名带行内标识。
    const iconButtons: Array<[string, string, string]> = [
      ['NodeOverview.vue ph-edit', nodeOverviewSource, 'ph-edit'],
      ['NodeOverview.vue copy-icon', nodeOverviewSource, 'copy-icon'],
      ['NodeOverview.vue mini-edit(重命名)', nodeOverviewSource, 'mini-edit'],
      ['NodeDetail.vue edit-icon', nodeDetailSource, 'edit-icon'],
    ]

    it.each(iconButtons)('%s 由 <button type="button"> 承载且保留原 class', (name, source, cls) => {
      const tag = source.match(new RegExp('<button[^>]*class="' + cls + '"[^>]*>'))
      expect(tag, name + ' 未找到承载 .' + cls + ' 的 <button>').not.toBeNull()
      expect(tag![0]).toContain('type="button"')
      // 原 class 仍在 button 上（视觉/布局不变），且不再由 <el-icon> 直接挂 @click
      expect(source).not.toMatch(new RegExp('<el-icon[^>]*' + cls + '[^>]*@click'))
    })

    it.each(iconButtons)('%s 的可访问名带行内标识（不是只写「编辑/复制」）', (name, source, cls) => {
      const tag = source.match(new RegExp('<button[^>]*class="' + cls + '"[^>]*>'))
      expect(tag![0], name + ' 缺少 aria-label').toMatch(/aria-label/)
      // 行内标识：模板串里必须引用具体对象（设备名 / 节点 ID / 通道名）
      expect(tag![0]).toMatch(/\$\{(row|node|collector|ch)\b/)
    })

    it('NodeDetail 的通道单元格由 <button> 承载且 aria-label 带设备名', () => {
      const tag = nodeDetailSource.match(/<button[^>]*class="device-channel-cell"[^>]*>/)
      expect(tag).not.toBeNull()
      expect(tag![0]).toContain('type="button"')
      expect(tag![0]).toMatch(/aria-label="`编辑 \$\{row\.name\} 的通道配置`"/)
      expect(nodeDetailSource).not.toMatch(/<div[^>]*class="device-channel-cell"[^>]*@click/)
    })
  })
})
