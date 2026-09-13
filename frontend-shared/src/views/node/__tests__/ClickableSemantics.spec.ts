import { describe, it, expect } from 'vitest'
import nodeListSource from '@/views/node/NodeList.vue?raw'
import nodeOverviewSource from '@/views/node/NodeOverview.vue?raw'
import edgeDeviceListSource from '@/views/edge-device/EdgeDeviceList.vue?raw'

// ── 本文件覆盖：页内可点击非原生容器补语义（规范 §3.1.3 MUST）──
// 同仓正确范式：components/common/StatCard.vue:2-10
//   role / tabindex / aria-label / @keydown.enter.space
//
// 取舍（主要任务 vs 装饰性）：
//   · 修：卡片整体可点击进详情（NodeList .collector-card）、页内 tab 切换
//     （NodeOverview .tab-item）、点击复制的值（EdgeDeviceList .fact-value.copyable）
//   · 不修：16x16 的 i.el-icon.ph-edit / 13x13 的 copy-icon —— 它们挂在已经
//     可点击的父级语义之内，单独给 13px 图标造 tab 停靠点会让 Tab 序列退化。

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

  describe('装饰性小图标不单独加 tab 停靠点（避免 Tab 序列退化）', () => {
    it('NodeOverview 的 16x16 ph-edit / 13x13 copy-icon 上没有被塞入 tabindex', () => {
      // 这两个图标挂在已可点击的父级语义内（重命名 / 复制）；单独可聚焦会让
      // 每次 Tab 多出无意义的 13px 停靠点，把 Tab 序列拉长。
      const phEdit = nodeOverviewSource.match(/<el-icon[^>]*class="ph-edit"[^>]*>/)
      const copyIcon = nodeOverviewSource.match(/<el-icon[^>]*class="copy-icon"[^>]*>/)
      expect(phEdit).not.toBeNull()
      expect(copyIcon).not.toBeNull()
      expect(phEdit![0]).not.toContain('tabindex')
      expect(copyIcon![0]).not.toContain('tabindex')
    })
  })
})
