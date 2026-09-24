/**
 * 侧栏导航的唯一真值源（Phase 0.3 门禁改造的产物）。
 *
 * 为什么需要它：改前「菜单有哪些项」这件事**只存在于 MainLayout.vue 的一个数组字面量里**，
 * 而门禁把它的规模硬编码成 14，并逐项断言 DOM 顺序：
 *
 *   MainLayout.spec.ts:134      expect(menuItems).toHaveLength(14)
 *   MainLayoutKeyboardNav:132   expect(items.length).toBe(14)
 *   e2e/helpers/uiux-metrics.ts sidebarMenuItems = querySelectorAll('.sidebar .el-menu-item').length
 *
 * 于是 Phase 2 一旦把 14 项平铺改成 5 组折叠（el-sub-menu），这些断言会**整批变红**，
 * 而且红的原因无法区分「真回归」与「布局改了」——这正是审计里 H3 的风险。
 *
 * 改造口径：**规模与覆盖从模型派生，可访问性与键盘不变量从 DOM 判定。**
 * - 模型（本文件）：有哪些导航目标 —— 与布局形态无关，平铺/分组都成立；
 * - DOM（各 spec）：这些目标的 tabindex / aria-current / 键盘可达性 —— 两种布局都必须满足。
 *
 * 分组后 children 是 `el-sub-menu` 内的 `.el-menu-item`（折叠时不在 DOM），
 * 故 DOM 侧断言必须先展开或改为「集合包含」而非「数量等于 + 顺序一致」。
 */

export interface NavDestination {
  /** 路由路径，同时是 el-menu 的 index / data-index（门禁与 e2e 都按它定位）。 */
  path: string
  title: string
}

/**
 * 全部导航目标（顺序即期望的阅读顺序）。
 *
 * 注意：这是**导航目标全集**，不是「一级菜单项数」。分组后一级项会少于它，
 * 故任何「一级菜单项 == N」的断言都不得再引用本数组长度。
 */
export const NAV_DESTINATIONS: readonly NavDestination[] = [
  { path: '/dashboard', title: '仪表盘' },
  { path: '/node', title: '节点' },
  { path: '/edge-device', title: '边缘设备' },
  { path: '/logical-device', title: '逻辑设备' },
  { path: '/data-sources', title: '数据源' },
  { path: '/channel', title: '通道管理' },
  { path: '/data', title: '数据面板' },
  { path: '/firmware', title: '固件管理' },
  { path: '/device-configs', title: '配置模板' },
  { path: '/monitor', title: '系统监控' },
  { path: '/alerts', title: '告警规则' },
  { path: '/automation', title: '自动化策略' },
  { path: '/notification-channels', title: '通知通道' },
  { path: '/notification-deliveries', title: '投递审计' },
] as const

/** 期望的导航路径集合（顺序无关，用于「覆盖」类断言 —— 分组不会改变成员，只会改变层级）。 */
export function expectedNavPaths(): string[] {
  return NAV_DESTINATIONS.map(d => d.path)
}

/**
 * 期望的导航路径**有序**列表。
 *
 * 平铺布局下 DOM 顺序必须与它一致；分组布局下 DOM 顺序 = 组顺序 × 组内顺序，
 * 与它可能不同 —— 故只有平铺布局的断言可以用它做 `toEqual`，
 * 分组后应改用 `expectedNavPaths()` 做集合比较。
 */
export function expectedNavPathsInOrder(): string[] {
  return expectedNavPaths()
}
