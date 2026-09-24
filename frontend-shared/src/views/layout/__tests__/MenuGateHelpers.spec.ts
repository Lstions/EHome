import { describe, expect, it } from 'vitest'
import { NAV_DESTINATIONS, expectedNavPaths, expectedNavPathsInOrder } from '../menuModel'
import { collectNavPaths, expandAllSubMenus, hasSubMenu } from '../menuDom'

/**
 * Phase 0.3：菜单门禁工具的**自证**。
 *
 * 门禁改造最容易失败的方式是「改完全绿但已经判不出问题」——即把断言放宽到
 * 两种布局都过、连覆盖缺失也过。故这里必须证明：
 *   ① 平铺布局能判定（正例通过）；
 *   ② 分组**折叠**布局下，未展开时查不到子项（说明"必须先展开"不是多余步骤）；
 *   ③ 分组展开后能收集到**全量**导航路径；
 *   ④ 少一项 / 多一项都必须被判定出来（否则门禁无牙）。
 *
 * 用真实 DOM + 手写 EP 结构模拟三种布局，不 mount 组件：
 * 本工具的职责是"给定 DOM 判定"，与 Vue 渲染无关，这样两者的失败面互不掩盖。
 */

/** 造一段与 EP 实际的 class 结构一致的 DOM。 */
function renderFlat(paths: string[]): HTMLElement {
  const menu = document.createElement('ul')
  menu.className = 'el-menu'
  for (const p of paths) {
    const li = document.createElement('li')
    li.className = 'el-menu-item'
    li.setAttribute('data-index', p)
    menu.appendChild(li)
  }
  return menu
}

/**
 * 分组布局：组标题 + 折叠（子项不入 DOM）或展开（子项入 DOM）。
 *
 * 折叠态**必须模拟 EP 的惰性渲染**：真实 el-sub-menu 未展开时子项根本不在 DOM，
 * 且点击标题会触发 Vue 渲染出子项。若 fixture 只是"静态地不放子项"，
 * 那么 expandAllSubMenus 即使被改成空操作也照样通过（实测：该变异曾存活），
 * 自证就变成了假绿。故这里给标题挂 click → 物化子项，与 EP 行为同形。
 */
function renderGrouped(groups: { title: string; paths: string[] }[], opened: boolean): HTMLElement {
  const menu = document.createElement('ul')
  menu.className = 'el-menu'
  for (const g of groups) {
    const sub = document.createElement('li')
    sub.className = 'el-sub-menu'
    const title = document.createElement('div')
    title.className = 'el-sub-menu__title'
    const materialize = () => {
      if (sub.querySelector('.el-menu-item')) return // 幂等：已物化则不重复
      sub.classList.add('is-opened')
      const inner = document.createElement('ul')
      inner.className = 'el-menu'
      for (const p of g.paths) {
        const li = document.createElement('li')
        li.className = 'el-menu-item'
        li.setAttribute('data-index', p)
        inner.appendChild(li)
      }
      sub.appendChild(inner)
    }
    title.addEventListener('click', materialize)
    sub.appendChild(title)
    if (opened) materialize()
    menu.appendChild(sub)
  }
  return menu
}

const ALL = expectedNavPaths()
const GROUPS = [
  { title: '设备', paths: ALL.slice(0, 4) },
  { title: '数据', paths: ALL.slice(4, 7) },
  { title: '自动化与通知', paths: ALL.slice(7, 12) },
  { title: '运维', paths: ALL.slice(12) },
]

describe('Phase 0.3 菜单门禁工具自证', () => {
  it('模型自身：14 个导航目标、路径唯一', () => {
    expect(NAV_DESTINATIONS).toHaveLength(14)
    expect(new Set(ALL).size).toBe(14)
    expect(expectedNavPathsInOrder()).toEqual(ALL)
  })

  it('平铺布局：能收集全量路径，且不含 sub-menu', () => {
    const el = renderFlat(ALL)
    expect(hasSubMenu(el)).toBe(false)
    expect(collectNavPaths(el)).toEqual(ALL)
    // 平铺下顺序断言成立（分组后不成立，故该断言只用于平铺）
    expect(collectNavPaths(el)).toEqual(expectedNavPathsInOrder())
  })

  it('分组折叠布局：子项不在 DOM（证明"必须先展开"不是多余步骤）', () => {
    const el = renderGrouped(GROUPS, false)
    expect(hasSubMenu(el)).toBe(true)
    // 折叠时一个导航项都查不到 —— 若门禁直接断言"数量 14"会假红，
    // 若断言"≥1"则会把"覆盖全缺"放过。两种误判都由这一步钉住。
    expect(collectNavPaths(el)).toEqual([])
  })

  it('分组展开布局：展开后能收集全量路径（覆盖判定成立）', () => {
    const el = renderGrouped(GROUPS, true)
    expect(collectNavPaths(el)).not.toEqual([])
    const collected = new Set(collectNavPaths(el))
    expect(collected).toEqual(new Set(ALL))
  })

  it('expandAllSubMenus 对平铺布局是无副作用空操作', () => {
    const el = renderFlat(ALL)
    const before = collectNavPaths(el)
    expandAllSubMenus(el)
    expect(collectNavPaths(el)).toEqual(before)
  })

  it('有牙：少一项 / 多一项都判得出来（否则门禁形同虚设）', () => {
    const missing = renderFlat(ALL.slice(0, 13))
    expect(new Set(collectNavPaths(missing))).not.toEqual(new Set(ALL))

    const extra = renderFlat([...ALL, '/ghost'])
    expect(new Set(collectNavPaths(extra))).not.toEqual(new Set(ALL))

    const swapped = renderFlat([...ALL.slice(0, 13), '/ghost'])
    expect(new Set(collectNavPaths(swapped))).not.toEqual(new Set(ALL))
  })

  it('sub-menu 标题不得被误当成导航项（否则"数量"类断言永远多算）', () => {
    const el = renderGrouped(GROUPS, true)
    const paths = collectNavPaths(el)
    // 组标题文本不应混进路径集合
    expect(paths).not.toContain('设备')
    expect(paths).not.toContain('数据')
    expect(paths).toHaveLength(14)
  })

  it('有牙：无 data-index 的 .el-menu-item 必须被排除（如 sub-menu 标题复用同类名时）', () => {
    const menu = document.createElement('ul')
    menu.className = 'el-menu'
    // 一个正常导航项
    const ok = document.createElement('li')
    ok.className = 'el-menu-item'
    ok.setAttribute('data-index', '/dashboard')
    menu.appendChild(ok)
    // 同类名但无 data-index 的项（EP 内部节点/分组标题）—— 不得计入导航
    const noise = document.createElement('li')
    noise.className = 'el-menu-item'
    menu.appendChild(noise)

    expect(collectNavPaths(menu)).toEqual(['/dashboard'])
  })

  it('有牙：折叠布局必须靠 expandAllSubMenus 展开才能判定（该调用不可省）', () => {
    const el = renderGrouped(GROUPS, false)
    // 未展开：查不到任何导航项 —— 此时若断言"数量 14"会假红
    expect(collectNavPaths(el)).toEqual([])
    // 展开后才拿得到全量 —— 这一步是覆盖判定的必要前提
    expandAllSubMenus(el)
    expect(new Set(collectNavPaths(el))).toEqual(new Set(ALL))
  })

  it('expandAllSubMenus 不得把已展开的组收起来（重复点击是 toggle）', () => {
    const el = renderGrouped(GROUPS, true)
    expandAllSubMenus(el)
    expandAllSubMenus(el)
    // is-opened 的组被跳过，故两次调用后仍是全量
    expect(new Set(collectNavPaths(el))).toEqual(new Set(ALL))
  })
})
