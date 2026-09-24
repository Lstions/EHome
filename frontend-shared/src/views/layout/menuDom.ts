/**
 * 菜单门禁的 DOM 判定工具（Phase 0.3）。
 *
 * 目标：让「导航覆盖 / 键盘可达 / roving tabindex / 焦点陷阱」这些**不变量**在
 * 平铺与分组（el-sub-menu）两种布局下都能判定，而不是依赖「一级项恰为 14 个」这种
 * 与布局耦合的形状断言。
 *
 * 三种布局下 `.el-menu-item` 的可见性差异（必须显式处理，否则会得到假绿/假红）：
 *   ① 平铺：全部 `.el-menu-item` 都在 DOM；
 *   ② 分组 + 折叠：子项**不在 DOM**（EP 的 sub-menu 惰性渲染）⇒ 直接查会漏项，
 *      若断言「数量 == 14」必然失败，若断言「≥1」又会放过覆盖缺失；
 *   ③ 分组 + 展开：子项在 DOM，且`.el-sub-menu__title` 也是可聚焦项。
 *
 * 因此覆盖类判定统一走「先展开所有分组，再收集 data-index」。
 */

/** 收集元素内的导航路径（按 DOM 顺序）。只认带 data-index 的项，避免把 sub-menu 标题算进来。 */
export function collectNavPaths(root: ParentNode): string[] {
  // 双重防护：选择器的 [data-index] 与下面的 filter 各自都足以排除「同类名但非导航项」的节点。
  // 实测（变异自证）：单独移除任一层，MenuGateHelpers.spec 仍全绿 —— 属**纵深防御**而非冗余；
  // 该 spec 因此只钉住"两者同时缺失"这一种真实退化，不逐个钉住每一层。
  // 若要逐层钉住，需要构造只被其中一层拦截的输入，而当前 EP 结构下这种输入不存在。
  return Array.from(root.querySelectorAll<HTMLElement>('.el-menu-item[data-index]'))
    .map(el => el.getAttribute('data-index'))
    .filter((v): v is string => typeof v === 'string' && v.length > 0)
}

/** 是否存在 el-sub-menu（即菜单是否已分组）。 */
export function hasSubMenu(root: ParentNode): boolean {
  return root.querySelector('.el-sub-menu') !== null
}

/**
 * 展开全部 el-sub-menu 并返回展开后的导航路径。
 *
 * 用法（在 spec 里）：
 *   const el = wrapper.find('.sidebar .el-menu').element
 *   await expandAllSubMenus(el)          // 折叠布局需先展开才能看到子项
 *   expect(new Set(collectNavPaths(el))).toEqual(new Set(expectedNavPaths()))
 *
 * 对平铺布局是无副作用的空操作（querySelectorAll 返回空）。
 * 同步触发 click 后需要 `await nextTick()`（调用方负责，因为 nextTick 属于 vue 侧）。
 */
export function expandAllSubMenus(root: ParentNode): void {
  const titles = Array.from(root.querySelectorAll<HTMLElement>('.el-sub-menu__title'))
  for (const title of titles) {
    // 已展开的跳过：重复点击会把已展开的组收起来（EP 标题是 toggle）。
    const subMenu = title.closest('.el-sub-menu')
    if (subMenu?.classList.contains('is-opened')) continue
    title.click()
  }
}
