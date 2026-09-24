/**
 * UI/UX 回归门禁的页内度量原语（浏览器侧执行）。
 *
 * 为什么单独抽出：这些函数要 `page.evaluate` 到浏览器里跑，不能引用 Node 侧的
 * Playwright 类型；放 helpers 里让 spec 只负责"断言什么"，不重复"怎么量"。
 *
 * 全部度量都遵守 docs/分析/UIUX审计契约-2026-09-13.md §2 的四个探针盲区教训：
 *   §2.2 可横向滚动的祖先不是裁切（EP 表格 wrapper 的 overflow:hidden + scrollWidth>clientWidth）
 *   §2.3 计数型字段必须能回答"分母是多少"；排除原生可聚焦元素的后代（cursor 继承）
 *   §2.1 只认 getBoundingClientRect / scrollWidth，不认截图观感
 *   D1 域补充：自研 .empty-state 才是主用空态（.el-empty 仅 2 处）
 */

/** 页内可序列化度量结果 */
export interface DocumentOverflow {
  path: string
  /** documentElement.scrollWidth - clientWidth；>0 即页面级横向溢出 */
  overflow: number
  scrollWidth: number
  clientWidth: number
  /** 真实裁切（已排除可横向滚动的祖先），最多保留 limit 条 */
  clips: ClipRecord[]
  /** 参与裁切判定的元素总数（分母，契约 §2.3） */
  scanned: number
}

export interface ClipRecord {
  tag: string
  cls: string
  /** 施加裁切的祖先（取前两个 class） */
  by: string
  /** 超出祖先边界的像素数 */
  overBy: number
  text: string
  /** 祖先自身是否可横向滚动（true 表示这是设计允许的内部横滚，已排除） */
  ancestorScrollable: boolean
}

export interface TouchTarget {
  tag: string
  cls: string
  w: number
  h: number
  text: string
  aria: string
  /** 判定用的真实布局盒（getBoundingClientRect），不是伪元素 */
  inTable: boolean
}

export interface ContrastSample {
  sel: string
  cls: string
  text: string
  fg: number[]
  bg: number[]
  /** 有效背景来自哪个元素（自身透明时向上找到的第一个不透明祖先） */
  bgFrom: string
  fontSize: number
  fontWeight: string
  ratio: number
  threshold: number
  pass: boolean
}

/**
 * 页面级横向溢出 + 真实裁切。
 *
 * 裁切判据（与 tools/uiux-audit.mjs 的 MEASURE 同源，见契约 §2.2）：
 *   沿祖先链找第一个 overflow != visible 的祖先；
 *   若该祖先**自身可横向滚动**（scrollWidth > clientWidth + 2 且 overflow-x ∈ auto/scroll/hidden），
 *   则内容在其内部滚动是设计允许的（EP el-table__header-wrapper / body-wrapper、
 *   .mobile-table-wrapper），**不计裁切**并停止上溯。
 *
 * 这条排除规则是必须的：首版探针不排除时在 automation 误报 10 处，全是宽表格的横滚容器。
 */
export function measureOverflow(limit = 12): DocumentOverflow {
  const de = document.documentElement
  const clips: ClipRecord[] = []
  let scanned = 0

  document.querySelectorAll('body *').forEach((el) => {
    const r = el.getBoundingClientRect()
    // 只统计有实际绘制盒的元素；零尺寸元素（隐藏、空 span）不构成裁切
    if (r.width <= 0 || r.height <= 0) return
    scanned++
    let anc: HTMLElement | null = el.parentElement
    while (anc && anc !== document.body) {
      const acs = getComputedStyle(anc)
      if (acs.overflowX !== 'visible' || acs.overflowY !== 'visible') {
        const ar = anc.getBoundingClientRect()
        const ancestorScrollable =
          anc.scrollWidth > anc.clientWidth + 2 &&
          (acs.overflowX === 'auto' || acs.overflowX === 'scroll' || acs.overflowX === 'hidden')
        const overRight = r.right - ar.right
        const overLeft = ar.left - r.left
        if (!ancestorScrollable && (overRight > 2 || overLeft > 2)) {
          if (clips.length < limit) {
            clips.push({
              tag: el.tagName.toLowerCase(),
              cls: typeof el.className === 'string' ? el.className.split(' ').slice(0, 2).join('.') : '',
              by: typeof anc.className === 'string' ? anc.className.split(' ').slice(0, 2).join('.') : '',
              overBy: Math.round(Math.max(overRight, overLeft)),
              text: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 24),
              ancestorScrollable,
            })
          }
        }
        break
      }
      anc = anc.parentElement
    }
  })

  return {
    path: location.pathname,
    overflow: de.scrollWidth - de.clientWidth,
    scrollWidth: de.scrollWidth,
    clientWidth: de.clientWidth,
    clips,
    scanned,
  }
}

/**
 * 触控热区：只取**真实布局盒**。
 *
 * 契约 §2 与 theme.css 的实测结论：曾用 ::after 伪元素外扩热区，
 * 但 Chromium 实测伪元素在 el-table 单元格内被单元格/行盒裁剪，
 * 且 getBoundingClientRect 根本测不出伪元素 —— 故一律用元素自身的 rect 判定。
 */
export function measureTouchTargets(selectors: string[]): TouchTarget[] {
  const out: TouchTarget[] = []
  for (const sel of selectors) {
    document.querySelectorAll(sel).forEach((el) => {
      const r = el.getBoundingClientRect()
      if (r.width <= 0 || r.height <= 0) return
      out.push({
        tag: el.tagName.toLowerCase(),
        cls: typeof el.className === 'string' ? el.className.split(' ').slice(0, 3).join('.') : '',
        w: Math.round(r.width),
        h: Math.round(r.height),
        text: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 16),
        aria: el.getAttribute('aria-label') || '',
        inTable: !!el.closest('.el-table'),
      })
    })
  }
  return out
}

/**
 * 对比度采样：WCAG 2.x 相对亮度比。
 *
 * 两个必须做对的地方（都是审计踩过的坑）：
 *  1. **有效背景**：EP 大量元素自身 backgroundColor 为 transparent，
 *     必须向上找第一个不透明祖先，否则会拿 transparent 去算（结果恒为某个虚高值）。
 *  2. **亮暗双主题都要测**：主控复验发现"亮色修好但暗色主按钮仍是 2.78"，
 *     只断言单一主题会漏掉一半（见 tools/contrast-regression.mjs 的文件头）。
 */
export function measureContrast(options: {
  selectors: string[]
  thresholds: Record<string, number>
  defaultThreshold: number
}): ContrastSample[] {
  const { selectors, thresholds, defaultThreshold } = options
  const parse = (s: string): { rgb: number[]; a: number } | null => {
    const m = String(s).match(/rgba?\(([^)]+)\)/)
    if (!m) return null
    const p = m[1].split(',').map((x) => parseFloat(x))
    return { rgb: p.slice(0, 3), a: p.length > 3 ? p[3] : 1 }
  }
  const luminance = ([r, g, b]: number[]) => {
    const f = (c: number) => {
      c /= 255
      return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
    }
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
  }
  const ratio = (a: number[], c: number[]) => {
    const l1 = luminance(a)
    const l2 = luminance(c)
    return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)
  }

  // 有效背景：从元素自身起向上找第一个不透明背景
  const effectiveBg = (el: Element): { rgb: number[]; from: string } => {
    let n: HTMLElement | null = el as HTMLElement
    while (n && n !== document.documentElement) {
      const bg = parse(getComputedStyle(n).backgroundColor)
      if (bg && bg.a > 0.9) {
        return { rgb: bg.rgb, from: typeof n.className === 'string' && n.className ? n.className.split(' ')[0] : n.tagName.toLowerCase() }
      }
      n = n.parentElement
    }
    const bodyBg = parse(getComputedStyle(document.body).backgroundColor)
    return { rgb: bodyBg ? bodyBg.rgb : [255, 255, 255], from: 'body' }
  }

  const out: ContrastSample[] = []
  for (const sel of selectors) {
    document.querySelectorAll(sel).forEach((el) => {
      const r = el.getBoundingClientRect()
      if (r.width <= 0 || r.height <= 0) return
      const cs = getComputedStyle(el)
      const fg = parse(cs.color)
      if (!fg) return
      const bg = effectiveBg(el)
      const value = ratio(fg.rgb, bg.rgb)
      const threshold = thresholds[sel] ?? defaultThreshold
      out.push({
        sel,
        cls: typeof el.className === 'string' ? el.className.split(' ').slice(0, 3).join('.') : '',
        text: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 16),
        fg: fg.rgb,
        bg: bg.rgb,
        bgFrom: bg.from,
        fontSize: parseFloat(cs.fontSize),
        fontWeight: cs.fontWeight,
        ratio: Math.round(value * 100) / 100,
        threshold,
        pass: value >= threshold,
      })
    })
  }
  return out
}

/**
 * 键盘可达性事实。
 *
 * `clickableNotFocusable` 的判据按契约 §2.3 的两条修正：
 *  - **不能**只扫 [role=button]/.el-button/button：本仓大量可点击元素是带 @click 的 div/li
 *    （侧栏 el-menu-item、通知条目、.logo-area），白名单会得到恒为 0 的**假阴性**；
 *  - **必须**排除原生可聚焦元素的**后代**：button 内的 svg/span 继承 cursor:pointer
 *    但不是独立点击目标，不排除会虚高（D3 域实测 414 → 过滤后 0）。
 * 同时返回分母 pointerTotal（契约 §2.3 纪律：计数型字段必须能回答分母）。
 */
export function measureKeyboardFacts(): {
  pointerTotal: number
  clickableNotFocusable: number
  cnfSample: string[]
  sidebarMenuItems: number
  sidebarFocusableItems: number
  sidebarItemsWithTabindex0: number
  sidebarTabindexValues: number[]
  /** Phase 0.3：分组事实（分组后 sidebarMenuItems 不含折叠子项）。 */
  sidebarGrouped: boolean
  sidebarSubMenuTitles: number
  sidebarSubMenuFocusable: number
} {
  const all = Array.from(document.querySelectorAll<HTMLElement>('*'))
  const pointerEls = all.filter(
    (el) => el.offsetParent !== null && getComputedStyle(el).cursor === 'pointer'
  )
  const cnf = pointerEls.filter((el) => {
    if (el.tabIndex >= 0) return false
    if (el.closest('button, a[href], input, select, textarea, [role="button"], [tabindex]')) return false
    return true
  })
  const menuItems = Array.from(document.querySelectorAll<HTMLElement>('.sidebar .el-menu-item'))
  // Phase 0.3：菜单可能已分组（el-sub-menu）。折叠态子项**不在 DOM**，
  // 于是 sidebarMenuItems 会骤降（14 → 组数），而消费方的 `> 0` 守卫仍会通过 ——
  // 属"分母还在、但已不代表覆盖"的静默退化。这里额外给出分组事实与分组标题的可聚焦性，
  // 让门禁能区分「平铺 N 项」与「分组 M 组 + 折叠子项」两种形态。
  const subMenuTitles = Array.from(document.querySelectorAll<HTMLElement>('.sidebar .el-sub-menu__title'))
  return {
    pointerTotal: pointerEls.length,
    clickableNotFocusable: cnf.length,
    cnfSample: cnf.slice(0, 8).map((e) => `${e.tagName}.${typeof e.className === 'string' ? e.className.split(' ')[0] : ''}:${(e.textContent || '').trim().slice(0, 12)}`),
    sidebarMenuItems: menuItems.length,
    sidebarFocusableItems: menuItems.filter((el) => el.tabIndex >= 0).length,
    sidebarItemsWithTabindex0: menuItems.filter((el) => el.getAttribute('tabindex') === '0').length,
    sidebarTabindexValues: menuItems.map((el) => el.tabIndex),
    /** 是否已分组（存在 el-sub-menu 标题）。分组后 sidebarMenuItems 不含折叠子项。 */
    sidebarGrouped: subMenuTitles.length > 0,
    /** 分组标题数（平铺布局为 0）。 */
    sidebarSubMenuTitles: subMenuTitles.length,
    /** 分组标题里可聚焦的个数：键盘用户必须能展开分组，否则子项永久不可达。 */
    sidebarSubMenuFocusable: subMenuTitles.filter((el) => el.tabIndex >= 0).length,
  }
}

/** 当前主题事实：三处入口必须一致（规范 §3.6.3 / stores/theme.ts:12-24） */
export function measureThemeState(): { attr: string | null; htmlDark: boolean; bodyClass: string; stored: string | null } {
  return {
    attr: document.documentElement.getAttribute('data-theme'),
    htmlDark: document.documentElement.classList.contains('dark'),
    bodyClass: document.body.className,
    stored: localStorage.getItem('theme'),
  }
}
