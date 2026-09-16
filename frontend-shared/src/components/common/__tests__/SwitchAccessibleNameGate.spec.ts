import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

/**
 * el-switch 可访问名门禁（回归锁）。
 *
 * 背景（实测缺陷，2026-09-15）：审计报告 F18 记录「el-switch 无可访问名称」。
 * 逐个数实测：全站 **23 个** `<el-switch>`，其中 **3 个**既没有 `aria-label`
 * 也没有邻近的 `<label>`/`label=` 文本可作可访问名（NodeOverview 的 DMA 绑定开关、
 * DataPanel 的实时数据开关、ChannelPanel 的逐 DMA 开关）。
 *
 * 为什么这类缺陷危险：开关是**二元状态控件**，屏幕阅读器读到的只有「开关，关」——
 * 用户不知道它控制的是**哪一个**设备/通道/参数。而页面上常常同时有多个长得一样的开关
 * （ChannelPanel 用 `v-for` 渲染 N 个 DMA 开关），没有可访问名就等于完全不可用。
 *
 * 为什么必须用「源码扫描」：渲染测试只覆盖有人写了测试的页面；
 * 而这类错误的本质是「任何页面新增一个开关都可能漏掉 aria-label，且**没有任何报错**」
 * （不是 TS 错误、不是运行时异常）。只有全站扫描才能在**新增页面**时立刻发现。
 *
 * 判据口径（与审计报告的"可访问名"语义一致）：一个 `<el-switch>` 有可访问名，当且仅当
 *   ① 自身带 `aria-label`/`:aria-label`，或
 *   ② 它处在某个 `<label>` 元素内（含 `label=`，如 Element Plus 表单 label）。
 * 其余一律判为缺失 —— **fail-closed**：认不出的写法必须报出来让人补，而不是静默放过。
 *
 * 变红条件：
 *   - 新增/修改 el-switch 时漏了 aria-label；
 *   - 有人把已有的 aria-label 删掉。
 */

const SRC_DIR = join(process.cwd(), 'src')

function walk(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === '__tests__') continue
      out.push(...walk(p))
    } else if (name.endsWith('.vue')) {
      out.push(p)
    }
  }
  return out
}

export interface SwitchSite {
  /** 开标签原文（用于失败信息定位与自检） */
  tag: string
  /** 1-based 行号 */
  line: number
  hasAccessibleName: boolean
}

/**
 * 找开标签的结束 `>` 下标，**跳过引号内的 `>`**。
 *
 * 为什么不能直接 `indexOf('>')`：模板里的属性值本身就常含 `>` ——
 * 例如 `:model-value="(localIntervals[cmd.id] ?? 0) > 0"` 与
 * `@change="(v) => onToggle(...)"`（箭头函数）。用朴素 indexOf 会把标签**截断在属性中间**，
 * 于是后面的 aria-label 看不到，门禁报出**假缺失**。
 * 本文件初版就踩了这个坑（自检用例 `> 0` 那条把它抓了出来）。
 */
function findTagEnd(source: string, start: number): number {
  let quote: string | null = null
  for (let i = start; i < source.length; i += 1) {
    const ch = source[i]
    if (quote) {
      if (ch === quote) quote = null
      continue
    }
    if (ch === '"' || ch === "'" || ch === '`') {
      quote = ch
      continue
    }
    if (ch === '>') return i
  }
  return -1
}

/**
 * 抽取源码里全部 `<el-switch ...>` 开标签及其可访问名判定。
 * 导出以便**分类器自检**（同一函数喂正例/反例），这是本仓既有范式。
 */
export function switchSitesIn(source: string): SwitchSite[] {
  const sites: SwitchSite[] = []
  const re = /<el-switch\b/g
  let m: RegExpExecArray | null
  while ((m = re.exec(source)) !== null) {
    const start = m.index
    const gt = findTagEnd(source, start)
    if (gt < 0) break
    const tag = source.slice(start, gt + 1)
    const line = source.slice(0, start).split('\n').length
    // ① 自身带 aria-label（静态或绑定）
    const ownAria = /:?aria-label\s*=/.test(tag)
    // ② 处在 <label>…</label> 内：判据是"最近一次 <label 出现在最近一次 </label> 之后"。
    //    这是纯文本近似 —— 它无法处理嵌套，故只在**保守方向**上使用：
    //    只有明确成立时才认为"有名字"，其余一律 fail-closed 要求显式 aria-label。
    //    （Element Plus 的 el-form-item 会渲染出 <label>，所以表单里的开关不必都写 aria-label。）
    const before = source.slice(0, start)
    sites.push({ tag, line, hasAccessibleName: ownAria || hasEnclosingLabelledContainer(before) })
  }
  return sites
}

/**
 * 判定「开关是否处在提供可访问名的容器里」。
 *
 * Element Plus 的 el-form-item label="启用" 会渲染出真实 <label>，
 * 因此表单里的开关不必重复写 aria-label。源码层可判定的等价写法：
 *   ① 自身 aria-label / :aria-label；
 *   ② 处在 <label …> … </label> 内；
 *   ③ 处在带 label="…" 的 Element Plus 表单容器内（el-form-item / el-checkbox / el-radio）。
 * 其余一律 fail-closed 报出。
 */
function hasEnclosingLabelledContainer(before: string): boolean {
  // 同一个坑自检里抓到过一次（本文件初版就是把"最近一次容器开标签"当成"当前还在容器内"）：
  // 那样写会让**容器闭合之后的兄弟节点**也被判为"有名字"，即把缺陷静默放过。
  // 所以每个候选容器都必须"开标签未被它自己的闭标签抵消"才算仍然包着当前开关。
  const balanced = (openTag: string, closeTag: string): boolean => {
    const open = before.lastIndexOf(openTag)
    if (open < 0) return false
    const close = before.lastIndexOf(closeTag)
    return close < open
  }

  if (balanced("<label", "</label>")) return true

  // Element Plus 表单容器：逐个候选查找"最近一次开标签"，并要求它还没被同名闭标签抵消。
  const containers: Array<[string, string]> = [
    ["<el-form-item", "</el-form-item>"],
    ["<el-checkbox", "</el-checkbox>"],
    ["<el-radio", "</el-radio>"],
  ]
  for (const [openTag, closeTag] of containers) {
    if (!balanced(openTag, closeTag)) continue
    const open = before.lastIndexOf(openTag)
    const gt = before.indexOf(">", open)
    if (gt < 0) continue
    if (/label\s*=\s*['"]/.test(before.slice(open, gt + 1))) return true
  }
  return false
}

describe('el-switch 可访问名门禁', () => {
  it('全站每个 el-switch 都有可访问名（aria-label 或带 label 的容器）', () => {
    const offenders: string[] = []
    let scannedFiles = 0
    let scannedSwitches = 0

    for (const file of walk(SRC_DIR)) {
      const src = readFileSync(file, "utf8")
      scannedFiles++
      for (const site of switchSitesIn(src)) {
        scannedSwitches++
        if (site.hasAccessibleName) continue
        offenders.push(
          file.replace(process.cwd() + "/", "") + ":" + site.line + " 的 <el-switch> 没有可访问名" +
            "（屏幕阅读器只会读「开关」，用户不知道它控制哪个对象；请加 aria-label）",
        )
      }
    }

    // 分母守卫：扫描器坏掉时必须是「红」，不能是「绿」（本仓反复强调的假绿模式）
    expect(scannedFiles, "扫描到的 .vue 文件数为 0 —— 扫描路径错了").toBeGreaterThan(20)
    expect(scannedSwitches, "扫到的 el-switch 为 0 —— 正则失效，门禁形同虚设").toBeGreaterThan(10)
    expect(offenders, "缺可访问名的 el-switch:\n" + offenders.join("\n")).toEqual([])
  })

  it('分类器自检：能区分有名字/无名字的写法（防止扫描器坏掉后永远绿）', () => {
    expect(switchSitesIn('<el-switch aria-label="日志流开关" />')[0].hasAccessibleName).toBe(true)
    expect(switchSitesIn('<el-switch :aria-label="x" />')[0].hasAccessibleName).toBe(true)
    expect(switchSitesIn('<label>启用<el-switch v-model="a" /></label>')[0].hasAccessibleName).toBe(true)
    expect(switchSitesIn('<el-form-item label="启用"><el-switch v-model="a" /></el-form-item>')[0].hasAccessibleName).toBe(true)
    // 真正的缺陷形态：裸开关、无提供名字的容器 —— 必须判为缺失
    expect(switchSitesIn('<el-switch v-model="a" />')[0].hasAccessibleName).toBe(false)
    expect(switchSitesIn('<div class="x"><el-switch v-model="a" /></div>')[0].hasAccessibleName).toBe(false)
    // 行号必须准确（失败信息要能直接定位）
    expect(switchSitesIn('<div>\n  <span/>\n  <el-switch v-model="a" />\n</div>')[0].line).toBe(3)
    // 带 label 的容器只对「其后」的开关生效，不能跨兄弟节点误判
    const two = switchSitesIn('<el-form-item label="A"><el-switch v-model="a" /></el-form-item><div><el-switch v-model="b" /></div>')
    expect(two[0].hasAccessibleName).toBe(true)
    expect(two[1].hasAccessibleName).toBe(false)

    // 属性值里含 '>' 时不得把标签截断（否则 aria-label 看不见 ⇒ **假缺失**）。
    // 这两条对应真实写法：`:model-value="(x ?? 0) > 0"` 与 `@change="(v) => f(v)"`。
    const gtInAttr = switchSitesIn('<el-switch :model-value="(a ?? 0) > 0" :aria-label="名称" />')
    expect(gtInAttr[0].hasAccessibleName).toBe(true)
    const arrowInAttr = switchSitesIn('<el-switch @change="(v: boolean) => onToggle(v)" />')
    expect(arrowInAttr[0].hasAccessibleName).toBe(false)

    // 单引号包裹的属性值同样要跳过
    const singleQuoted = switchSitesIn("<el-switch :aria-label='名称' />")
    expect(singleQuoted[0].hasAccessibleName).toBe(true)
  })
})
