import { describe, expect, it } from 'vitest'
import { NAV_DESTINATIONS, expectedNavPaths, expectedNavPathsInOrder } from '../menuModel'
import { collectNavPaths, collectVisibleNavPaths, expandAllSubMenus, hasSubMenu } from '../menuDom'

/**
 * Phase 0.3：菜单门禁工具的**自证**。
 *
 * 门禁改造最容易失败的方式是「改完全绿但已经判不出问题」——即把断言放宽到
 * 两种布局都过、连覆盖缺失也过。故这里必须证明：
 *   ① 平铺布局能判定（正例通过）；
 *   ② 分组布局下能拿到全量（EP 用 v-show，子项折叠时仍在 DOM —— 见下方实测说明）；
 *   ③ 但「存在」≠「可见」：折叠子项必须被 collectVisibleNavPaths 排除；
 *   ④ 少一项 / 多一项都必须被判定出来（否则门禁无牙）。
 *
 * ⚠️ **本 fixture 的写法经过一次修正，值得记下来**：
 * 第一版把折叠态写成"静态地不放子项"，于是 `expandAllSubMenus` 即使被改成空操作
 * 也照样全绿（该变异实测存活）= 自证是假绿。
 * 而 EP 真实行为是 v-show（子项在 DOM、display:none），与第一版两种写法都不符。
 * 现在 fixture 同时支持两种模式：
 *   - `materializeOnExpand: true` → 模拟 v-if 式惰性渲染（子项点开后才有）；
 *   - 默认 → 模拟 EP 2.14 的 v-show（子项始终在 DOM，靠 style 控制可见）。
 * 两种模式都要能判定，门禁才既不怕 EP 升级换实现、也不怕自己写错。
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
 * 分组布局。两种子项渲染模式见上方说明。
 * happy-dom 下 `offsetParent` 恒为 null，故可见性用 **inline display** 表达，
 * collectVisibleNavPaths 亦同时判 display（与真实浏览器 offsetParent 判据等价）。
 */
function renderGrouped(
  groups: { title: string; paths: string[] }[],
  opened: boolean,
  materializeOnExpand = false,
): HTMLElement {
  const menu = document.createElement('ul')
  menu.className = 'el-menu'
  for (const g of groups) {
    const sub = document.createElement('li')
    sub.className = 'el-sub-menu' + (opened ? ' is-opened' : '')
    const title = document.createElement('div')
    title.className = 'el-sub-menu__title'

    const inner = document.createElement('ul')
    inner.className = 'el-menu'
    inner.style.display = opened ? '' : 'none'
    for (const p of g.paths) {
      const li = document.createElement('li')
      li.className = 'el-menu-item'
      li.setAttribute('data-index', p)
      inner.appendChild(li)
    }
    const materialize = () => {
      if (sub.classList.contains('is-opened')) return
      sub.classList.add('is-opened')
      inner.style.display = ''
    }
    title.addEventListener('click', materialize)
    sub.appendChild(title)
    sub.appendChild(inner) // v-show 模式：子项**始终**在 DOM
    if (materializeOnExpand && !opened) {
      // v-if 模式：初始不放子项，点开才放
      sub.removeChild(inner)
      title.addEventListener('click', () => { if (!sub.contains(inner)) sub.appendChild(inner) })
    }
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

/**
 * 挂到文档再返回。
 *
 * 必要性（实测）：**游离节点**的 getComputedStyle 不会解析祖先的 display，
 * 于是"折叠子项不可见"判不出来 ⇒ collectVisibleNavPaths 返回全量（假绿）。
 * 真实浏览器里菜单一定在文档中，故 fixture 也必须入档，否则测的不是同一件事。
 */
function attach<T extends HTMLElement>(el: T): T {
  document.body.appendChild(el)
  return el
}

describe('Phase 0.3 菜单门禁工具自证', () => {
  it('模型自身：14 个导航目标、路径唯一', () => {
    expect(NAV_DESTINATIONS).toHaveLength(14)
    expect(new Set(ALL).size).toBe(14)
    expect(expectedNavPathsInOrder()).toEqual(ALL)
  })

  it('平铺布局：能收集全量路径，且不含 sub-menu', () => {
    const el = attach(renderFlat(ALL))
    expect(hasSubMenu(el)).toBe(false)
    expect(collectNavPaths(el)).toEqual(ALL)
    // 平铺下顺序断言成立（分组后不成立，故该断言只用于平铺）
    expect(collectNavPaths(el)).toEqual(expectedNavPathsInOrder())
  })

  it('分组折叠（EP v-show 实测行为）：子项仍在 DOM，故存在性判定无需展开', () => {
    const el = attach(renderGrouped(GROUPS, false))
    expect(hasSubMenu(el)).toBe(true)
    // EP 2.14.3 用 [[vShow, opened]] 渲染子级 ul ⇒ 折叠态子项**仍在 DOM**。
    // 因此 collectNavPaths（存在性）拿得到全量 —— 若仍按"折叠即不在 DOM"写，
    // 会误以为覆盖判定必须依赖展开，进而把 expandAllSubMenus 当成必要步骤。
    expect(new Set(collectNavPaths(el))).toEqual(new Set(ALL))
  })

  it('分组折叠：子项虽在 DOM 但**不可见** —— 存在 ≠ 可达（覆盖断言不得只看存在）', () => {
    const el = attach(renderGrouped(GROUPS, false))
    // 折叠时可见集合为空：用户看不到也点不到
    expect(collectVisibleNavPaths(el)).toEqual([])
    // 展开后才可见
    expandAllSubMenus(el)
    expect(new Set(collectVisibleNavPaths(el))).toEqual(new Set(ALL))
  })

  it('v-if 式惰性渲染（EP 若改为 v-if）也必须能判定 —— 展开是必要步骤', () => {
    const el = attach(renderGrouped(GROUPS, false, true))
    // 该模式子项初始不在 DOM
    expect(collectNavPaths(el)).toEqual([])
    expandAllSubMenus(el)
    expect(new Set(collectNavPaths(el))).toEqual(new Set(ALL))
  })

  it('分组展开布局：展开后能收集全量路径（覆盖判定成立）', () => {
    const el = attach(renderGrouped(GROUPS, true))
    expect(collectNavPaths(el)).not.toEqual([])
    const collected = new Set(collectNavPaths(el))
    expect(collected).toEqual(new Set(ALL))
  })

  it('expandAllSubMenus 对平铺布局是无副作用空操作', () => {
    const el = attach(renderFlat(ALL))
    const before = collectNavPaths(el)
    expandAllSubMenus(el)
    expect(collectNavPaths(el)).toEqual(before)
  })

  it('有牙：少一项 / 多一项都判得出来（否则门禁形同虚设）', () => {
    const missing = attach(renderFlat(ALL.slice(0, 13)))
    expect(new Set(collectNavPaths(missing))).not.toEqual(new Set(ALL))

    const extra = attach(renderFlat([...ALL, '/ghost']))
    expect(new Set(collectNavPaths(extra))).not.toEqual(new Set(ALL))

    const swapped = attach(renderFlat([...ALL.slice(0, 13), '/ghost']))
    expect(new Set(collectNavPaths(swapped))).not.toEqual(new Set(ALL))
  })

  it('sub-menu 标题不得被误当成导航项（否则"数量"类断言永远多算）', () => {
    const el = attach(renderGrouped(GROUPS, true))
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

  it('有牙：v-if 模式下折叠布局必须靠 expandAllSubMenus 展开才能判定（该调用不可省）', () => {
    const el = attach(renderGrouped(GROUPS, false, true))
    // 未展开：查不到任何导航项 —— 此时若断言"数量 14"会假红
    expect(collectNavPaths(el)).toEqual([])
    // 展开后才拿得到全量 —— 这一步是覆盖判定的必要前提
    expandAllSubMenus(el)
    expect(new Set(collectNavPaths(el))).toEqual(new Set(ALL))
  })

  it('expandAllSubMenus 不得把已展开的组收起来（重复点击是 toggle）', () => {
    const el = attach(renderGrouped(GROUPS, true))
    expandAllSubMenus(el)
    expandAllSubMenus(el)
    // is-opened 的组被跳过，故两次调用后仍是全量
    expect(new Set(collectNavPaths(el))).toEqual(new Set(ALL))
  })
})
