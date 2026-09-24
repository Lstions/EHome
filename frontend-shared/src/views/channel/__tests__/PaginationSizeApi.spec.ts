import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { resolve, join } from 'node:path'
import channelListSource from '@/views/channel/ChannelList.vue?raw'

/**
 * el-pagination 的 `small` **布尔形态**弃用门禁（Element Plus 2.14.3）。
 *
 * 缺陷形态：`<el-pagination ... small />`（无冒号的布尔 attribute 也算）。
 * EP 2.14.3 的 pagination.mjs 在 setup 里调用 useDeprecated，条件正是
 * 「props.small 为真」，于是**每次渲染**都向 console 打一条 ElementPlusError：
 *   [el-pagination] [API] small is about to be deprecated in version 3.0.0,
 *   please use size instead.
 * 这条告警只在浏览器控制台可见：vitest 与 vue-tsc 都完全看不见它
 * （改回 small 时 154 文件全绿）。因此这里用 ?raw 源码断言 + 全仓扫描钉死。
 *
 * 语义等价映射（**读源码得到，不是猜的**）——element-plus/es/components/
 * pagination/src/pagination.mjs 的 setup：
 *   const _size = computed(() => props.small ? 'small' : props.size ?? _globalSize.value)
 * 而 small 与 size 同时给出时 small **优先**。
 *   small            ≡ size="small"
 *   :small="false"   ≡ 不传（props.small 的 buildProps 默认值是 false）
 * 另外 `small` 的弃用与 `size` 的取值域无关：useSizeProp 的 values 取自
 * constants/size.mjs 的 componentSizes = ['', 'default', 'small', 'large']，
 * 所以 size="small" 是合法的。
 *
 * ⚠️ 为什么写成**集合式**分类器而不是只钉 ChannelList.vue：
 * 同一条弃用还有别的元素（el-button / el-select / el-input 等）的同类用法。
 * 本门禁当前的分母锁定在 el-pagination（本轮唯一确认的缺陷面）；
 * 若将来发现其它组件也有该形态，应扩展 COMPONENT 集合而不是另写一份扫描器。
 */

/** 去掉 HTML 注释块。
 *
 * 必要性（与 LinkUnderlineApi 门禁同因）：源码里的**说明性注释**会原样包含
 * `small` 这样的字面量。若不对注释做剥离：
 *   (a) 说明「为什么不能这么写」的注释会被误判为缺陷（假红）；
 *   (b) 更危险的是反向假绿 —— 注释里恰好写着 size="small"，
 *       而真实属性已被改回 small，基于 toContain 的断言依然通过。
 * 门禁只对**实际模板标记**负责，因此所有断言一律在剥离注释后的源码上进行。
 */
export function stripHtmlComments(source: string): string {
  return source.replace(/<!--[\s\S]*?-->/g, '')
}

/** el-pagination 布尔 small 的判定式。
 *
 * 命中三种真实写法：
 *   small                 —— 无冒号的布尔 attribute（本轮 ChannelList.vue 的形态）
 *   :small="true"         —— 显式绑定 true
 *   :small="false"        —— 显式绑定 false（同样触发 useDeprecated 的 watch 吗？
 *                            不：condition 是 computed(() => !!props.small)，
 *                            绑定 false 时 props.small===false ⇒ 不打告警；
 *                            但它仍是**非此即彼**的旧 API 形态，且与
 *                            LinkUnderlineApi 的判据保持一致的严格度，故一并判红。）
 * 不命中：size="small"、:size="small"、small 出现在普通文本里。
 */
const BOOLEAN_SMALL_ATTRIBUTE = /(^|[\s"'<])small(?=[\s/>])/m
const BOUND_SMALL_ATTRIBUTE = /:small\s*=\s*"(true|false)"/

/** 仅在该标签是 el-pagination 时才算缺陷（避免误伤普通文本/其它组件）。 */
export function hasBooleanPaginationSmall(source: string): boolean {
  const markup = stripHtmlComments(source)
  const tags = markup.match(/<el-pagination\b[\s\S]*?\/?>/g) ?? []
  return tags.some((tag) => BOOLEAN_SMALL_ATTRIBUTE.test(tag) || BOUND_SMALL_ATTRIBUTE.test(tag))
}

/** 截取某个标签的开标签（到第一个 '>' 为止），用于「精确到该元素」的断言。 */
export function openingTagOf(source: string, tag: string): string {
  const markup = stripHtmlComments(source)
  const start = markup.indexOf('<' + tag)
  if (start < 0) throw new Error('找不到标签: ' + tag)
  const end = markup.indexOf('>', start)
  return markup.slice(start, end + 1)
}

/** 递归收集目录下（排除 node_modules / __tests__）的所有 .vue 源文件。 */
function collectVueSources(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === '__tests__') continue
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) collectVueSources(full, acc)
    else if (entry.endsWith('.vue')) acc.push(full)
  }
  return acc
}

describe('PaginationSizeApi：分类器自检（防止分类器写错导致永远绿灯/红灯）', () => {
  it('无冒号的 small attribute 必须被判为「有布尔用法」（本轮缺陷的真实形态）', () => {
    const sample = '<el-pagination layout="total" background small @current-change="x" />'
    expect(hasBooleanPaginationSmall(sample)).toBe(true)
  })

  it(':small="true" 必须被判为「有布尔用法」', () => {
    expect(hasBooleanPaginationSmall('<el-pagination :small="true" />')).toBe(true)
  })

  it(':small="false" 必须被判为「有布尔用法」（旧 API 形态，非此即彼）', () => {
    expect(hasBooleanPaginationSmall('<el-pagination :small="false" />')).toBe(true)
  })

  it('size="small" 必须判为「无布尔用法」（这是修复后的合法形态）', () => {
    const sample = '<el-pagination layout="total" background size="small" @current-change="x" />'
    expect(hasBooleanPaginationSmall(sample)).toBe(false)
  })

  it(':size="small"（v-model 变量名恰好叫 small）不误报 —— 分类器只认属性名', () => {
    // 分母说明：仓里确实存在把 size 绑到名为 size 的变量的写法；
    // 本用例锁的是「变量名小写 small 时不得误判」，防假阳性。
    expect(hasBooleanPaginationSmall('<el-pagination :size="small" />')).toBe(false)
  })

  it('普通文本/其它元素的 small 不误报', () => {
    expect(hasBooleanPaginationSmall('<el-button size="small">x</el-button>')).toBe(false)
    expect(hasBooleanPaginationSmall('<div class="small">small</div>')).toBe(false)
  })

  it('仅出现在 HTML 注释里的 small 不算缺陷（防说明性注释假红）', () => {
    const sample = '<!-- 不能写 small -->\n<el-pagination size="small" />'
    expect(hasBooleanPaginationSmall(sample)).toBe(false)
  })

  it('注释之外仍有真实布尔用法时必须判红（防「注释里写着合法写法」蒙混）', () => {
    const sample = '<!-- 应为 size="small" -->\n<el-pagination small />'
    expect(hasBooleanPaginationSmall(sample)).toBe(true)
  })
})

describe('ChannelList.vue：分页器尺寸形态', () => {
  const paginationTag = openingTagOf(channelListSource, 'el-pagination')

  it('真实标记中不含 el-pagination 布尔 small（否则每次渲染都打弃用告警）', () => {
    expect(hasBooleanPaginationSmall(channelListSource)).toBe(false)
  })

  it('该 el-pagination 的开标签上使用 EP 2.14 等价的 size="small"', () => {
    expect(paginationTag).toContain('size="small"')
    expect(paginationTag).not.toMatch(/[\s"'<]small(?=[\s/>])/)
  })

  it('剥离注释确实生效（原始源码里注释提到旧写法，剥离后只剩真实属性）', () => {
    // 分母：原始 ?raw 非空且确实含 el-pagination，说明这条自检不是空转。
    expect(channelListSource.length).toBeGreaterThan(1000)
    expect(channelListSource).toContain('<el-pagination')
    expect(stripHtmlComments(channelListSource)).toContain('size="small"')
  })
})

describe('全仓扫描：frontend-shared/src 下所有 .vue', () => {
  const root = resolve(process.cwd(), 'src')
  const files = collectVueSources(root)

  it('扫描器自身有效（找到的 .vue 数量是合理分母，不是空集合假绿）', () => {
    // 若递归/路径写错，files 为空 ⇒ 下面的断言会「全绿」，必须先钉住分母。
    // 阈值口径：本守卫防的是"扫描器坏了 ⇒ 集合为空或严重截断"，**不是**精确分子。
    // 原 `> 80` 会被正常的删除清理误伤（本轮删 3 个死组件后实测正好 80 ⇒ 假红）。
    // 取 70 留出整改余量：真出问题时集合会掉到个位数或 0，仍会红。
    expect(files.length).toBeGreaterThan(70)
    expect(files.some((f) => f.endsWith(join('channel', 'ChannelList.vue')))).toBe(true)
  })

  it('扫描器确实能扫到 el-pagination（分母非空，否则门禁形同虚设）', () => {
    const withPagination = files.filter((f) => stripHtmlComments(readFileSync(f, 'utf8')).includes('<el-pagination'))
    expect(withPagination.length).toBeGreaterThan(0)
  })

  it('无任何 el-pagination 布尔 small 用法', () => {
    const offenders: string[] = []
    for (const file of files) {
      if (hasBooleanPaginationSmall(readFileSync(file, 'utf8'))) {
        offenders.push(file.replace(root + '/', ''))
      }
    }
    expect(offenders, '存在 el-pagination 布尔 small 用法: ' + offenders.join(', ')).toEqual([])
  })
})
