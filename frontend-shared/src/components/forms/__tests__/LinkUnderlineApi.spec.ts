import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { resolve, join } from 'node:path'
import loginFormSource from '../LoginForm.vue?raw'

/**
 * el-link 的 `underline` **布尔形态**弃用门禁（Element Plus 2.14.3）。
 *
 * 缺陷形态：`<el-link :underline="false">`。
 * EP 2.14.3 的 link.vue 在 setup 里调用 useDeprecated，条件正是
 * 「props.underline 是布尔」，于是每次渲染都向 console 打一条 ElementPlusError：
 *   [el-link] The underline option (boolean) is about to be deprecated ...
 * 这是**唯一**触发点；字符串形态（"always" | "hover" | "never"）不触发。
 *
 * 语义等价映射（link.vue:34-36 的实现）：
 *   :underline="false" ≡ underline="never"
 *   :underline="true"  ≡ underline="hover"   （注意不是 "always"）
 * 所以修复不能顺手写成 "always" —— 那会把「无下划线」变成「常驻下划线」。
 *
 * ⚠️ 本文件存在的理由：弃用告警只在浏览器控制台可见，单元测试与 vue-tsc 都
 * 完全看不见它（改回布尔绑定时 147 文件全绿）。因此这里用 ?raw 源码断言 +
 * 全仓扫描把 API 形态钉死。
 */

/**
 * 去掉 HTML 注释块。
 *
 * 必要性（本门禁第一版就被它咬过）：源码里的**说明性注释**会原样包含
 * `:underline="false"` 这样的字面量。若不对注释做剥离，
 *   (a) 说明「为什么不能这么写」的注释会被误判为缺陷（假红）；
 *   (b) 更危险的是反向假绿 —— 注释里恰好写着 `underline="never"`，
 *       而真实属性已被改成别的值，`toContain('underline="never"')` 依然通过。
 * 门禁只应对**实际模板标记**负责，所以所有断言一律在剥离注释后的源码上进行。
 */
export function stripHtmlComments(source: string): string {
  return source.replace(/<!--[\s\S]*?-->/g, '')
}

/**
 * 判定一段源码里是否存在 el-link 的布尔 underline 绑定。
 *
 * 只匹配**绑定形态** `:underline="<布尔字面量>"`（冒号绑定）；
 * 字符串形态 `underline="never"`（无冒号）不匹配。
 */
const BOOLEAN_UNDERLINE_BINDING = /:underline\s*=\s*"(true|false)"/

export function hasBooleanUnderlineBinding(source: string): boolean {
  return BOOLEAN_UNDERLINE_BINDING.test(stripHtmlComments(source))
}

/** 递归收集目录下（排除 node_modules / __tests__）的所有 .vue 源文件 */
function collectVueSources(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === '__tests__') continue
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) collectVueSources(full, acc)
    else if (entry.endsWith('.vue')) acc.push(full)
  }
  return acc
}

/** 截取某个标签的开标签（到第一个 '>' 为止），用于「精确到该元素」的断言 */
export function openingTagOf(source: string, tag: string): string {
  const start = stripHtmlComments(source).indexOf('<' + tag)
  if (start < 0) throw new Error('找不到标签: ' + tag)
  const end = stripHtmlComments(source).indexOf('>', start)
  return stripHtmlComments(source).slice(start, end + 1)
}

describe('LinkUnderlineApi：分类器自检（防止正则写错导致永远绿灯/红灯）', () => {
  it('含 :underline="false" 的真实标记样本必须被判为「有布尔绑定」', () => {
    const sample = '<el-link type="primary" :underline="false" @click="x = true">忘记密码？</el-link>'
    expect(hasBooleanUnderlineBinding(sample)).toBe(true)
  })

  it('含 :underline="true" 的真实标记样本必须被判为「有布尔绑定」', () => {
    expect(hasBooleanUnderlineBinding('<el-link :underline="true">x</el-link>')).toBe(true)
  })

  it('字符串形态 underline="never" 的真实标记样本必须判为「无布尔绑定」', () => {
    const sample = '<el-link type="primary" underline="never" @click="x = true">忘记密码？</el-link>'
    expect(hasBooleanUnderlineBinding(sample)).toBe(false)
  })

  it('完全不含 underline 的样本必须判为「无布尔绑定」', () => {
    expect(hasBooleanUnderlineBinding('<el-link type="primary">x</el-link>')).toBe(false)
  })

  it('仅出现在 HTML 注释里的 :underline="false" 不算缺陷（防说明性注释假红）', () => {
    const sample = '<!-- 不能写 :underline="false" -->\n<el-link underline="never">x</el-link>'
    expect(hasBooleanUnderlineBinding(sample)).toBe(false)
  })

  it('注释之外仍有真实布尔绑定时必须判红（防「注释里写着合法写法」蒙混）', () => {
    const sample = '<!-- 应为 underline="never" -->\n<el-link :underline="false">x</el-link>'
    expect(hasBooleanUnderlineBinding(sample)).toBe(true)
  })
})

describe('LoginForm.vue：忘记密码入口的 underline 形态', () => {
  // 一律基于剥离注释后的源码断言：注释里同时提到两种写法，直接用原始 ?raw
  // 会因注释命中而假绿（合法写法被注释"背书"）。
  const markup = stripHtmlComments(loginFormSource)
  const linkTag = openingTagOf(loginFormSource, 'el-link')

  it('真实标记中不含 el-link 布尔 underline 绑定（否则每次渲染都打弃用告警）', () => {
    expect(hasBooleanUnderlineBinding(loginFormSource)).toBe(false)
  })

  it('该 el-link 的开标签上使用与 :underline="false" 语义等价的 underline="never"', () => {
    expect(linkTag).toContain('underline="never"')
    expect(linkTag).not.toMatch(/:underline\s*=/)
  })

  it('不得退回 :underline="false"（精确到该 el-link 的开标签）', () => {
    expect(linkTag).not.toContain(':underline=')
  })

  it('剥离注释确实生效（原始源码里注释含两种写法，剥离后只剩真实属性）', () => {
    // 分母：原始 ?raw 里两种写法都出现，说明这条自检不是空转
    expect(loginFormSource).toContain(':underline="false"')
    expect(loginFormSource).toContain('underline="never"')
    // 剥离后只剩真实的那一个
    expect(markup).toContain('underline="never"')
    expect(markup).not.toContain(':underline="false"')
  })
})

describe('全仓扫描：frontend-shared/src 下所有 .vue', () => {
  const root = resolve(process.cwd(), 'src')
  const files = collectVueSources(root)

  it('扫描器自身有效（找到的 .vue 数量是合理分母，不是空集合假绿）', () => {
    // 若递归/路径写错，files 为空 ⇒ 下面的断言会「全绿」，必须先钉住分母。
    // 阈值口径：本守卫要防的是"扫描器坏了/路径写错 ⇒ 集合为空或严重截断"，
    // **不是**精确分子（那会被正常的删除清理误伤 —— 本轮删 3 个死组件后
    // 实测分母由 80+ 降到正好 80，就撞上了原来的 `> 80`）。
    // 现取 70（留出正常整改余量）：真出问题时集合会掉到个位数或 0，仍会红。
    expect(files.length).toBeGreaterThan(70)
    expect(files.some((f) => f.endsWith(join('forms', 'LoginForm.vue')))).toBe(true)
  })

  it('无任何 el-link 布尔 underline 用法', () => {
    const offenders: string[] = []
    for (const file of files) {
      if (hasBooleanUnderlineBinding(readFileSync(file, 'utf8'))) {
        offenders.push(file.replace(root + '/', ''))
      }
    }
    expect(offenders, '存在 el-link 布尔 underline 用法: ' + offenders.join(', ')).toEqual([])
  })

  it('CSS 里的 text-decoration: underline 不触发本门禁（分类器不误报）', () => {
    const offenders: string[] = []
    for (const file of files) {
      const source = readFileSync(file, 'utf8')
      if (/text-decoration:\s*underline/.test(source) && hasBooleanUnderlineBinding(source)) {
        offenders.push(file)
      }
    }
    // 分母非空：仓里确实存在这些 CSS 用法（否则这条自检是空转）
    expect(files.some((f) => /text-decoration:\s*underline/.test(readFileSync(f, 'utf8')))).toBe(true)
    expect(offenders).toEqual([])
  })
})
