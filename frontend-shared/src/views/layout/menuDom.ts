/**
 * 菜单门禁的 DOM 判定工具（Phase 0.3）。
 *
 * 目标：让「导航覆盖 / 键盘可达 / roving tabindex / 焦点陷阱」这些**不变量**在
 * 平铺与分组（el-sub-menu）两种布局下都能判定，而不是依赖「一级项恰为 14 个」这种
 * 与布局耦合的形状断言。
 *
 * ⚠️ 关于「折叠时子项是否在 DOM」——**实测结论（EP 2.14.3，勿凭直觉）**：
 *   `element-plus/.../menu/src/sub-menu.mjs:253` 用 `[[vShow, opened.value]]` 渲染子级 `<ul>`，
 *   即 **v-show 而非 v-if** ⇒ 折叠态子项**仍在 DOM**，只是 `display:none`。
 *   用真实组件挂载实测：折叠时 `.el-menu-item` 仍为 3 个（未展开也数得到）。
 *
 * 由此得到两条与直觉相反的结论，二者都影响门禁写法：
 *   ① `collectNavPaths` **不需要**先展开就能拿到全量 —— 展开只影响**可见性**，不影响存在性；
 *   ② 但「在 DOM」≠「可达」：折叠子项 `offsetParent === null`、`display:none`，
 *      用户看不到也点不到 ⇒ 覆盖类断言若只看"存在"会**高估可达性**。
 *
 * 因此本模块保留 `expandAllSubMenus`：它不是"让子项出现"（它们本就在），
 * 而是让后续基于**可见性**的断言（如触控热区、可见文本）成立。
 * 存在性断言与可见性断言必须分开写，混用会得到假绿。
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

/**
 * 元素是否可见（自身或任一祖先被 display:none / visibility:hidden 即不可见）。
 *
 * 为什么不能只用 `offsetParent`（实测两个环境的差异，写错了会一边假绿一边假红）：
 *   - 真实浏览器：隐藏元素的 `offsetParent === null` —— 可判；
 *   - happy-dom（vitest 环境）：`offsetParent` 恒为 **undefined**（不可用），
 *     且**不会**把祖先的 display 传递到后代的计算样式上（后代自身 display 仍是空串）。
 * 因此这里以「沿祖先链查 display/visibility」为主判据，`offsetParent === null` 作为
 * 真实浏览器下的补充判据（仅在它不是 undefined 时才采信，避免 happy-dom 下全判隐藏）。
 */
function isElementVisible(el: HTMLElement): boolean {
  if (el.offsetParent === null) return false
  let node: HTMLElement | null = el
  while (node) {
    const style = getComputedStyle(node)
    if (style.display === 'none') return false
    if (style.visibility === 'hidden') return false
    node = node.parentElement
  }
  return true
}

/**
 * 只收集**可见**的导航路径。
 *
 * 「导航项存在」与「用户看得见」是两件事：分组折叠时子项在 DOM 但 display:none
 * （EP 用 v-show）。需要判断可达性/可点击性时必须用本函数，
 * 否则会把折叠态误判为"14 项都可达"。
 */
export function collectVisibleNavPaths(root: ParentNode): string[] {
  return Array.from(root.querySelectorAll<HTMLElement>('.el-menu-item[data-index]'))
    .filter(isElementVisible)
    .map(el => el.getAttribute('data-index'))
    .filter((v): v is string => typeof v === 'string' && v.length > 0)
}

/** 是否存在 el-sub-menu（即菜单是否已分组）。 */
export function hasSubMenu(root: ParentNode): boolean {
  return root.querySelector('.el-sub-menu') !== null
}

/**
 * 展开全部 el-sub-menu。
 *
 * 用途：让基于**可见性**的断言（可见文本、触控热区、截图）成立。
 * 对**存在性**断言不是必需的（EP 用 v-show，子项本就在 DOM，见文件头实测结论）。
 * 对平铺布局是无副作用的空操作（查不到标题）。
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
