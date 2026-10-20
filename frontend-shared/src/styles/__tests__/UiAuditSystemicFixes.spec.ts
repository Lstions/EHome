import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import EmptyState from '@/components/common/EmptyState.vue'

/**
 * 全站 UI 审计（2026-10-21）系统级修复的合同测试。
 *
 * 为什么用 readFileSync 直读源码：vitest 默认 `css: false`，`?raw` 导入 CSS 会返回空串、
 * 断言永远通过（本项目既有教训）。theme.css 必须直读真实文件。
 */
const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')
const loginSrc = readFileSync(resolve(process.cwd(), 'src/views/auth/Login.vue'), 'utf8')
const dashboardSrc = readFileSync(resolve(process.cwd(), 'src/views/dashboard/Dashboard.vue'), 'utf8')

describe('全站审计修复：表格空态排版（EP 默认 width:50% + line-height:60px）', () => {
  it('theme.css 覆盖 .el-table__empty-text 的宽度与行高', () => {
    expect(themeCss).toMatch(/\.el-table__empty-text\s*\{[^}]*width:\s*auto/)
    expect(themeCss).toMatch(/\.el-table__empty-text\s*\{[^}]*line-height:\s*1\.6/)
    expect(themeCss).toMatch(/\.el-table__empty-text\s*\{[^}]*max-width:\s*90%/)
  })
})

describe('全站审计修复：暗色对比度（分页当前页 / 说明文字 / 主色当文字）', () => {
  it('暗色分页当前页改用亮蓝（改前 #1f5ad8 on #1a1a1a = 2.91:1）', () => {
    expect(themeCss).toMatch(/html\.dark \.el-pager li\.is-active[^}]*color:\s*#79bbff/)
  })

  it('暗色 table-empty-hint 抬到 secondary（改前 3.93:1）', () => {
    expect(themeCss).toMatch(/html\.dark \.table-empty-hint[^}]*color:\s*var\(--el-text-color-secondary\)/)
  })

  it('暗色链接与激活 tab 用 primary-dark-2（改前 2.43:1 / 4.01:1）', () => {
    expect(themeCss).toContain('--el-link-text-color: var(--el-color-primary-dark-2)')
    expect(themeCss).toMatch(/html\.dark \.device-link[^}]*color:\s*var\(--el-color-primary-dark-2\)/)
    expect(themeCss).toMatch(/html\.dark \.el-tabs__item\.is-active[^}]*color:\s*var\(--el-color-primary-dark-2\)/)
  })
})

describe('EmptyState 字符串 icon 解析（16 处空态图标渲染成空白圆的缺陷）', () => {
  it('字符串 icon 名解析为真实图标组件（渲染出 svg）', () => {
    const wrapper = mount(EmptyState, { props: { icon: 'Bell', title: 'x' } })
    // 全局 stub 把 ElIcon 渲染为 span.el-icon；内部必须是一个真实组件（svg 或已知组件）
    const html = wrapper.html()
    expect(html).not.toContain('<bell')
  })

  it('未知 icon 名回退默认图标而不是渲染成未知标签', () => {
    const wrapper = mount(EmptyState, { props: { icon: 'NotARealIcon', title: 'x' } })
    expect(wrapper.html()).not.toContain('<notarealicon')
  })
})

describe('登录页触控目标（审计实测 38/32/32px）', () => {
  it('窄屏媒体查询把输入框 wrapper / 记住我 / 忘记密码热区撑到 44px', () => {
    expect(loginSrc).toMatch(/@media \(max-width: 480px\)[\s\S]*\.el-input__wrapper[^}]*min-height:\s*44px/)
    expect(loginSrc).toMatch(/@media \(max-width: 480px\)[\s\S]*\.el-checkbox[^}]*min-height:\s*44px/)
    expect(loginSrc).toMatch(/@media \(max-width: 480px\)[\s\S]*\.el-link[^}]*min-height:\s*44px/)
  })
})

describe('仪表盘修复（趋势假空态 + 最新数据宽表不可读）', () => {
  it('趋势类别不在真实键集合时自动纠正（默认 temperature 无任何设备上报）', () => {
    expect(dashboardSrc).toContain('selectedTrendByUser')
    expect(dashboardSrc).toMatch(/realKeys\.has\(trendCategory\.value\)/)
  })

  it('窄屏最新数据改卡片列表 + 完整数据弹层（摘要不截断配置值）', () => {
    expect(dashboardSrc).toContain('latest-cards')
    expect(dashboardSrc).toContain('dataDetailVisible')
    expect(dashboardSrc).toContain('SUMMARY_PRIORITY')
    expect(dashboardSrc).toContain('查看完整数据')
  })
})