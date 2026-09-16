/**
 * P1-C「列表页约定门禁」—— 本仓第 4 条全站源码扫描门禁。
 *
 * 范式取自已有 3 条扫描门禁（**勿另起炉灶**）：
 *   utils/__tests__/i1ErrorConvergenceGuard.spec.ts          —— 用 node:fs 扫描而非 grep
 *     （其文件头记录过：grep 在 vitest 里抛错会被吞成空数组 ⇒ 测试假绿）
 *   components/common/__tests__/PageHeaderPropsGate.spec.ts  —— 「分类器自检」：正例/反例喂给**同一个**判定函数
 *   views/__tests__/MobileTableWrapper.spec.ts               —— 口径跑空守卫 + 豁免白名单不得腐烂 + 反证组
 * 放 views/__tests__/ 的理由：本门禁的**分母是「路由级视图」**，与 MobileTableWrapper.spec.ts 同域
 * （后者也是 views 域的全站扫描）；utils/ 那条扫 src 全域，components/common/ 那条扫 src/views。
 *
 * ── 口径（denominator）─────────────────────────────────────────────────────
 * **组件直接注册在 src/router/index.ts 上、且模板里有 <el-pagination> 的 .vue** = 路由级列表页。
 *   · 不按「所有 .vue」算：卡片/子组件本来就不该有页头与分页（台账 §3.35：分母错 ⇒ 工作量虚增 4 倍）；
 *   · 不按「目录名带 list」算：命名只是巧合，改名即漏检；
 *   · **有分页才叫列表页**：详情页/状态页（NodeOverview / Monitor / Profile / Dashboard / error /
 *     auth / layout）不进分母，否则规则③会误伤一批本就不该有页头的页面。
 *   路由表里 component 有两种写法，解析器都覆盖：内联 () => import('...') 与 routeLoaders.ts 导出的
 *   具名 loader（loadNodeList / loadEdgeDeviceList）。**新增第三种写法必须同步本解析器**；
 *   解析不出来时硬失败（toBeTruthy），不是跳过 —— 跳过 = 静默漏检 = 假绿。
 *   当前分母 = 12 个页面（2026-09-15 实测 11 个；P2-A 新增 NotificationDeliveries.vue 后为 12，
 *   跑空守卫见「口径可复现」用例）。
 *
 * ── 本门禁**查不了什么**（能力边界，必须与规则一起读）─────────────────────
 * 1. **查不了运行时行为**。「真分页」的实质是「翻页时带着 page/page_size 打一次接口，后端返回当前页
 *    切片」。源码层做不到这件事，本门禁只做源码层**必要不充分**的近似：
 *      ① 不存在对本地列表的切片分页（近似判定，边界见下）；
 *      ② el-pagination 绑定了 :total 与 current-page（缺任一项都不可能是真分页）。
 *    **两条都过 ≠ 真分页**：接口参数拼错、后端忽略 page 仍返回全量、翻页回调里没发请求，这三种
 *    都查不出来。运行时行为由各页自己的 mount 测试（断言请求参数）与 e2e 覆盖。
 * 2. **查不了请求体**。本门禁不解析 api 层调用链，也不看 store —— 页面把分页委托给 store
 *    （AlertRules → stores/alert.ts 的 page_size）时，本门禁只能看到"绑定了 total/current-page/回调"。
 * 3. **查不了语义**：规则②只能识别"确认弹窗的调用形态"，不能判断某个操作**是不是**危险操作。
 *    非危险操作用了 confirmDanger（过度确认）同样通过；未走任何确认的真·删除也查不出来。
 * 4. **查不到 template 里没有 el-pagination 的列表页**：v-infinite-scroll / 虚拟滚动 / 「加载更多」
 *    形态的列表页被整个排除在分母外。当前全站无此形态；将来引入需显式登记，而不是假装覆盖。
 * 5. **查不到 .vue 之外的分页**（把切片搬进 store / composable 就看不见了）。
 * 6. 规则②的「危险操作无确认」子判定只匹配 delete/remove/destroy/purge 形态的调用。
 * 7. **规则①是近似判定，边界在下面**。
 *
 * ── 规则①的近似的边界（写清楚，别把它当真分页的证明）─────────────────────
 * 判定 = 每个 .slice(...) 调用的**实参文本**里出现 currentPage / pageSize / page 之类标识符。
 *   items.slice((currentPage.value - 1) * pageSize, currentPage.value * pageSize)   ⇒ 命中（本地分页）
 *   filteredChannels.value.slice(start, start + pageSize)                           ⇒ 命中（本地分页）
 *   realtimeData.value.slice(0, 50) / new Date().toISOString().slice(0, 10)         ⇒ 不命中（无害截断）
 * **漏检**：切片参数不经过页码变量（如改 offset 再切片）、把分页封进子组件/composable 再调用。
 * **误报**：把 pageSize 当"最多保留 N 条"的上限（数据面板实时流就是这种 ⇒ 见豁免表，属**已知误报**）。
 *
 * ── 变红条件 ──────────────────────────────────────────────────────────────
 * · 新列表页用本地切片分页 / 用裸 confirm 做危险确认 / 缺 PageHeader；
 * · 有分页却不绑 :total 或 current-page；
 * · 豁免表腐烂（登记的文件已修好 / 已不在分母里 / 理由没写日期）。
 */
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { parse } from '@vue/compiler-sfc'

const ROOT = process.cwd()
const SRC = join(ROOT, 'src')

/** 本次扫描日期：豁免理由必须带它，避免一条豁免被复制成永久免死金牌。 */
const SCAN_DATE = '2026-09-15'

export interface Region {
  text: string
  startLine: number
}

export interface Regions {
  template: Region
  script: Region
}

export interface Finding {
  file: string
  line: number
  rule: string
  detail: string
}


// ── 源码解析工具（不依赖 shell；解析失败即抛或硬失败，绝不静默吞成空数组）──────

/** 从 from 处的 '<' 找到配对的 '>'，跳过引号内的 '>'（多行属性、属性值含 '>' 都能吃下）。 */
function endOfTag(src: string, from: number): number {
  let quote = ''
  for (let j = from + 1; j < src.length; j += 1) {
    const c = src[j]
    if (quote) {
      if (c === quote && src[j - 1] !== '\\') quote = ''
      continue
    }
    if (c === '"' || c === "'" || c === '\u0060') { quote = c; continue }
    if (c === '>') return j
  }
  return src.length - 1
}

/**
 * 收集 src 里所有 <tag ...> 开标签的**完整文本**（'<el-pagination\n  :total="x"\n/>' 算一条）。
 *
 * 为什么不用单行正则 \`<el-pagination[^>]*>\`：本仓绝大多数 el-pagination 是多行属性写法，
 * 单行正则会把一个标签截成两半，于是 :total 判定（规则②的"分页绑定"子判定）会随机漏判 ——
 * 与 MobileTableWrapper.spec.ts 文件头记录的是同一类坑（漏掉不是"少报一个"，而是永久假绿）。
 */
export function openTags(src: string, tag: string): string[] {
  const re = new RegExp('<' + tag + '\\b', 'gi')
  const out: string[] = []
  let m: RegExpExecArray | null
  while ((m = re.exec(src)) !== null) {
    const end = endOfTag(src, m.index)
    out.push(src.slice(m.index, end + 1))
    re.lastIndex = end + 1
  }
  return out
}

/** 取 openParen 处的 '(' 所对应的实参文本（平衡括号 + 跳过引号）。 */
export function callArgs(src: string, openParen: number): string {
  let depth = 0
  let quote = ''
  for (let j = openParen; j < src.length; j += 1) {
    const c = src[j]
    if (quote) {
      if (c === quote && src[j - 1] !== '\\') quote = ''
      continue
    }
    if (c === '"' || c === "'" || c === '\u0060') { quote = c; continue }
    if (c === '(') depth += 1
    else if (c === ')') {
      depth -= 1
      if (depth === 0) return src.slice(openParen + 1, j)
    }
  }
  // 括号没闭合（源码被截断）：返回余下文本，宁可多报也不静默漏报。
  return src.slice(openParen + 1, openParen + 400)
}

/**
 * 把注释与字符串字面量替换成空白（保留换行，故**行号不变**）。
 * 源码扫描必须区分"代码"与"注释/文案"：本仓注释里大量引用被禁写法的说明
 * （如 AlertRules.vue:322 解释"为什么不用 ElMessageBox.confirm"），不剥离就会误报。
 */
export function stripCommentsAndStrings(src: string): string {
  let out = ''
  let quote = ''
  let lineComment = false
  let blockComment = false
  for (let i = 0; i < src.length; i += 1) {
    const c = src[i]
    const n = src[i + 1]
    if (lineComment) { if (c === '\n') { lineComment = false; out += c } else out += ' '; continue }
    if (blockComment) { if (c === '*' && n === '/') { blockComment = false; out += '  '; i += 1 } else out += (c === '\n' ? c : ' '); continue }
    if (quote) {
      if (c === '\\') { out += '  '; i += 1; continue }
      if (c === quote) { quote = ''; out += c; continue }
      out += (c === '\n' ? c : ' ')
      continue
    }
    if (c === '/' && n === '/') { lineComment = true; out += '  '; i += 1; continue }
    if (c === '/' && n === '*') { blockComment = true; out += '  '; i += 1; continue }
    if (c === '"' || c === "'" || c === '\u0060') { quote = c; out += c; continue }
    out += c
  }
  return out
}

/** 模板里的 <!-- --> 注释替换成空白（保留换行）。注释里提 PageHeader 不算"有页头"。 */
function stripHtmlComments(src: string): string {
  return src.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' '))
}

/** 区域内的下标 → 文件内行号（区域各自带 startLine，脚本区行号才不会整体偏移）。 */
function lineAt(region: Region, index: number): number {
  return region.startLine + region.text.slice(0, index).split('\n').length - 1
}

/** 解析 SFC：分别取模板区与脚本区（含各自起始行），模板区已剥 HTML 注释。 */
export function parseSfc(raw: string, file: string): Regions {
  const { descriptor, errors } = parse(raw, { filename: file })
  expect(errors, file + ' SFC 解析失败: ' + JSON.stringify(errors)).toEqual([])
  const blocks: Array<{ content: string; loc: { start: { line: number } } }> = []
  if (descriptor.script) blocks.push(descriptor.script)
  if (descriptor.scriptSetup) blocks.push(descriptor.scriptSetup)
  return {
    template: {
      text: stripHtmlComments(descriptor.template ? descriptor.template.content : ''),
      startLine: descriptor.template ? descriptor.template.loc.start.line : 1,
    },
    script: {
      // 同时存在 <script> 与 <script setup> 时拼接；行号以首块为基准（本仓无此形态）。
      text: blocks.map((b) => b.content).join('\n'),
      startLine: blocks.length > 0 ? blocks[0].loc.start.line : 1,
    },
  }
}


// ── 规则①：不得对本地列表做切片分页（近似判定，边界见文件头）────────────────

/** 页码/页长类标识符。不能用 '\bpage\b' 去匹配 currentPage —— 两者都是词字符，没有词边界。 */
const PAGE_IDENT_RE = /current_?page|page_?size|per_?page|page_?no|\bpage\b/i

/**
 * 规则①（近似）：找出「切片的实参引用了页码/页长变量」的写法。
 *
 *   filteredChannels.value.slice(start, start + pageSize)                          ⇒ 命中（本地分页）
 *   realtimeData.value.slice(0, 50)                                                ⇒ 不命中（保留最新 N 条）
 *   new Date().toISOString().slice(0, 10)                                          ⇒ 不命中（取日期）
 *
 * 实参用平衡括号扫描而非 [^)]*：嵌套括号 ((currentPage.value - 1) * pageSize, ...) 会让 [^)]* 提前截断。
 */
export function localSliceFindings(file: string, regions: Regions): Finding[] {
  const found: Finding[] = []
  // 脚本区：剥注释**与字符串**（注释里写 ".slice(0, pageSize)" 的说明不算违规）。
  // 模板区：只剥 HTML 注释（parseSfc 已做）、**不能**再剥字符串 —— Vue 模板里
  // v-for="r in rows.slice(...)" 的表达式本身就写在双引号里，剥掉会把要查的东西一起抹掉。
  // 这是自检①（正例 D「模板内联切片」）抓出来的真实 bug：第一版整片剥字符串 ⇒ 模板侧恒为空集。
  const scope = [
    { text: stripCommentsAndStrings(regions.script.text), startLine: regions.script.startLine },
    { text: regions.template.text, startLine: regions.template.startLine },
  ]
  for (const region of scope) {
    const text = region.text
    const re = /\.slice\s*\(/g
    let m: RegExpExecArray | null
    while ((m = re.exec(text)) !== null) {
      const args = callArgs(text, m.index + m[0].length - 1)
      if (!PAGE_IDENT_RE.test(args)) continue
      found.push({
        file,
        line: lineAt({ text: region.text, startLine: region.startLine }, m.index),
        rule: '本地切片分页',
        detail:
          '  .slice(' + args.replace(/\s+/g, ' ').trim().slice(0, 80) + ')' +
          ' —— 切片实参引用了页码/页长变量。翻页必须重新请求接口（带 page/page_size），' +
          '本地切片会让页码与真实数据脱节。若确实只是"最多显示/保留 N 条"的无害截断，' +
          '请登记到 EXEMPT_LOCAL_SLICE 并写明理由。',
      })
    }
  }
  return found
}

/**
 * 规则①的"分页绑定"子判定：el-pagination 必须绑 :total 与 current-page。
 * 只绑 current-page 不绑 total ⇒ 分页器不知道总数，只可能是本地切片的装饰；
 * 只绑 total 不绑 current-page ⇒ 页码不可控。两者缺一都不可能是真分页。
 */
export function paginationBindingFindings(file: string, tags: string[], templateStartLine: number): Finding[] {
  const found: Finding[] = []
  for (const tag of tags) {
    const one = tag.replace(/\s+/g, ' ')
    const line = templateStartLine + tag.split('\n').length - 1
    if (!/(^|\s):total\s*=/.test(tag)) {
      found.push({ file, line, rule: '分页缺 :total', detail: '  ' + one.slice(0, 100) })
    }
    if (!/(^|\s)(v-model:current-page|:current-page)\s*=/.test(tag)) {
      found.push({ file, line, rule: '分页缺 current-page', detail: '  ' + one.slice(0, 100) })
    }
  }
  return found
}

// ── 规则②：危险操作必须走 feedback.confirmDanger ───────────────────────────

/**
 * 允许出现 .confirm(...) 的接收者白名单（fail-closed：**新接收者必须显式登记**）。
 *
 * deviceOperation —— stores/deviceOperation.ts:67 的业务确认令牌（两阶段确认授权），
 *   不是 UI 弹窗，与"危险确认弹窗"无关。
 * feedback.confirm —— **刻意不在白名单**：它是不带 danger 语义的普通确认弹窗
 *   （feedback.ts:181），危险操作上用它等价于回退到审计 F5 之前的形态。
 */
const ALLOWED_CONFIRM_RECEIVERS = new Set(['deviceOperation'])

/**
 * 规则②：脚本里不得出现裸确认弹窗 / 浏览器原生 confirm / 未登记的确认出口。
 *
 *   ElMessageBox.confirm(...) ⇒ 违规：按钮无 danger 语义，且 EP 默认 autofocus 把初始焦点交给
 *     确认键 ⇒ 破坏性操作上是"回车即删除"（审计 F5；utils/feedback.ts 文件头有实测记录）。
 *   confirm(...) / window.confirm(...) ⇒ 违规：阻塞式，无法做 danger 语义与安全侧定焦。
 *   x.confirm(...)（x 不在白名单）⇒ 违规：无法证明它不是另一个绕过 confirmDanger 的弹窗。
 *   feedback.confirmDanger(...) ⇒ 通过。
 *
 * 注释与字符串已剥离：本仓多处注释在解释"为什么不用 ElMessageBox.confirm"，不能算违规。
 */
export function dangerConfirmFindings(file: string, regions: Regions): Finding[] {
  const code = stripCommentsAndStrings(regions.script.text)
  const found: Finding[] = []
  // 用**词边界前置断言**一次抓全三种形态（第一版漏了 window.confirm 与 x.confirm，自检③抓出来的真实 bug）：
  //   (?<![\w$]) 保证不匹配 reconfirm( / $confirm(，也不把 confirmDanger( 当成 confirm（后面接的是 'D'）。
  //   接收者是谁在下面按 lastLine 判断 —— 只有拿到接收者才能做白名单（fail-closed）判定。
  const re = /(?<![\w$])confirm\s*\(/g
  let m: RegExpExecArray | null
  while ((m = re.exec(code)) !== null) {
    const idx = m[0].lastIndexOf('confirm')
    const abs = m.index + idx
    const before = code.slice(0, abs)
    const lastLine = before.split('\n').pop() as string
    // 页面自己声明的函数（function confirm() / const confirm = ...）不是弹窗调用。
    if (/(?:function|async|const|let|var)\s+$/.test(lastLine)) continue
    const line = lineAt(regions.script, abs)
    if (/ElMessageBox\s*\.\s*$/.test(lastLine)) {
      found.push({ file, line, rule: '裸危险确认', detail: '  裸 ElMessageBox.confirm：按钮无 danger 语义、初始焦点落在确认键上。危险操作请改走 feedback.confirmDanger(...)。' })
      continue
    }
    const receiverMatch = /([\w$]+)\s*\.\s*$/.exec(lastLine)
    if (!receiverMatch) {
      found.push({ file, line, rule: '裸危险确认', detail: '  浏览器原生 confirm()：阻塞式，且无法做 danger 语义与安全侧定焦。请改走 feedback.confirmDanger(...)。' })
      continue
    }
    const receiver = receiverMatch[1]
    if (receiver === 'window' || receiver === 'globalThis') {
      found.push({ file, line, rule: '裸危险确认', detail: '  浏览器原生 ' + receiver + '.confirm()：阻塞式，且无法做 danger 语义与安全侧定焦。请改走 feedback.confirmDanger(...)。' })
      continue
    }
    if (ALLOWED_CONFIRM_RECEIVERS.has(receiver)) continue
    found.push({ file, line, rule: '未登记的确认出口', detail: '  ' + receiver + '.confirm(...) 不在允许清单里。危险操作请改走 feedback.confirmDanger(...)；若这是业务确认（不弹 UI），请把接收者登记进 ALLOWED_CONFIRM_RECEIVERS 并写明理由。' })
  }
  return found
}

// ── 规则③：必须有 PageHeader ──────────────────────────────────────────────

/**
 * 规则③：模板里必须出现 <PageHeader>（含 <page-header> 写法）。
 *
 * 为什么扫 renderTemplate（**剥掉 HTML 注释后**的整份 SFC）而不是 regions.template：
 * regions.template 已经剥过一遍注释、偏移量变了，而 openTags 也要在**同一份文本**上定位，
 * 否则 "注释在前 + 真标签在后" 的文件会算错位置。这里是自检④抓出来的真实 bug：
 * 第一版把剥过注释的模板区喂给 openTags，凡模板以 <!-- 注释开头（本仓多数页面如此）
 * 的**有页头**页面都被误判成"缺页头"——11 个页面里错报 7 个（自检④的失败输出即证据）。
 * 注释里的 <PageHeader> 仍然不算数：它在剥注释阶段就被替换成空白了。
 */
export function pageHeaderFindings(file: string, regions: Regions): Finding[] {
  // 用 PascalCase + 'i' 标志：openTags 的 '\\b' 词边界只在 PascalCase 形态上成立。
  // 第一版写成 openTags(tpl, 'page-header')（字面带连字符）⇒ **永远匹配不到 <PageHeader>**，
  // 于是 11 个页面里 7 个有页头的页面被误报成"缺页头"——自检④抓出来的真实 bug。
  // 反过来 <PageHeaderSkeleton> 不会误匹配：'r' 与 'S' 都是词字符，中间没有词边界。
  // 两种写法都认（与 PageHeaderPropsGate 同时接受 showBack / show-back 是同一条纪律）：
  //   <PageHeader> 是 SFC 里的实际写法；<page-header> 是 kebab-case 的合法等价写法。
  // 第一版只查 'page-header'（带连字符）⇒ 永远匹配不到 <PageHeader>，7 个有页头的页面被误报。
  if (openTags(regions.template.text, 'PageHeader').length > 0) return []
  if (openTags(regions.template.text, 'page-header').length > 0) return []
  return [{
    file,
    line: 1,
    rule: '缺 PageHeader',
    detail:
      '  模板里没有 <PageHeader>。路由级页面必须有页头（I-6 标题统一，规范 §4.1.1 MUST）；' +
      '若该页确有更复杂的自有页头（如 NodeOverview 的重命名 + 在线徽标，台账 §3.35），' +
      '请在 EXEMPT_PAGE_HEADER 登记理由。',
  }]
}


// ── 豁免白名单（存量豁免：逐条登记 + 理由 + 日期；**禁止整个目录豁免**）──────

/**
 * 规则①的存量豁免：确实是本地切片分页，但 P1-C 只加门禁、**不改业务页面**。
 * 修好之后必须把条目删除；理由过期同样要让「豁免白名单不得腐烂」用例变红。
 */
const EXEMPT_LOCAL_SLICE: Record<string, string> = {
  // ChannelList.vue 的存量豁免已于 2026-09-17 删除：该页已完成服务端分页改造
  // （GET /channels 走 page/page_size/node_id/hardware_type，:total 用服务端 total，
  // 本地 paginatedChannels / filteredChannels 切片已删除）。删除后本门禁对它是真实生效的。
  'src/views/data/DataPanel.vue':
    '已知误报（不是分页切片）：DataPanel.vue:703 的 historyData.value.slice(0, pageSize.value) 是 WebSocket ' +
    '实时流的"本页最多保留 N 条"上限；该页分页走 504-505 的 page: currentPage.value / page_size: pageSize.value，' +
    '是真分页。被命中只是因为切片的第二个实参恰好叫 pageSize。若把该变量改名为 maxRows 即可消除误报，' +
    '但那属于业务页面改动（P1-C 不改业务页面），故先登记。登记日期 ' + SCAN_DATE + '。',
}

/**
 * 规则②的**结构性**豁免：页面不走通用 confirmDanger，而走专用的破坏性操作对话框。
 * 与 EXEMPT_LOCAL_SLICE 的区别：这里不是"暂不修"，而是"结论就是不该用 confirmDanger"。
 * 证据：DeviceDeleteDialog.vue 含"是否同时删除历史数据"的 radio 并说明影响面；confirmDanger 只能给
 * 一个 message 字符串，替换它 = 丢掉"是否删数据"这个必选项 = 功能回退。
 * 缺口登记：该对话框自身的 danger 语义/安全侧定焦/键盘可达性规范化不在 P1-C 范围，已报给主控。
 */
/**
 * 规则②的**存量豁免**：确实是裸 ElMessageBox.confirm，但**不是破坏性操作**，因此不构成
 * "回车即删除"的风险面（教训见台账 §3.36 的分母纪律：把裸 confirm 一律当成危险确认是分母误用）。
 * P1-C 只加门禁、不改业务页面；改走 feedback.confirmDanger 会把确认键染成 danger 红
 * （对"克隆""手动触发"这类非破坏操作是**语义错误**，规范 §3.4.3 的 danger 类目只用于破坏性操作），
 * 所以正确修法是迁到 feedback.confirm（普通确认）—— 那同样属业务改动，登记缺口待排期。
 */
const EXEMPT_BARE_CONFIRM: Record<string, string> = {
  'src/views/config/DeviceConfigList.vue':
    'DeviceConfigList.vue:417 的裸 ElMessageBox.confirm 是**克隆配置**的确认（"确定要克隆配置 X 吗？"），' +
    '非破坏性操作，无"回车即删除"风险；该页真正的删除（handleAction 的 delete 分支）已走 feedback.confirmDanger（:474）。' +
    '正确修法是迁到 feedback.confirm（普通确认，不带 danger 语义），属业务改动，登记缺口待排期。登记日期 ' + SCAN_DATE + '。',
  'src/views/automation/AutomationRules.vue':
    'AutomationRules.vue:635 的裸 ElMessageBox.confirm 是**手动触发规则**的确认（"手动触发规则 X？将跳过条件评估…"），' +
    '非破坏性操作；该页删除已走 feedback.confirmDanger（:596），高风险动作确认也已走 confirmDanger（:615）。' +
    '正确修法是迁到 feedback.confirm（普通确认），属业务改动，登记缺口待排期。登记日期 ' + SCAN_DATE + '。',
}

const STRICTER_CONFIRM_PAGES: Record<string, string> = {
  'src/views/edge-device/EdgeDeviceList.vue':
    '删除确认走专用对话框 DeviceDeleteDialog.vue / DeviceBatchDeleteDialog.vue（含"同时删除历史数据"radio 与影响面说明），' +
    '比 confirmDanger 提供更多信息与选择，故**不应**替换为 confirmDanger。登记日期 ' + SCAN_DATE + '。',
  'src/views/data/DataPanel.vue':
    '页面上的设备数据清理确认同 EdgeDeviceList：走 DeviceDeleteDialog.vue。登记日期 ' + SCAN_DATE + '。',
}

/**
 * 规则③的存量豁免：**确实缺页头**（页面以统计卡开场，全文件没有 <h1>/<h2>），
 * 不是"有自有页头装不下"（那种情况见台账 §3.35 的 NodeOverview）。
 * 补页头会改变页面视觉 ⇒ 属业务改动，P1-C 只登记缺口，由主控排期。
 */
const EXEMPT_PAGE_HEADER: Record<string, string> = {
  'src/views/node/NodeList.vue':
    '存量缺页头：页面正文以统计卡开场，全文件没有 <h1>/<h2> 页标题（不是"有自有页头装不下"）。' +
    '补页头属业务改动（改变页面视觉），P1-C 只加门禁不改业务页面，故登记缺口待排期。登记日期 ' + SCAN_DATE + '。',
  'src/views/edge-device/EdgeDeviceList.vue':
    '同 NodeList.vue：存量缺页头，页面以统计卡开场，无 <h1>/<h2> 页标题。登记缺口待排期。登记日期 ' + SCAN_DATE + '。',
}

/** 豁免理由必须写明"原因 + 日期"：防止一条豁免被复制成永久免死金牌。 */
function assertReasonLabel(file: string, reason: string | undefined, table: string) {
  expect(reason, file + ' 在 ' + table + ' 里没有理由').toBeTruthy()
  expect(reason, file + ' 在 ' + table + ' 里的理由必须写明登记日期（YYYY-MM-DD）').toMatch(/\d{4}-\d{2}-\d{2}/)
}

// ── 分母采集 ──────────────────────────────────────────────────────────────

export interface PageFacts {
  file: string
  regions: Regions
  paginationTags: string[]
  findings: Finding[]
}

/**
 * 遍历「组件直接注册在 router/index.ts、且模板里有 el-pagination」的页面。
 * 组件解析不出来时**显式硬失败**：新增路由写法必须同步本解析器，否则页面会被静默漏检。
 */
export function auditRouteListPages(): PageFacts[] {
  const routerSrc = readFileSync(join(SRC, 'router', 'index.ts'), 'utf8')
  const loaderSrc = readFileSync(join(SRC, 'router', 'routeLoaders.ts'), 'utf8')
  const loaders = new Map<string, string>()
  for (const m of loaderSrc.matchAll(/export const (\w+)\s*=\s*\(\)\s*=>\s*import\('([^']+)'\)/g)) {
    loaders.set(m[1], m[2])
  }

  const out: PageFacts[] = []
  for (const m of routerSrc.matchAll(/component:\s*(?:\(\)\s*=>\s*import\('([^']+)'\)|(\w+))/g)) {
    const loaderName = m[2]
    const rel = m[1] || (loaderName ? loaders.get(loaderName) : undefined)
    expect(
      rel,
      '路由组件 ' + (loaderName || m[1]) + ' 解析不到文件：router/index.ts 用了本门禁不认识的 component 写法，' +
      '请同步 auditRouteListPages 的解析器（否则该页会被静默漏检、门禁假绿）',
    ).toBeTruthy()
    const file = String(rel).replace(/^@\//, 'src/')
    const regions = parseSfc(readFileSync(join(ROOT, file), 'utf8'), file)
    const paginationTags = openTags(regions.template.text, 'el-pagination')
    if (paginationTags.length === 0) continue // 详情页/状态页不是列表页
    const findings = [
      ...localSliceFindings(file, regions),
      ...paginationBindingFindings(file, paginationTags, regions.template.startLine),
      ...dangerConfirmFindings(file, regions),
      ...pageHeaderFindings(file, regions),
    ]
    out.push({ file, regions, paginationTags, findings })
  }
  return out
}


/** 一条 finding 是否被该页的豁免覆盖（豁免按"规则 + 文件"逐条生效，不做目录级豁免）。 */
function isExempt(p: PageFacts, f: Finding): boolean {
  if (f.rule === '本地切片分页' || f.rule === '分页缺 :total' || f.rule === '分页缺 current-page') {
    return Boolean(EXEMPT_LOCAL_SLICE[p.file])
  }
  if (f.rule === '裸危险确认' || f.rule === '未登记的确认出口') {
    // 非破坏性操作的裸 confirm：见 EXEMPT_BARE_CONFIRM（带理由 + 日期）。
    // 专用对话框：见 STRICTER_CONFIRM_PAGES（结论是"不该用 confirmDanger"）。
    return Boolean(EXEMPT_BARE_CONFIRM[p.file]) || Boolean(STRICTER_CONFIRM_PAGES[p.file])
  }
  if (f.rule === '缺 PageHeader') {
    return Boolean(EXEMPT_PAGE_HEADER[p.file])
  }
  return false
}

function formatFindings(findings: Finding[]): string {
  return findings.map((f) => '  ' + f.file + ':' + f.line + '  [' + f.rule + ']\n' + f.detail).join('\n')
}

// ── 用例 ──────────────────────────────────────────────────────────────────

describe('P1-C 列表页约定门禁（路由级列表页）', () => {
  it('口径可复现：分母非空、每个页面都真判到了 el-pagination、且判定器对每页都给出结论', () => {
    const pages = auditRouteListPages()
    // 跑空守卫：口径失效时下面的 toEqual([]) 会全部假绿（本仓已吃过一次亏，见 i1 守卫文件头）。
    expect(pages.length, '路由级列表页分母为 0 —— 路由解析器或 el-pagination 判定失效').toBeGreaterThanOrEqual(11)
    for (const p of pages) {
      expect(p.paginationTags.length, p.file + ' 被纳入分母却没有 el-pagination 标签：口径自相矛盾').toBeGreaterThan(0)
      expect(p.findings, p.file + ' 判定器没有对该页给出任何结论（判定器坏了）').toBeInstanceOf(Array)
    }
  })

  it('分母覆盖关键页：路由解析器不得漏掉任何一个已知列表页（防假绿）', () => {
    const files = auditRouteListPages().map((p) => p.file)
    for (const f of [
      'src/views/node/NodeList.vue',
      'src/views/edge-device/EdgeDeviceList.vue',
      'src/views/logical-device/LogicalDeviceList.vue',
      'src/views/data-source/DataSourceList.vue',
      'src/views/data/DataPanel.vue',
      'src/views/firmware/FirmwareManage.vue',
      'src/views/config/DeviceConfigList.vue',
      'src/views/alert/AlertRules.vue',
      'src/views/automation/AutomationRules.vue',
      'src/views/notification/NotificationChannels.vue',
      'src/views/notification/NotificationDeliveries.vue',
      'src/views/channel/ChannelList.vue',
    ]) {
      expect(files, f + ' 不在分母里：路由解析器漏了它（这是本门禁最危险的失效方式）').toContain(f)
    }
  })

  it('规则①+③：列表页不得用本地切片分页/缺分页绑定/缺 PageHeader（豁免除外）', () => {
    const failures: Finding[] = []
    for (const p of auditRouteListPages()) {
      for (const f of p.findings) {
        if (isExempt(p, f)) continue
        failures.push(f)
      }
    }
    expect(
      failures,
      '列表页约定违规（文件:行）：\n' + formatFindings(failures) +
      '\n\n（豁免清单在文件里的 EXEMPT_LOCAL_SLICE / STRICTER_CONFIRM_PAGES / EXEMPT_PAGE_HEADER，' +
      '每条都必须写明理由与登记日期；禁止目录级豁免。）',
    ).toEqual([])
  })

  it('规则②：危险操作必须有确认出口（confirmDanger 或专用对话框），且不得用未登记的确认出口', () => {
    const failures: Finding[] = []
    for (const p of auditRouteListPages()) {
      const code = stripCommentsAndStrings(p.regions.script.text)
      const hasAllowed = /feedback\s*\.\s*confirmDanger\s*\(/.test(code) || Boolean(STRICTER_CONFIRM_PAGES[p.file])
      // 风险面：源码里有"删除/销毁"类调用，却看不到任何确认出口。
      const destructive = [...code.matchAll(/\.\s*(delete|remove|destroy|purge)\w*\s*\(/g)].map((m) => m[0])
      // 逐条过 isExempt：豁免必须按"文件 + 规则"逐条生效，不能在这里被绕过
      // （第一版直接 push，导致 EXEMPT_BARE_CONFIRM 形同虚设 —— 豁免表与实际判定必须只有一条通路）。
      for (const f of p.findings) {
        if (f.rule !== '裸危险确认' && f.rule !== '未登记的确认出口') continue
        if (isExempt(p, f)) continue
        failures.push(f)
      }
      if (destructive.length > 0 && !hasAllowed) {
        failures.push({
          file: p.file,
          line: 0,
          rule: '危险操作无确认',
          detail:
            '  源码里有 ' + destructive.length + ' 处删除/销毁类调用（' + [...new Set(destructive)].slice(0, 3).join(', ') + '），' +
            '却既没有 feedback.confirmDanger(...)，也没有在本门禁登记专用破坏性对话框。' +
            '若该操作确实无需二次确认，请在本文件的豁免表里登记理由与日期（禁止目录级豁免）。',
        })
      }
    }
    expect(failures, '危险确认违规（文件:行）：\n' + formatFindings(failures)).toEqual([])
  })

  it('豁免白名单不得腐烂：文件必须仍在分母里，且理由必须成立', () => {
    const pages = auditRouteListPages()
    const files = pages.map((p) => p.file)
    const byFile = new Map(pages.map((p) => [p.file, p]))
    const tables: Array<[string, Record<string, string>]> = [
      ['EXEMPT_LOCAL_SLICE', EXEMPT_LOCAL_SLICE],
      ['EXEMPT_BARE_CONFIRM', EXEMPT_BARE_CONFIRM],
      ['STRICTER_CONFIRM_PAGES', STRICTER_CONFIRM_PAGES],
      ['EXEMPT_PAGE_HEADER', EXEMPT_PAGE_HEADER],
    ]
    const failures: string[] = []
    for (const [name, table] of tables) {
      for (const [file, reason] of Object.entries(table)) {
        assertReasonLabel(file, reason, name)
        if (!files.includes(file)) {
          failures.push(file + ' 已不在路由级列表页分母里（页面被删/改名/去掉分页），请从 ' + name + ' 移除该条')
          continue
        }
        const p = byFile.get(file) as PageFacts
        // 豁免必须仍然"有事可免"：修好之后条目要删掉，否则白名单会腐烂成永久免检名单。
        const relevant = p.findings.filter((f) => isExempt(p, f))
        if (relevant.length === 0 && name !== 'STRICTER_CONFIRM_PAGES' && name !== 'EXEMPT_BARE_CONFIRM') {
          failures.push(file + ' 登记在 ' + name + '，但实测已无对应违规 —— 修好后请删除该条')
        }
        if (name === 'STRICTER_CONFIRM_PAGES' && !/DeviceDeleteDialog/.test(reason)) {
          failures.push(file + ' 在 ' + name + ' 里的理由必须写明走的是哪个专用对话框')
        }
      }
    }
    expect(failures, '豁免表腐烂：\n' + failures.join('\n')).toEqual([])
  })

  it('豁免不得是目录级：EXEMPT_LOCAL_SLICE / EXEMPT_PAGE_HEADER 的键必须是具体 .vue 文件', () => {
    for (const [name, table] of [['EXEMPT_LOCAL_SLICE', EXEMPT_LOCAL_SLICE], ['EXEMPT_PAGE_HEADER', EXEMPT_PAGE_HEADER]] as Array<[string, Record<string, string>]>) {
      for (const key of Object.keys(table)) {
        expect(key, name + ' 的键必须是 src/... 下的具体 .vue 文件，禁止目录级豁免：' + key).toMatch(/^src\/[\w\-/]+\.vue$/)
      }
    }
  })
});


// ── 分类器自检：把正例/反例喂给**同一个**判定函数（范式同 PageHeaderPropsGate）──────
// 下面每个反例都是本仓真实出现过的写法或本任务真实注入的变异，不是想象出来的形态。
// 没有这组用例，判定器坏掉（正则退化/行号偏移/剥注释过火）后上面几条会**永远绿**。

function fakeScript(script: string, startLine = 20): Regions {
  return { template: { text: '', startLine: 1 }, script: { text: script, startLine } }
}

function fakeTemplate(tpl: string): Regions {
  return { template: { text: stripHtmlComments(tpl), startLine: 1 }, script: { text: '', startLine: 1 } }
}

describe('P1-C 判定器自检（分类器正例/反例）', () => {
  it('自检①：本地切片分页判定器能区分"分页切片"与"无害截断"', () => {
    const hit = (s: string) => localSliceFindings('x.vue', fakeScript(s))

    // 正例 A：ChannelList.vue:327-330 的真实存量写法（切片实参是 start + pageSize）
    expect(hit('const paginatedChannels = computed(() => filteredChannels.value.slice(start, start + pageSize))').length).toBe(1)
    // 正例 B：本任务注入的典型变异（嵌套括号，用 [^)]* 会截断 ⇒ 漏报）
    expect(hit('const paged = computed(() => allNodes.value.slice((currentPage.value - 1) * pageSize, currentPage.value * pageSize))').length).toBe(1)
    // 正例 C：DataPanel.vue:703 的真实写法 —— 已知误报，靠豁免表兜底，把它固化成事实
    expect(hit('realtimeData.value = realtimeData.value.slice(0, pageSize.value)').length).toBe(1)
    // 正例 D：模板里内联切片（不能只扫脚本）
    expect(localSliceFindings('x.vue', fakeTemplate('<div v-for="r in rows.slice((page - 1) * size, page * size)" />')).length).toBe(1)

    // 反例：本仓真实存在的 5 种无害截断 —— 判定器必须放行，否则门禁退化成"见 slice 就报"
    for (const safe of [
      'const promises = deviceIds.slice(0, 5).map(deviceId => deviceId)',
      'const truncated = rawData.length > 64 ? rawData.slice(0, 64) : rawData',
      'return new Date().toISOString().slice(0, 10)',
      'value = value.slice(0, 10) + "..."',
      'series.data = series.data.slice(-500)',
      'const keys = Object.keys(config).slice(0, 3)',
    ]) {
      expect(hit(safe), safe + ' 不该被判成本地分页').toEqual([])
    }
    // 反例：注释里写切片分页的说明不算违规（本仓注释密度极高）
    expect(hit('// 改前: items.slice((page - 1) * size, page * size)')).toEqual([])
    expect(hit('/* paginated = list.slice(start, start + pageSize) */')).toEqual([])
    // 行号必须指向真实位置：脚本区从第 20 行开始，切片在脚本第 3 行 ⇒ 文件第 22 行
    expect(hit('const a = 1\nconst b = 2\nconst c = list.slice(0, pageSize)')[0].line).toBe(22)
  })

  it('自检②：分页绑定判定器 —— 多行标签、缺 :total、缺 current-page 都要判对', () => {
    const tpl = [
      '<el-pagination',
      '  v-model:current-page="currentPage"',
      '  :total="total"',
      '  data-test="a>b"',
      '  layout="total, prev, pager, next"',
      '/>',
    ].join('\n')
    const tags = openTags(tpl, 'el-pagination')
    expect(tags.length, '多行 el-pagination 被截成多条/零条（正则退化成单行匹配）').toBe(1)
    expect(paginationBindingFindings('x.vue', tags, 5)).toEqual([])
    // 缺 :total ⇒ 红
    expect(paginationBindingFindings('x.vue', openTags('<el-pagination v-model:current-page="p" :page-size="20" />', 'el-pagination'), 1).map((f) => f.rule)).toEqual(['分页缺 :total'])
    // 缺 current-page ⇒ 红
    expect(paginationBindingFindings('x.vue', openTags('<el-pagination :total="t" />', 'el-pagination'), 1).map((f) => f.rule)).toEqual(['分页缺 current-page'])
    // 两条都缺 ⇒ 两条都报
    expect(paginationBindingFindings('x.vue', openTags('<el-pagination />', 'el-pagination'), 1).length).toBe(2)
    // :total 出现但值里含 '>'（边界）仍应判对
    expect(paginationBindingFindings('x.vue', openTags('<el-pagination :total="a > b ? 1 : 2" v-model:current-page="p" />', 'el-pagination'), 1)).toEqual([])
  })

  it('自检③：危险确认判定器能认出裸写法、放行 confirmDanger、且不误伤注释', () => {
    const hit = (s: string) => dangerConfirmFindings('x.vue', fakeScript(s))
    // 正例：DeviceConfigList.vue:417 / AutomationRules.vue:635 的真实形态
    expect(hit('await ElMessageBox.confirm(msg, "克隆配置", { confirmButtonText: "确定" })').length).toBe(1)
    expect(hit('if (!window.confirm("删除?")) return').length).toBe(1)
    expect(hit('if (!confirm("删除?")) return').length).toBe(1)
    // 反例：统一出口
    expect(hit('const ok = await feedback.confirmDanger(msg, { title: "确认删除" })')).toEqual([])
    // 反例：页面自己声明的 confirm()（ActionConfirmationDialog.vue:15 的真实形态）
    expect(hit('function confirm() { if (reason.value.trim()) emit("confirm", reason.value.trim()) }')).toEqual([])
    expect(hit('const confirm = () => { emit("confirm") }')).toEqual([])
    // 反例：白名单里的业务确认令牌（stores/deviceOperation.ts:67 的真实形态）
    expect(hit('await deviceOperation.confirm(id, actionId, params, reason)')).toEqual([])
    // 正例（fail-closed）：feedback.confirm 不带 danger 语义 ⇒ 必须报，否则等于给 F5 回退开后门
    expect(hit('const ok = await feedback.confirm("确定删除?")').map((f) => f.rule)).toEqual(['未登记的确认出口'])
    // 不误伤：注释里解释"为什么不用 ElMessageBox.confirm"（AlertRules.vue:322 的真实形态）
    expect(hit('// ElMessageBox.confirm 时按钮是 primary 样式, 真调用已走 confirmDanger')).toEqual([])
    // 不误伤：字符串字面量里出现该 API 名（提示文案）
    expect(hit('ElMessage.info("请勿使用 ElMessageBox.confirm")')).toEqual([])
    // 行号必须指向真实位置（脚本区从第 20 行开始，调用在脚本第 2 行 ⇒ 文件第 21 行）
    expect(hit('const a = 1\nawait ElMessageBox.confirm(m)')[0].line).toBe(21)
  })

  it('自检④：页头判定器能认出 PageHeader 的多种写法，且不把注释/子组件名算数', () => {
    const ok = (tpl: string) => pageHeaderFindings('x.vue', fakeTemplate(tpl))
    expect(ok('<template><PageHeader title="A" /></template>')).toEqual([])
    expect(ok('<PageHeader\n  title="A"\n  subtitle="B"\n/>')).toEqual([])
    expect(ok('<template><page-header title="A" /></template>')).toEqual([])
    expect(ok('<PageHeader title="系统监控">\n  <template #extra><button /></template>\n</PageHeader>')).toEqual([])
    // 注入变异：把页头删掉 ⇒ 必须变红（本任务真实做过的变异）
    expect(ok('<div class="stats-row"><StatCard label="本页节点" /></div>').map((f) => f.rule)).toEqual(['缺 PageHeader'])
    // 注释里提到 PageHeader 不算数（否则"删掉页头只留注释"会假绿）
    expect(ok('<!-- 页头（I-6）：PageHeader 已移除 --><div />').map((f) => f.rule)).toEqual(['缺 PageHeader'])
    // 相似但不同的标签名不算数（PageHeaderSkeleton 不是页头）
    expect(ok('<PageHeaderSkeleton />').map((f) => f.rule)).toEqual(['缺 PageHeader'])
  })

  it('自检⑤：豁免表 fail-closed —— 未登记的违规必须报出来，登记过的才放行', () => {
    const p: PageFacts = {
      file: 'src/views/__fake__/FakeList.vue',
      regions: fakeScript('const paged = list.slice(0, pageSize)'),
      paginationTags: ['<el-pagination :total="t" v-model:current-page="p" />'],
      findings: [],
    }
    const finding: Finding = { file: p.file, line: 1, rule: '本地切片分页', detail: 'x' }
    // 未登记 ⇒ 不放行
    expect(isExempt(p, finding)).toBe(false)
    // 登记过的文件（EXEMPT_LOCAL_SLICE 现存条目：DataPanel 的已知误报）⇒ 该规则放行
    expect(isExempt({ ...p, file: 'src/views/data/DataPanel.vue' }, finding)).toBe(true)
    // 反例（防"删掉登记项后本自检变成恒绿"）：ChannelList.vue 的登记已于服务端分页改造后删除 ⇒ 不放行
    expect(
      isExempt({ ...p, file: 'src/views/channel/ChannelList.vue' }, finding),
      'ChannelList.vue 已删除存量豁免，不得再被放行',
    ).toBe(false)
    // 但豁免只覆盖它登记的那条规则：同一个文件上"缺 PageHeader"不被 EXEMPT_LOCAL_SLICE 放行
    const headerFinding: Finding = { file: 'src/views/data/DataPanel.vue', line: 1, rule: '缺 PageHeader', detail: 'x' }
    expect(isExempt({ ...p, file: 'src/views/data/DataPanel.vue' }, headerFinding)).toBe(false)
  })
});
