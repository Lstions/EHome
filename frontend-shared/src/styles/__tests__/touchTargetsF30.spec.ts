import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * F30：小号 select 在窄屏/粗指针下必须回到「输入控件 44px」档（规范 §4.4.5）。
 *
 * ## 根因（本文件守护的东西）
 * 窄屏块与粗指针块里把输入控件抬到 44px 的规则是 `.el-select__wrapper`（特异性 0,1,0），
 * 它压不住两条**更窄**的规则：
 *   ① EP 自带 `.el-select--small .el-select__wrapper{min-height:24px}`（0,3,0）
 *   ② 本文件第 4 条 `.el-select--small .el-select__wrapper{min-height:36px}`（0,3,0，桌面档取值）
 * 实测（修复前，真实 Chromium）：小号 select 在 fine-390 / coarse-390 均被钉在 36px，
 * 而同一页的**普通** select 正确为 44px —— 症状是「只有小号变体不达标」。
 *
 * ## 为什么这个文件存在（而不是并进 F6/F27 的 spec）
 * F6 与 F27 的 spec 都按「窄屏块数量 <=3」「块内锚点」定位 theme.css（它们已加守卫）。
 * F30 的修法必须把规则放在**第 4 条全局规则之后**，而那个位置已在既有块结构之外；
 * 为了不动别人的抽取锚点，这里只做**独立**断言，不改它们的 spec。
 *
 * 注意：本层只守护「源码里写了什么」，**不能**代替真实浏览器取证
 * （本仓有「源码有规则、产物里没有」的教训）。真实三档实测见
 * .tmp-probe/lead-select-wrapper.mjs。
 */
const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

/** 位置是否处于注释内（F6 记录过的坑：注释里的同名文本会骗过 indexOf）。 */
function inComment(css: string, pos: number): boolean {
  const open = css.lastIndexOf('/*', pos)
  if (open === -1) return false
  return css.lastIndexOf('*/', pos) < open
}

/** 找 needle 第一次不在注释里出现的位置。 */
function indexOutsideComment(css: string, needle: string): number {
  for (let from = 0; ; ) {
    const at = css.indexOf(needle, from)
    if (at === -1) return -1
    if (!inComment(css, at)) return at
    from = at + needle.length
  }
}

/** 取出 selector 之后第一个 { ... } 块（按大括号配对）。 */
function blockAfter(css: string, at: number): string {
  const open = css.indexOf('{', at)
  if (open === -1) return ''
  let depth = 0
  for (let i = open; i < css.length; i += 1) {
    if (css[i] === '{') depth += 1
    else if (css[i] === '}') {
      depth -= 1
      if (depth === 0) return css.slice(open + 1, i)
    }
  }
  return ''
}

const SMALL_RULE = '.el-select--small .el-select__wrapper'

/** 全局（无媒体查询）的小号 select 规则位置 —— 它是窄屏档要压过的对象。 */
const globalAt = indexOutsideComment(themeCss, SMALL_RULE)

describe('F30 小号 select 的窄屏/粗指针 44px 档', () => {
  it('分母自证：读到了非空 theme.css，且全局小号 select 规则确实存在', () => {
    expect(themeCss.length).toBeGreaterThan(10000)
    expect(globalAt, '找不到全局 ' + SMALL_RULE + ' 规则，断言会假绿').toBeGreaterThan(-1)
  })

  it('全局（桌面档）小号 select 仍是 36px —— 桌面密度不得被本修复改动', () => {
    expect(blockAfter(themeCss, globalAt)).toMatch(/min-height:\s*36px/)
  })

  it('窄屏媒体查询里存在小号 select 的 44px 覆盖，且位于全局规则之后（靠后出现取胜）', () => {
    // 在全局规则之后寻找 @media (max-width: 768px) { ... SMALL_RULE ... 44px }
    const after = themeCss.slice(globalAt)
    const mediaAt = after.indexOf('@media (max-width: 768px)')
    expect(mediaAt, '全局规则之后没有窄屏媒体查询 —— 同特异性更早出现会被压掉（实测仍为 36px）').toBeGreaterThan(-1)
    const mediaBody = blockAfter(after, mediaAt)
    expect(mediaBody, '窄屏块里没有 ' + SMALL_RULE).toContain(SMALL_RULE)
    expect(mediaBody).toMatch(/min-height:\s*44px/)
  })

  it('粗指针媒体查询里也存在小号 select 的 44px 覆盖（宽视口触屏设备同样需要）', () => {
    const after = themeCss.slice(globalAt)
    // 取**最后一个**粗指针块（F30 的覆盖写在文件末尾；文件前部另有一个 F6 的粗指针块）
    const coarseAt = after.lastIndexOf('@media (pointer: coarse)')
    expect(coarseAt, '全局规则之后没有粗指针媒体查询').toBeGreaterThan(-1)
    const coarseBody = blockAfter(after, coarseAt)
    expect(coarseBody, '粗指针块里没有 ' + SMALL_RULE).toContain(SMALL_RULE)
    expect(coarseBody).toMatch(/min-height:\s*44px/)
  })

  it('反例守卫：窄屏块数量仍 <=3（F6/F27 的抽取锚点按块数定位，新增会顶开它们）', () => {
    const narrowBlocks = themeCss.split('@media (max-width: 768px)').length - 1
    expect(narrowBlocks, '出现了额外的 max-width:768px 块：' + narrowBlocks).toBeLessThanOrEqual(3)
  })

  it('反例守卫：本修复不得引入 !important（那会掩盖而非解决层叠问题）', () => {
    const after = themeCss.slice(globalAt)
    const selAt = after.indexOf(SMALL_RULE, after.indexOf('@media (max-width: 768px)'))
    expect(selAt, '窄屏块里没有 ' + SMALL_RULE).toBeGreaterThan(-1)
    expect(blockAfter(after, selAt), '小号 select 的 44px 规则不得用 !important').not.toContain('!important')
  })
})
