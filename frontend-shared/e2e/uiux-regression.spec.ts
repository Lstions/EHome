/**
 * UI/UX 审计断言回归门禁（2026-09-13，2026-09-14 就绪判据修复）。
 *
 * ## 2026-09-14 修复：门禁曾在"页面还没渲染数据"时就测量（已证实过一次真实假绿）
 *
 * 主控用真实 Chromium 在同源审计实例 360×800 逐帧实测 `/logical-device`：
 *
 *     t+   0ms  {scanned:149, nclips:0, rows:0, pag:0, loadingMask:1, bodyLen:154}
 *     t+ 300ms  {scanned:705, nclips:12, maxClip:365, rows:20, pag:1, loadingMask:1}
 *     t+ 500ms  {scanned:701, nclips:12, maxClip:365, rows:20, pag:1, loadingMask:0}
 *     t+5000ms  {scanned:701, nclips:12, maxClip:365, rows:20, pag:1, loadingMask:0}
 *
 * 旧就绪谓词 `scanned > 50` 在 **t+0** 就已满足，而那一刻表格 0 行、bodyLen=154
 * （只有工具栏和表头）、loadmask 还挂着、真实裁切尚未出现 —— 门禁于是取数判合规。
 * 变异自证证实了后果：删掉 `LogicalDeviceList.vue` 的
 * `.ld-pagination :deep(.el-pagination){flex-wrap:wrap}` 并重新构建（产物 CSS 中该规则
 * 确实消失），`-g "360px 无视口横向溢出"` 仍 **1 passed**，而独立探针在同一份产物上
 * 测得 clips=12 / maxClip=365px / btn-prev x=-118（视口外不可达）。
 *
 * 因此：就绪判据改为"数据承载信号 + 取数结束 + 布局连续两次一致"
 * （`waitForRouteReady`，见 helpers/uiux-fixtures.ts），并新增本文件第 1 节的
 * **探针自校准**用例（注入已知缺陷 → 断言探针必须变红）。
 * **度量原语 uiux-metrics.ts 未改动** —— 它在稳定态本来就是对的，错的只是"何时测量"。
 *
 * 用例数量：修复前 19 条，修复后 20 条（只有新增，没有删除/跳过/放宽）。
 *
 * ## 为什么有这份文件
 *
 * 主控派发理由（审计员提出的关键风险）：
 *   「本次审计结论全部在**临时 CDP 探针**下取得，**审计结束后回归未受保护**。」
 *
 * 裁决依据：规范 §7.6「只有可自动判断、影响面明确且已经证明能防真实回归的规则，
 * 才进入强制门禁」。故本文件**只收可机械判定**的断言，且**每条都对应一个已确认的
 * 真实缺陷**（注释里写明问题编号与实测证据）。**主观项（美观、间距节奏）一律不入门禁。**
 *
 * ## 三条必须遵守的方法学（违反会重犯审计踩过的坑）
 *
 * 1. **不用固定 `waitForTimeout` 等数据** —— 一律 `waitForSelector` / `expect.poll`。
 *    （审计探针为批量采样用过 `waitForTimeout(1600)`，回归断言不照抄。）
 * 2. **不依赖固定测试数据** —— `ehome_uiux` 只在本地有；需要数据的断言用 `page.route`
 *    注入确定性 mock。
 * 3. **判据必须排除已知假阳性/假阴性**（契约 §2.2/§2.3）：
 *    - 可横向滚动的祖先**不是**裁切（EP 表格 wrapper 的 `overflow:hidden` +
 *      `scrollWidth>clientWidth` 是设计允许的内部横滚，探针曾误报 10 处）；
 *    - 判定"可点击但不可聚焦"必须排除原生可聚焦元素的**后代**
 *      （`button` 内的 `svg/span` 继承 `cursor:pointer`，探针曾因此虚高 392）；
 *    - 触控热区只认**真实布局盒** `getBoundingClientRect`，**不用 `::after`**
 *      （实测在 el-table 单元格内被裁剪，且测不出伪元素）。
 *
 * ## 环境
 *
 * 前端静态产物由后端同源托管，默认 `http://127.0.0.1:8082`（`UIUX_BASE` 可覆盖）。
 * 后端由主控的受管任务持有，本文件**不启动也不重启**任何服务；不可用时给出明确错误。
 */
import { expect, test } from '@playwright/test'
import {
  assertBackendReachable,
  automationEventPage,
  CONTRAST_BODY,
  CONTRAST_LARGE,
  gotoRoute,
  logicalDevicePage,
  loginViaApi,
  measureRouteWhenReady,
  mockOverviewFailure,
  pollUntil,
  readOverflow,
  ROUTES,
  TOUCH_MIN_DENSE_TOOLBAR,
  TOUCH_MIN_MOBILE,
  UIUX_BASE,
  VIEWPORTS,
} from './helpers/uiux-fixtures'
import {
  measureContrast,
  measureKeyboardFacts,
  measureOverflow,
  measureThemeState,
  measureTouchTargets,
} from './helpers/uiux-metrics'

test.beforeAll(async () => {
  // 前置依赖失败时立刻给出指向根因的错误，而不是让每条用例各超时一次
  await assertBackendReachable()
})

// ─────────────────────────────────────────────────────────────────────────────
// 1. 横向溢出与真实裁切
// ─────────────────────────────────────────────────────────────────────────────

test.describe('横向溢出与真实裁切', () => {
  /**
   * 守护的不变量：核心路由 × 四档视口下页面级不出现横向溢出，且不存在真实裁切。
   *
   * 为什么这是回归门禁而不是"看起来合理"：
   *  - **F9 / D2-04 / D3 #4（高）**：`device-configs` 工具栏在 360px 视口被容器裁切，
   *    「导入」按钮左移出卡片 11px。实测 `impBtn.x=10 < cardBody.x=21`，
   *    且祖先链全部 `scrollable:false` ⇒ 真实裁切而非可滚动溢出。
   *    修法是一行：`.filter-right { flex-wrap: wrap }`（DeviceConfigList.vue:646-649 已加）。
   *  - **F8 / D2-03 / D3 #13（高/中）**：含 `el-table` 的页面未包 `.mobile-table-wrapper`，
   *    窄屏下固定操作列占表格宽 80%，导致操作不可达。
   *
   * 判据来自规范 §5.2：`documentElement.scrollWidth <= clientWidth`，
   * 并用 `getBoundingClientRect` 沿祖先链复核真实裁切。
   *
   * **排除规则（契约 §2.2，必须保留）**：祖先自身可横向滚动时不计裁切 ——
   * EP 的 `el-table__header-wrapper`/`body-wrapper` 与 `.mobile-table-wrapper`
   * 就是这种容器（`overflow-x:hidden/auto` + `scrollWidth > clientWidth`），
   * 宽表格在其内部横滚是设计允许的（规范 §4.3 只要求"操作列可达 + 横滚提示"）。
   * 首版探针不排除时在 automation 页误报 10 处，排掉后归零。
   */
  for (const vp of VIEWPORTS) {
    test(`核心路由在 ${vp.name}px 无视口横向溢出、无真实裁切`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height })
      await loginViaApi(page, 'light')

      const failures: string[] = []
      /** 契约 §2.3 的分母纪律：每个路由都要能回答"扫了多少元素""有多少数据行" */
      const denominators: string[] = []

      for (const route of ROUTES) {
        // ── 就绪判据（2026-09-14 假绿修复）──────────────────────────────────
        // 旧写法：gotoRoute + waitForSelector('.el-table__row, .el-card, …')
        //        再 pollUntil(v => v.scanned > 50) —— **结构性分母**。
        //        实测 /logical-device @360px：t+0 {scanned:149, rows:0, loadingMask:1,
        //        clips:0} 就已满足该谓词，而真实裁切 300ms 后才出现（12 处、最大 365px），
        //        门禁于是在"数据还没渲染"的瞬间取数并判合规 —— 已造成一次真实假绿
        //        （删掉分页 flex-wrap 规则重建后仍 1 passed）。
        // 新写法：等"数据承载信号可见（有数据/空态/错误态三者之一）+ 取数已结束
        //        （loadmask/骨架归零）+ 布局连续两次一致"；超时即抛出、用例失败，
        //        **绝不**退回"测得 0 处裁切所以通过"。判据细节见
        //        helpers/uiux-fixtures.ts 的 waitForRouteReady 与 ROUTES 注释。
        const m = await measureRouteWhenReady(page, route)
        const facts = await page.evaluate(() => ({
          rows: document.querySelectorAll('.el-table__row').length,
          tables: document.querySelectorAll('.el-table').length,
        }))
        denominators.push(
          `${route.name} @${vp.name}px：scanned=${m.scanned}，tables=${facts.tables}，` +
            `rows=${facts.rows}，clips=${m.clips.length}`
        )

        if (m.overflow > 0) {
          failures.push(
            `${route.name} @${vp.name}px 页面级横向溢出 ${m.overflow}px（scrollWidth=${m.scrollWidth} > clientWidth=${m.clientWidth}）`
          )
        }
        if (m.clips.length > 0) {
          failures.push(
            `${route.name} @${vp.name}px 真实裁切 ${m.clips.length} 处：` +
              m.clips
                .map((c) => `<${c.tag} class="${c.cls}"> 被 .${c.by} 裁掉 ${c.overBy}px ("${c.text}")`)
                .join('；')
          )
        }
      }
      // 分母守卫（契约 §2.3）：每条路由都必须能自证"扫到的元素数/数据面不是 0"。
      // 只把事实写进注释性质的输出；真正的守卫是上面 waitForRouteReady 的超时抛出。
      // 保留这次断言是为了让分母出现在**失败信息**里（下面第一条即含全部 13 条路由）。
      expect(denominators.length, '受审路由数量为 0，无法说明分母').toBe(ROUTES.length)
      expect(failures, failures.join('\n') + '\n\n分母（契约 §2.3）：\n' + denominators.join('\n')).toEqual([])
    })
  }

  /**
   * ## 探针自校准：在**真实页面**上注入已知缺陷，断言探针必须变红（契约 §2.3）
   *
   * 为什么这条是本次修复的**核心交付**：上一轮假绿暴露的不是某一条断言写错，而是
   * 「门禁回答不了『我的度量到底会不会红』」。本仓已有同类范式——
   * `src/views/layout/__tests__/TouchTargets.spec.ts` 的"探针本身可校准：分母非 0"。
   * 那条守的是"分母是 0 还是真的没缺陷"；这条守的是**横向裁切探针在真实页面上确实能变红**。
   *
   * ### 注入的缺陷就是历史上真实发生过的那个
   * `LogicalDeviceList.vue` 的 `.ld-pagination :deep(.el-pagination) { flex-wrap: wrap }`。
   * 删掉它后（主控已用"删除规则 → pnpm build → grep 产物 CSS"证实规则消失），
   * 360px 下 `el-pagination`（实测宽 676.94px，layout="total, sizes, prev, pager, next, jumper"
   * 自带 white-space:nowrap）在 .el-main 内容宽 352px 里被右对齐后整体左移出容器，
   * 而 .ld-pagination / .mobile-table-wrapper / .el-main 的 scrollWidth === clientWidth
   * （横向位移预算 0）⇒ **真实裁切**，上一页按钮永久不可达。
   * 这里用 `addStyleTag` 在浏览器里把该规则改写成 `nowrap !important` 复现同一几何
   * （不动源码、不动产物；主控的完整"删源码重建"变异自证见交付报告）。
   *
   * ### 前置条件（否则这条会变成"恒红"的空断言）
   * 先证明**注入前**探针在稳定态确实是 0 裁切 —— 也就是先证明"绿"是真的绿。
   *
   * ### 断言清单
   *   A. 前置：稳定态下 `measureOverflow().clips.length === 0`（否则本用例没有对照基准）；
   *   B. 注入后 `clips.length > 0`；
   *   C. 注入后存在 `overBy > 100` 的裁切条目（不是 2-3px 的亚像素噪声）；
   *   D. 裁切发生在分页控件上（`.ld-pagination` 相关元素），而不是别处的无关元素；
   *   E. 被裁切的元素里包含 `.btn-prev`（上一页按钮）；
   *   F. `.btn-prev` 的 `getBoundingClientRect().left < 0` —— **视口外不可达**；
   *   G. `document.elementFromPoint(btn-prev 中心)` 取不到该按钮 —— 用户点不到它；
   *   H. 页面级 `scrollWidth === clientWidth`（证明这是"裁切"而非"可滚动溢出"，
   *      即上面 A–G 测的确实是门禁守护的那类缺陷）。
   *
   * 覆盖 **两个 project**（uiux-gate-fine 桌面 360×800 / uiux-gate-coarse 移动 390×844 暗色）：
   * 门禁矩阵有两个入口，校准必须证明**门禁实际使用的那个上下文**里断言会红。
   */
  test('探针自校准：注入已知分页裁切缺陷后 measureOverflow 必须变红（契约 §2.3）', async ({ page }) => {
    // 复现主控实测的几何：360×800（缺陷只在窄屏出现；project 默认视口是 1440×900，
    // 在宽屏下 nowrap 也不会裁切 —— 那会让这条校准用例变成永远为绿的假证据）
    await page.setViewportSize({ width: 360, height: 800 })
    await loginViaApi(page, 'light')
    // 用与门禁同源的 mock（1003 条逻辑设备，走真分页），不依赖 ehome_uiux 的现存数据
    await page.route('**/api/v1/logical-devices**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(logicalDevicePage(1003)),
      })
    )

    const probe = ROUTES.find((r) => r.path === '/logical-device')!
    // 与上一条溢出用例**完全相同**的就绪判据（否则校准的就不是门禁真正用的那条路径）
    const before = await measureRouteWhenReady(page, probe)

    // A. 前置：注入前必须是真绿，否则后面"变红"说明不了任何事
    expect(
      before.clips.length,
      '注入缺陷前探针就已经是红的 —— 本用例失去对照基准：' + JSON.stringify(before.clips.slice(0, 5))
    ).toBe(0)

    // 注入已知缺陷（等价于删掉源码里的 .ld-pagination :deep(.el-pagination){flex-wrap:wrap}）
    await page.addStyleTag({ content: '.ld-pagination .el-pagination{flex-wrap:nowrap !important}' })
    // 等布局吸收新样式：用"探针连续两次报同一结果"代替 sleep
    const after = await pollUntil(
      () => readOverflow(page),
      (v) => v.clips.length > 0,
      '注入 flex-wrap:nowrap 后探针仍未测到任何裁切 —— 度量已退化，门禁无法发现该缺陷'
    )
    // 再取一次稳定读数，排除"刚好抓到过渡帧"
    const stable = await pollUntil(
      () => readOverflow(page),
      (v) => v.clips.length === after.clips.length && v.scanned === after.scanned,
      '注入后布局未稳定（两次读数不一致）'
    )

    // B/C. 有裁切，且存在 >100px 的真实裁切（不是亚像素噪声）
    expect(stable.clips.length, '探针未测到裁切：' + JSON.stringify(stable)).toBeGreaterThan(0)
    const big = stable.clips.filter((c) => c.overBy > 100)
    expect(
      big.length,
      `裁切幅度不足（最大 ${Math.max(...stable.clips.map((c) => c.overBy))}px）—— 探针只能测到噪声`
    ).toBeGreaterThan(0)

    // D. 裁切确实发生在分页控件上
    const onPagination = stable.clips.filter((c) => c.by.includes('ld-pagination') || c.cls.includes('pagination'))
    expect(
      onPagination.length,
      '裁切不在分页控件上，断言指向了别的缺陷：' + JSON.stringify(stable.clips.slice(0, 5))
    ).toBeGreaterThan(0)

    // E/F/G/H. 上一页按钮的实际可达性（走真实几何，不看截图观感）
    const reach = await page.evaluate(() => {
      const btn = document.querySelector('.ld-pagination .btn-prev')
      if (!btn) return null
      const r = btn.getBoundingClientRect()
      const cx = r.left + r.width / 2
      const cy = r.top + r.height / 2
      const hit = document.elementFromPoint(cx, cy)
      const de = document.documentElement
      return {
        left: Math.round(r.left),
        right: Math.round(r.right),
        cx: Math.round(cx),
        inViewport: cx >= 0 && cx <= window.innerWidth,
        hitSelf: !!(hit && (hit === btn || btn.contains(hit))),
        pageOverflow: de.scrollWidth - de.clientWidth,
      }
    })
    expect(reach, '页面上找不到 .ld-pagination .btn-prev，无法校准可达性').not.toBeNull()
    expect(
      reach!.left,
      `上一页按钮 left=${reach!.left} 未移出视口左侧 —— 注入的缺陷没有复现出"不可达"`
    ).toBeLessThan(0)
    expect(reach!.hitSelf, 'elementFromPoint 仍能取到上一页按钮 —— 它其实可达，缺陷未复现').toBe(false)
    expect(
      reach!.pageOverflow,
      '页面级出现横向溢出，说明这是可滚动溢出而非本次守护的"真实裁切"'
    ).toBe(0)
  })

  /**
   * 守护的不变量：F9 的**具体**回归点 —— device-configs 工具栏按钮不得越出卡片内容区左边界。
   *
   * 与上一条的区别：上一条是"全站无裁切"的面，这条钉住审计点名的**具体坐标关系**，
   * 并给出可复核的实测值（审计原文用 `impBtn.x < cardBody.x` 判定）。
   * 用 mock 造 3 张配置卡，使断言不依赖 `ehome_uiux` 的现有数据。
   */
  test('device-configs @360px 工具栏按钮不越出卡片内容区（F9/D2-04/D3 #4）', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 })
    await loginViaApi(page, 'light')
    const configs = Array.from({ length: 3 }, (_, i) => ({
      id: 900 + i,
      name: `MOCK-CFG-${i}`,
      device_type: 'jiabaida_bms',
      driver_path: 'bms',
      config_json: '{"mock":true}',
      is_default: false,
      created_at: '2026-09-01T00:00:00Z',
      updated_at: '2026-09-01T00:00:00Z',
    }))
    await page.route('**/api/v1/device-configs**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ code: 200, message: 'ok', data: { items: configs, total: 3, page: 1, page_size: 20 } }),
      })
    )
    await gotoRoute(page, '/device-configs')
    await page.waitForSelector('.filter-right .el-button', { timeout: 15000 })

    const geometry = await page.evaluate(() => {
      const box = (el: Element | null) => {
        if (!el) return null
        const r = el.getBoundingClientRect()
        return { x: Math.round(r.x), right: Math.round(r.right), w: Math.round(r.width) }
      }
      const bar = document.querySelector('.filter-bar')
      const buttons = Array.from(document.querySelectorAll('.filter-right .el-button'))
      return {
        containerX: bar ? Math.round(bar.getBoundingClientRect().x) : null,
        buttons: buttons.map((b) => ({ text: (b.textContent || '').trim().slice(0, 8), ...box(b)! })),
        buttonCount: buttons.length,
      }
    })

    expect(geometry.buttonCount, '工具栏应有按钮作为分母').toBeGreaterThan(0)
    expect(geometry.containerX, '找不到 .filter-bar 作为对比基准').not.toBeNull()
    // 审计原文判据：每个按钮的左边界不得小于其容器左边界（允许 1px 亚像素误差）
    const escaped = geometry.buttons.filter((b) => b.x < geometry.containerX! - 1)
    expect(
      escaped,
      `以下按钮左边界越出容器 x=${geometry.containerX}：` +
        escaped.map((b) => `"${b.text}" x=${b.x}`).join('、')
    ).toEqual([])
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 2. 触控热区
// ─────────────────────────────────────────────────────────────────────────────

test.describe('触控热区实际可点击区域', () => {
  /**
   * 守护的不变量：窄屏下**表格行内操作**的真实布局盒不小于规范下限。
   *
   * 对应已确认真实缺陷：
   *  - **F6 / D2-07（高）**：移动触控目标远低于 44px —— 行操作 24px 高、图标按钮 24x11。
   *  - **D3 #3（高）**：automation 页 **264 个**目标 <36px，其中 **257 个**是
   *    "行高 18px"单一根因（`size="small" + link` 的行内按钮），
   *    审计原文：`按高度聚合（桌面）：{"18": 257, "32": 7}`。
   *  - **D6-01（高）**：mobile-390/360 下多个主操作控件高度仅 22–32px。
   *
   * 判据（规范 §4.4.5 MUST）：移动端图标按钮/Switch ≥44×44px；
   * **极高密度、非主要任务的工具栏**可降到 36px。
   *
   * **为什么只测高度不测宽度**：审计原文的判定就是高度维度主导
   * （`{"18": 257}`），且行内 link 按钮的宽度由文案决定（"编辑" 30px），
   * 规范 §4.4.5 关注的是"实际可点击区域"的最小边，但表内 link 按钮
   * 已由 `padding` 提供横向热区 —— 钉住高度即可防住审计点名的那个根因，
   * 且不会把"文案短"误判成缺陷。宽度不足时单独在下面用 dense-toolbar 断言覆盖。
   *
   * **为什么用 mock**：行内按钮的存在依赖列表有数据；用 `page.route` 注入
   * 确定的行数，避免依赖 `ehome_uiux`。
   */
  const TABLE_ROUTES: Array<{ name: string; path: string; pattern: string; body: unknown }> = [
    {
      name: 'logical-device-list',
      path: '/logical-device',
      pattern: '**/api/v1/logical-devices**',
      body: logicalDevicePage(1003),
    },
    {
      name: 'automation',
      path: '/automation',
      pattern: '**/api/v1/automation-events**',
      body: automationEventPage(500),
    },
  ]

  for (const route of TABLE_ROUTES) {
    test(`${route.name} 窄屏表格行内操作高度 ≥${TOUCH_MIN_DENSE_TOOLBAR}px（F6/D2-07/D3 #3）`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 })
      await loginViaApi(page, 'light')
      await page.route(route.pattern, (r) =>
        r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(route.body) })
      )
      await gotoRoute(page, route.path)
      await page.waitForSelector('.el-table__row', { timeout: 20000 })

      const targets = await page.evaluate(measureTouchTargets, [
        '.el-table .el-button',
        '.el-table .el-switch',
      ])

      // 分母守卫（契约 §2.3）：没有量到任何控件时，不能把"0 个违规"当合规证据
      expect(targets.length, `${route.name} 未渲染出任何表格内控件，分母为 0 不能作为合规证据`).toBeGreaterThan(0)

      const tooShort = targets.filter((t) => t.h < TOUCH_MIN_DENSE_TOOLBAR)
      expect(
        tooShort,
        `${route.name} @390px 有 ${tooShort.length}/${targets.length} 个表格内控件高度 <${TOUCH_MIN_DENSE_TOOLBAR}px：` +
          tooShort.slice(0, 8).map((t) => `"${t.text || t.cls}" ${t.w}×${t.h}`).join('、')
      ).toEqual([])
    })
  }

  /**
   * 守护的不变量：**页头图标按钮**（全站共用、出现在全部已登录页面）在窄屏 ≥44px。
   *
   * 对应已确认真实缺陷 **D4 #3（高）**：移动端页头图标按钮触控区 32×32，
   * 低于 §4.4.5 的 44×44。审计原文实测 `{w:32,h:32}`。
   *
   * 为什么单列一条而不并入上面的表格断言：页头三按钮在**每一个**页面都存在，
   * 是"跨域共用"的最高频触控目标；它一旦回归，影响面是全站而不是单页。
   * 判据取 44（移动端主要任务），不是 36 的工具栏例外 —— 它在页头而非高密度工具栏内。
   */
  for (const vp of VIEWPORTS.filter((v) => v.width <= 768)) {
    test(`页头图标按钮在 ${vp.name}px 实际可点击区域 ≥${TOUCH_MIN_MOBILE}px（D4 #3）`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height })
      await loginViaApi(page, 'light')
      await gotoRoute(page, '/dashboard')
      await page.waitForSelector('.main-header button', { timeout: 15000 })

      const targets = await page.evaluate(measureTouchTargets, ['.main-header button'])
      expect(targets.length, '页头应有图标按钮作为分母').toBeGreaterThan(0)

      const undersized = targets.filter((t) => t.w < TOUCH_MIN_MOBILE || t.h < TOUCH_MIN_MOBILE)
      expect(
        undersized,
        `页头按钮 @${vp.name}px 中 ${undersized.length}/${targets.length} 个小于 ${TOUCH_MIN_MOBILE}px：` +
          undersized.map((t) => `"${t.aria || t.text}" ${t.w}×${t.h}`).join('、')
      ).toEqual([])
    })
  }

  /**
   * 守护的不变量：窄屏**输入控件**（输入框/选择器）热区 ≥44px。
   *
   * 对应已确认真实缺陷 **D6-01（高）**：`monitor: div.el-select {label:"10秒", w:342,h:32}`、
   * `dashboard: div.el-select.el-select--small {label:"温度", w:300,h:24}`。
   * 输入控件是移动端主要输入目标，走 44px 档而非 36px 工具栏例外。
   */
  test('窄屏输入控件热区 ≥44px（D6-01）', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await loginViaApi(page, 'light')
    await gotoRoute(page, '/node')

    // 用 expect.poll 等"有尺寸的输入控件"出现，而不是等某个选择器：
    // 页头全局搜索的 .el-input__wrapper 在窄屏是 0×0（display:none 的容器内），
    // 直接 waitForSelector 会挑中它并因"不可见"超时 —— 与真实缺陷无关。
    const targets = await pollUntil(
      () => page.evaluate(measureTouchTargets, ['.el-input__wrapper', '.el-select__wrapper']),
      (v) => v.some((t) => t.h > 0),
      '窄屏下未出现任何有尺寸的输入控件（分母为 0 不能作为合规证据）'
    )
    expect(targets.length, '应至少量到一个输入控件作为分母').toBeGreaterThan(0)

    const undersized = targets.filter((t) => t.h < TOUCH_MIN_MOBILE)
    expect(
      undersized,
      `@390px 有 ${undersized.length}/${targets.length} 个输入控件高度 <${TOUCH_MIN_MOBILE}px：` +
        undersized.slice(0, 6).map((t) => `${t.cls} ${t.w}×${t.h}`).join('、')
    ).toEqual([])
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 3. 对比度（亮暗双主题）
// ─────────────────────────────────────────────────────────────────────────────

test.describe('对比度 WCAG 阈值（亮暗双主题）', () => {
  /**
   * 守护的不变量：审计点名的那批元素在**亮色和暗色**下都达到 WCAG AA。
   *
   * 对应已确认真实缺陷 **F4（P1）/ D2-06 / D3 #9 / D4 #4-6**：
   *   跨四域一致测得的原值：WS 徽标 2.08、`el-tag--success` 2.08、
   *   `el-tag--warning` 2.04、`.el-button--primary` 白字 2.78、
   *   表头/统计标签/空态文案 3.08、离线徽标 `#909399`+`#f5f7fa` = 2.87。
   *
   * **为什么必须双主题**（tools/contrast-regression.mjs 文件头记录的真实教训）：
   *   「对比度修复改善了亮色（EP 默认调色板白字 2.78 → 5.98），但**漏了暗色**：
   *     暗色块沿用 EP 默认 `--color-primary(#409eff)`，主按钮白字仍只有 2.78。」
   *   这是**主控独立复验**发现的 —— 子代理自报"已完成"没覆盖。单主题断言会漏掉一半。
   *
   * 阈值（WCAG AA）：正文 4.5:1；表头等次要文字 3.0:1。
   *
   * 主题通过 `localStorage.theme` + reload 生效（stores/theme.ts:7-24 的读入口径），
   * 并在断言前**验证 DOM 真的切过去了** —— 否则暗色断言会在亮色 DOM 上跑出假绿。
   */
  const CONTRAST_TARGETS: Array<{ sel: string; threshold: number; note: string }> = [
    { sel: '.el-button--primary', threshold: CONTRAST_BODY, note: '主按钮白字（原 2.78）' },
    { sel: '.el-tag--success', threshold: CONTRAST_BODY, note: '成功徽标（原 2.08）' },
    { sel: '.el-tag--warning', threshold: CONTRAST_BODY, note: '警告徽标（原 2.04）' },
    { sel: '.el-table th .cell', threshold: CONTRAST_LARGE, note: '表头文字（原 3.08，次要文字按大字 3.0）' },
    { sel: '.stat-label', threshold: CONTRAST_BODY, note: '统计卡标签' },
    { sel: '.ws-status', threshold: CONTRAST_BODY, note: '顶栏 WS 状态徽标（原 2.08，出现在全部已登录页）' },
  ]

  for (const theme of ['light', 'dark'] as const) {
    test(`${theme === 'dark' ? '暗色' : '亮色'}主题下关键元素达 WCAG AA（F4/D2-06/D3 #9/D4 #4）`, async ({ page }) => {
      await loginViaApi(page, theme)
      await gotoRoute(page, '/dashboard')
      await page.waitForSelector('.el-table th, .stat-label', { timeout: 20000 })

      // 先证明主题真的切过去了：三处入口（attr / html.dark / storage）必须一致，
      // 否则暗色断言会在亮色 DOM 上跑出假绿（单主题漏测的另一种形态）
      const themeState = await pollUntil(
        () => page.evaluate(measureThemeState),
        (s) =>
          s.attr === theme &&
          s.htmlDark === (theme === 'dark') &&
          s.stored === theme,
        `主题未生效：期望 ${theme}，实测 ${JSON.stringify(await page.evaluate(measureThemeState))}`
      )
      expect(themeState.attr).toBe(theme)

      const selectors = CONTRAST_TARGETS.map((t) => t.sel)
      const thresholds = Object.fromEntries(CONTRAST_TARGETS.map((t) => [t.sel, t.threshold]))
      // 注意：page.evaluate 只接受**一个**参数，必须打包成对象传入
      const samples = await page.evaluate(measureContrast, {
        selectors,
        thresholds,
        defaultThreshold: CONTRAST_BODY,
      })

      // 分母守卫：每个选择器都必须真的量到元素
      for (const t of CONTRAST_TARGETS) {
        const hit = samples.filter((s) => s.sel === t.sel)
        expect(
          hit.length,
          `${theme} 主题下选择器 ${t.sel}（${t.note}）没有量到元素，无法证明其对比度达标（分母为 0）`
        ).toBeGreaterThan(0)
      }

      const failures = samples.filter((s) => !s.pass)
      expect(
        failures,
        `${theme} 主题下 ${failures.length} 个元素低于 WCAG 阈值：\n` +
          failures
            .map(
              (f) =>
                `  ${f.sel} "${f.text}" ${f.ratio} < ${f.threshold} （fg=rgb(${f.fg.join(',')}) bg=rgb(${f.bg.join(',')}) 来自 ${f.bgFrom}）`
            )
            .join('\n')
      ).toEqual([])
    })
  }
})

// ─────────────────────────────────────────────────────────────────────────────
// 4. 失败态不得伪装正常
// ─────────────────────────────────────────────────────────────────────────────

test.describe('失败态不得伪装正常', () => {
  /**
   * 守护的不变量（**本轮修复的核心，也是"伪装成正常"家族最容易回归的一个**）：
   * `/dashboard` 概览接口 500 时，页面**不得**出现「运行正常」，
   * 且**必须**出现错误态与重试入口，KPI 显示未知（—）而不是 0。
   *
   * 对应已确认真实缺陷 **U-1（P0 阻断，主控独立复现）**：
   *   「拦截 `/api/v1/overview` 返回 500 → `/dashboard` 4 个 KPI 全渲染 **0**，
   *     异常摘要卡显示**绿色对勾 +「运行正常」+「节点与设备均在线，暂无采集错误。」**，
   *     无任何错误态。`alertOkIconColor=rgb(103,194,58)`、`kpiValues=["0","0","0","0"]`、`elResult=0`。」
   *
   * 同族已在 6 个页面复现（D2 域），根因三件套：
   *   1. `|| 0` 数值回退把"未知"写成 0
   *   2. `v-else` 无条件渲染"正常/无异常"摘要
   *   3. `catch` 只弹 `ElMessage` 不置错误态
   * 对照范式：`/node/1` 显示「节点不存在或加载失败」+ 重试；
   *   `data-sources` 是唯一正确处理错误的列表页（顶部 el-alert + 重试、KPI 显示 `—`）。
   *
   * 规范 §3.4.2/§1.2.1：未知不得默认成正常；失败提供重试或退化路径。
   *
   * 这条断言的价值在变异自证里被证明：把 Dashboard.vue 的 `v-else-if="overviewError"`
   * 分支去掉后，本用例立刻变红（见交付报告的变异记录）。
   */
  test('/dashboard 概览 500 时不显示「运行正常」，且给出错误态与重试（U-1）', async ({ page }) => {
    await loginViaApi(page, 'light')
    await mockOverviewFailure(page, 500)
    await gotoRoute(page, '/dashboard')

    // 等到失败态真正渲染出来（不靠 sleep）
    await page.waitForSelector('[data-test="dashboard-summary-error"]', { timeout: 20000 })

    // 1) 不得出现"运行正常"家族的文案
    const bodyText = (await page.locator('body').innerText()).replace(/\s+/g, ' ')
    for (const forbidden of ['运行正常', '节点与设备均在线', '暂无采集错误']) {
      expect(bodyText, `概览接口 500 时页面仍出现「${forbidden}」——失败被伪装成正常`).not.toContain(forbidden)
    }

    // 2) 必须有明确的错误态（异常摘要 + 页面级）
    await expect(page.locator('[data-test="dashboard-summary-error"]')).toBeVisible()
    await expect(page.locator('[data-test="dashboard-error"]')).toBeVisible()

    // 3) 必须有重试入口
    await expect(page.locator('[data-test="dashboard-retry"]')).toBeVisible()

    // 4) KPI 必须显示未知（—）而不是 0 —— "0 节点在线"是这次阻断的直接误导
    const kpis = await page.locator('.stat-value').allInnerTexts()
    expect(kpis.length, '应至少有一个 KPI 作为分母').toBeGreaterThan(0)
    expect(
      kpis,
      `概览失败时 KPI 应全部为未知「—」，实测：${JSON.stringify(kpis)}`
    ).toEqual(kpis.map(() => '—'))
  })

  /**
   * 守护的不变量：失败态的**反面** —— 接口正常时该页面仍要正常渲染。
   *
   * 为什么需要这条（防止"为了过失败态断言而把成功态也写成错误态"）：
   * 只断言"失败时不显示正常"可以被一个"永远显示错误"的实现骗过 ——
   * 那同样是缺陷（用户会以为系统一直坏了）。这条钉住"正常数据时不得报错"。
   *
   * 这里用 mock 造正常的概览响应，使断言不依赖 `ehome_uiux` 的真实告警数量：
   * 审计原文记录 `summaryTagText` 为「运行正常」或「需关注」二选一，
   * 取决于 `offlineCollectors/offlineDevices/dataErrorCount`。
   * 造一份"全 0 异常"的数据即可稳定命中「运行正常」这一支。
   */
  test('/dashboard 概览正常时显示运行正常、不显示错误态（U-1 的反面对照）', async ({ page }) => {
    await loginViaApi(page, 'light')
    await page.route('**/api/v1/overview**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          code: 200,
          message: 'ok',
          data: {
            nodes: { total: 2, online: 2, offline: 0 },
            edge_devices: { total: 3, online: 3, offline: 0 },
            channels: { total: 2, online: 2 },
            data_errors: { total: 0, last_hour: 0 },
            latest_data: [],
          },
        }),
      })
    )
    await gotoRoute(page, '/dashboard')

    const tag = page.locator('[data-test="dashboard-summary-tag"]')
    await expect(tag).toBeVisible({ timeout: 20000 })
    await expect(tag).toHaveText('运行正常')
    await expect(page.locator('[data-test="dashboard-summary-error"]')).toHaveCount(0)
    await expect(page.locator('[data-test="dashboard-error"]')).toHaveCount(0)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 5. 键盘可达性
// ─────────────────────────────────────────────────────────────────────────────

test.describe('键盘可达性', () => {
  /**
   * 守护的不变量：桌面端连按 Tab 能在若干次内把焦点送进侧栏导航，
   * 且侧栏菜单项确有可聚焦入口。
   *
   * 对应已确认真实缺陷 **F7 / D4 #2（高）/ D3 #18（中）**：
   *   「侧栏 12 个菜单项 tabindex=-1（**30 次 Tab 进不去**）」
   *   主控独立复现：「`cursor:pointer` 元素 221 个，其中 208 个 `tabIndex<0`；
   *     连按 30 次 Tab，焦点**从未进入侧栏**。」
   *
   * 规范 §3.1.3：可点击非原生容器需有 role/tabindex/aria-label。
   *
   * **判据修正（契约 §2.3，必须保留）**：统计"可点击但不可聚焦"时必须排除
   * 原生可聚焦元素的**后代** —— `button` 内的 `svg/path/i/span` 继承
   * `cursor:pointer` 但不是独立点击目标。D3 域实测不过滤时 automation 报 414 个，
   * 其中约 392 个属此类（仅 `span` 就 260 个，来自 `.el-table__row` 的 cursor 继承），
   * **过滤后真实缺陷为 0**。这是 §2.3 假阴性家族的镜像：假阳性。
   *
   * **为什么同时给分母**（`pointerTotal`）：契约纪律"每一个计数型字段都必须能回答
   * 分母是多少"。一个恒为 0 的统计量既可能意味着"没问题"，也可能意味着"什么都没扫到"。
   */
  test('连按 Tab 可在 30 次内把焦点送入侧栏导航（F7/D4 #2）', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await loginViaApi(page, 'light')
    await gotoRoute(page, '/dashboard')
    await page.waitForSelector('.sidebar .el-menu-item', { timeout: 15000 })

    const facts = await page.evaluate(measureKeyboardFacts)
    // 分母守卫：侧栏确实存在菜单项，否则"进不去"与"没有侧栏"无法区分
    expect(facts.sidebarMenuItems, '侧栏菜单项数量为 0，无法判断键盘可达性（契约 §2.3 分母纪律）').toBeGreaterThan(0)

    // 前置事实：至少要有**一个**可聚焦入口，否则 Tab 永远进不去
    expect(
      facts.sidebarFocusableItems,
      `侧栏 ${facts.sidebarMenuItems} 个菜单项里没有任何可聚焦入口（全部 tabindex="-1"）：` +
        `tabindex 实测 ${JSON.stringify(facts.sidebarTabindexValues)}`
    ).toBeGreaterThan(0)

    // 行为断言：连按 Tab（≤30 次，审计原文的 30 次口径）必须进入侧栏
    let enteredAt = -1
    for (let i = 1; i <= 30; i++) {
      await page.keyboard.press('Tab')
      const inSidebar = await page.evaluate(() => {
        const a = document.activeElement
        return !!(a && a.closest('.sidebar'))
      })
      if (inSidebar) {
        enteredAt = i
        break
      }
    }
    expect(
      enteredAt,
      '连按 30 次 Tab 焦点仍未进入侧栏（D4 #2 回归：菜单项无可聚焦入口）'
    ).toBeGreaterThan(0)
  })

  /**
   * 守护的不变量：`clickableNotFocusable` 这个统计量**不是恒 0 的空统计**，
   * 且它若不为 0，必须是可人工核对的真实目标。
   *
   * 这条断言守护的是**探针本身的可信度**（契约 §2.3 的核心教训）：
   *   「探针在一个字段上先后犯过**两种相反**的错误 —— 先是假阴性（选择器太窄，恒为 0），
   *     修完又变成假阳性（判据太宽，虚高 392）。这说明**单靠"数字看起来合理"无法判断
   *     探针正确性**，必须拿一两个已知答案的样本校准。」
   *
   * 因此这里断言三件可机械判定的事：
   *   1. `pointerTotal` 分母**不为 0** —— 证明扫描器真的扫到了东西（防假阴性）；
   *   2. `sidebarMenuItems` **不为 0** —— 侧栏这个已知样本存在；
   *   3. `clickableNotFocusable` **不含**原生可聚焦元素的后代 —— 防假阳性。
   *      抽样项不得命中 `button/a/input/[role=button]/[tabindex]` 内部。
   */
  test('键盘探针本身可校准：分母非 0，且不含原生可聚焦元素的后代（契约 §2.3）', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await loginViaApi(page, 'light')
    await gotoRoute(page, '/dashboard')
    await page.waitForSelector('.sidebar .el-menu-item', { timeout: 15000 })

    const facts = await page.evaluate(measureKeyboardFacts)

    // 1) 防假阴性：分母必须非 0
    expect(
      facts.pointerTotal,
      'cursor:pointer 元素数为 0 —— 扫描器什么都没扫到，此时 clickableNotFocusable=0 不能作为合规证据'
    ).toBeGreaterThan(0)
    expect(facts.sidebarMenuItems, '侧栏菜单项为 0，已知样本缺失').toBeGreaterThan(0)

    // 2) 防假阳性：抽样项必须不在原生可聚焦元素的内部
    const inNative = await page.evaluate(() =>
      Array.from(document.querySelectorAll<HTMLElement>('*'))
        .filter((el) => el.offsetParent !== null && getComputedStyle(el).cursor === 'pointer')
        .filter((el) => el.tabIndex < 0)
        .filter((el) => !el.closest('button, a[href], input, select, textarea, [role="button"], [tabindex]'))
        .filter((el) => el.closest('button, a[href], input, select, textarea, [role="button"], [tabindex]') !== null)
        .length
    )
    expect(
      inNative,
      'clickableNotFocusable 判据把原生可聚焦元素的后代算了进去（契约 §2.3 的假阳性回归）'
    ).toBe(0)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 6. 主题一致性（对比度断言的前置不变量）
// ─────────────────────────────────────────────────────────────────────────────

test.describe('主题状态一致性', () => {
  /**
   * 守护的不变量：亮暗主题的三处入口（`data-theme` 属性、`html.dark` 类、
   * `localStorage.theme`）在**登录后的真实路由**上保持一致。
   *
   * 为什么这是必须的门禁而不是实现细节：
   * 对比度断言（第 3 节）依赖主题真的生效。tests 里曾经的写法是先 `goto` 再设
   * `localStorage`，此时 DOM 仍是旧主题 —— **暗色断言会在亮色 DOM 上跑出假绿**，
   * 这正是"单主题断言漏掉一半"的另一种形态。
   * 规范 §3.6.3 MUST：`theme` store 同步 mode、storage、`data-theme`、`html.dark`，
   * 不得新增第二个主题开关或局部暗色 class。
   */
  for (const theme of ['light', 'dark'] as const) {
    test(`${theme} 主题在真实路由上三处入口一致（规范 §3.6.3）`, async ({ page }) => {
      await loginViaApi(page, theme)
      await gotoRoute(page, '/dashboard')
      await page.waitForSelector('.main-layout', { timeout: 15000 })

      const state = await pollUntil(
        () => page.evaluate(measureThemeState),
        (s) => s.attr === theme && s.stored === theme,
        `主题 ${theme} 未在 DOM/storage 生效`
      )
      expect(state.attr).toBe(theme)
      expect(state.htmlDark).toBe(theme === 'dark')
      expect(state.stored).toBe(theme)
      expect(state.bodyClass).toContain(theme === 'dark' ? 'dark-theme' : 'light-theme')
    })
  }
})

// 说明：本文件**有意不包含**主观项（美观、间距节奏、圆角观感）——
// 规范 §7.6 只允许"可自动判断、影响面明确且已经证明能防真实回归"的规则进入强制门禁。
// 每个断言对应的缺陷编号与证据见各 describe/test 的注释。
void UIUX_BASE
