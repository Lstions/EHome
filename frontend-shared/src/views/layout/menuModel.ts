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
 *
 * ⚠️ 本数组的顺序必须与 `NAV_GROUPS` 展开后的顺序**完全一致**，原因：
 * 侧栏 roving tabindex 用 `activeMenuIndex`（按本数组 findIndex）作为 DOM 下标去聚焦，
 * 而分组后 DOM 顺序 = 组顺序 × 组内顺序。两者若不一致，焦点会跳到错误的菜单项上
 * （键盘用户按 Tab 进的不是当前页）。`assertGroupsCoverDestinations()` 会强制校验这点。
 */
export const NAV_DESTINATIONS: readonly NavDestination[] = [
  { path: '/dashboard', title: '仪表盘' },
  { path: '/node', title: '节点' },
  { path: '/edge-device', title: '边缘设备' },
  { path: '/logical-device', title: '逻辑设备' },
  { path: '/channel', title: '通道管理' },
  { path: '/device-configs', title: '配置模板' },
  { path: '/data-sources', title: '数据源' },
  { path: '/data', title: '数据面板' },
  { path: '/alerts', title: '告警规则' },
  { path: '/automation', title: '自动化策略' },
  { path: '/notification-channels', title: '通知通道' },
  { path: '/notification-deliveries', title: '投递审计' },
  { path: '/firmware', title: '固件管理' },
  { path: '/monitor', title: '系统监控' },
] as const

/** 期望的导航路径集合（顺序无关，用于「覆盖」类断言 —— 分组不会改变成员，只会改变层级）。 */
export function expectedNavPaths(): string[] {
  return NAV_DESTINATIONS.map(d => d.path)
}

/**
 * 导航分组（Phase 2.4：14 项平铺 → 5 组）。
 *
 * 为什么分组而不是继续加平铺项：14 项平铺是"每加一个域就追加一行"的必然结果，
 * 一级扫描成本随功能增长而线性上升；分组把一级项压到 5 个，且按**用户任务**而非
 * 按技术域归类（审计 §4.4 D1）。
 *
 * **设计取舍：默认全部展开（default-openeds）**，而不是折叠收起。理由：
 * 折叠会让子项 `display:none` ⇒ 键盘用户必须先聚焦并展开分组才能到达子项，
 * 而侧栏的 roving tabindex 只在叶子项之间移动（`menuItemEls` 只收集 `.el-menu-item`），
 * 分组标题不在该序列里 —— 折叠后子项将**无法通过键盘到达**。
 * 默认展开则：① 全部叶子仍在焦点序列内，键盘可达性不变；
 * ② 分组标题只承担"归类标签 + 可手动收起"的作用，不引入新的键盘必经步骤。
 * 这是"降低一级扫描成本"与"不牺牲可达性"之间的最小风险取舍。
 */
export interface NavGroup {
  title: string
  /** 组内导航目标的 path（必须在 NAV_DESTINATIONS 内）。 */
  paths: string[]
}

export const NAV_GROUPS: readonly NavGroup[] = [
  { title: '总览', paths: ['/dashboard'] },
  { title: '设备与通道', paths: ['/node', '/edge-device', '/logical-device', '/channel', '/device-configs'] },
  { title: '数据', paths: ['/data-sources', '/data'] },
  { title: '自动化与通知', paths: ['/alerts', '/automation', '/notification-channels', '/notification-deliveries'] },
  { title: '运维', paths: ['/firmware', '/monitor'] },
] as const

/** 分组的期望标题（顺序）。 */
export function expectedGroupTitles(): string[] {
  return NAV_GROUPS.map(g => g.title)
}

/**
 * 分组自检：每个导航目标恰好属于一组，且不引用不存在的路径。
 *
 * 放在模型里而不是只写测试：分组与目标集**分处两个数组**，很容易出现
 * 「新增页面忘了加进任何组」（菜单里看不到）或「组里写了拼错的 path」
 * （渲染出空项）这类静默错配。这里让它在开发期就抛出来。
 */
export function assertGroupsCoverDestinations(): void {
  const known = new Set(expectedNavPaths())
  const seen = new Set<string>()
  for (const g of NAV_GROUPS) {
    for (const p of g.paths) {
      if (!known.has(p)) throw new Error(`[menu] 分组「${g.title}」引用了不存在的路径 ${p}`)
      if (seen.has(p)) throw new Error(`[menu] 路径 ${p} 被多个分组重复引用`)
      seen.add(p)
    }
  }
  const missing = [...known].filter(p => !seen.has(p))
  if (missing.length) {
    throw new Error(`[menu] 以下导航目标未归入任何分组（菜单中将不可见）：${missing.join(', ')}`)
  }

  // 顺序一致性（关系到键盘焦点是否正确，见 NAV_DESTINATIONS 注释）。
  const flat = NAV_GROUPS.flatMap(g => g.paths)
  const ordered = expectedNavPathsInOrder()
  if (flat.join('|') !== ordered.join('|')) {
    throw new Error(
      '[menu] NAV_GROUPS 展开后的顺序与 NAV_DESTINATIONS 不一致 —— '
      + '会导致侧栏 roving tabindex 聚焦到错误的菜单项（键盘用户按 Tab 进的不是当前页）。\n'
      + `  分组展开顺序: ${flat.join(' → ')}\n`
      + `  目标数组顺序: ${ordered.join(' → ')}`,
    )
  }
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
