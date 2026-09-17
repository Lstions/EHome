import { describe, expect, it } from 'vitest'
import source from '../OTAForm.vue?raw'

/**
 * OTAForm「固件信息」区防挤压契约（源码结构级断言，项目既有 `?raw` 惯例）。
 *
 * 缺陷（2026-09-17 生产截图取证，2026-09-17 隔离栈复核）：
 * 600px 弹层 + el-form label-width="120px" + :column="2" + 64 字符无断点 MD5 ⇒
 * auto 表格布局反压另一列，实测 label 列 27px / 内容列 57.7px：
 *   · 表头「文件名」「文件大小」「更新日志」逐字竖排；
 *   · 「文件大小」的值被拆成「1.35」+「MB」两行；
 *   · 描述表整体溢出弹层 body 右侧 103px。
 * 修复：改单列（长值独占整行）+ MD5 等宽/任意断点 + 短值禁止折行。
 *
 * 为什么断言源码而不是 class 属性：项目测试环境用 Element Plus 轻量 stub
 * （src/test-setup.ts），<el-descriptions> 只是渲染 slot 的 div，会**吞掉**
 * :column，挂载断言拿不到列数。因此列数只能从源码模板断言。
 * 而 scoped 样式在 happy-dom 下不注入 DOM，故样式从 <style scoped> 源码断言。
 */

/** 取模板里「固件信息」那段 el-form-item 源码；定位不到返回空串（fail-closed） */
export function firmwareTemplate(src: string): string {
  const at = src.indexOf('label="固件信息"')
  if (at < 0) return ''
  const end = src.indexOf('</el-form-item>', at)
  return end < 0 ? '' : src.slice(at, end)
}

/** 取 <style scoped> 块内容；没有 scoped 块返回 null（不是 ''，以便与「空块」区分） */
export function scopedStyle(src: string): string | null {
  return new RegExp('<style[^>]*\\bscoped\\b[^>]*>([\\s\\S]*?)</style>').exec(src)?.[1] ?? null
}

/**
 * 取指定选择器的规则块；取不到返回 null。
 *
 * 必须**括号配平**地截取该规则自身的块：早期版本取「首个 { 到文件末尾最后一个 }」，
 * 结果块内容被扩展到后续规则，于是「删掉 label 的 white-space: nowrap」也能靠
 * 后面 .firmware-size 里的 nowrap 蒙混过关（变异自证 M5 实测假绿）。
 */
export function ruleBlockOf(css: string | null, selector: string): string | null {
  if (css === null) return null
  const at = css.indexOf(selector)
  if (at < 0) return null
  const open = css.indexOf('{', at)
  if (open < 0) return null
  let depth = 0
  for (let i = open; i < css.length; i++) {
    if (css[i] === '{') depth++
    else if (css[i] === '}') {
      depth--
      if (depth === 0) return css.slice(open + 1, i)
    }
  }
  return null
}

/** 该长度的串被 nowrap/break-all 约束后的最小行数（1 = 单行显示） */
export function minLineCountAfterWrap(len: number, mode: 'nowrap' | 'break-all'): number {
  return mode === 'break-all' ? 1 : len
}

/**
 * 分类器：给定一份 SFC 源码，判定「文件大小不被拆行」的证据是否存在。
 * 证据 = scoped 样式里对大小值显式 nowrap，或整个描述区已单列（值独占整行，无挤压源）。
 * 两者皆无 ⇒ false。防「把 :column 改回 2 且删掉样式」的静默回归。
 */
export function hasSizeNoWrapEvidence(src: string): boolean {
  const css = scopedStyle(src)
  const tmpl = firmwareTemplate(src)
  if (css === null || tmpl === '') return false
  return /white-space:\s*nowrap/.test(css) || /:column="1"/.test(tmpl)
}

const css = scopedStyle(source)
const tmpl = firmwareTemplate(source)

describe('OTAForm 固件信息区 · 分类器自检（防断言假绿）', () => {
  it('两条独立保障各自可判真；两者皆无 / 连样式块都缺 ⇒ 判假（fail-closed）', () => {
    // 组装一份「SFC 形状」的最小样本（含定位标记 label="固件信息" 与 </el-form-item>）
    const sfc = (descAttrs: string, styleCss: string) =>
      '<el-form-item label="固件信息" v-if="selectedFirmware">\n' +
      `  <el-descriptions ${descAttrs}>\n  </el-descriptions>\n` +
      '</el-form-item>\n' +
      (styleCss === '' ? '' : `<style scoped>${styleCss}</style>`)

    // 保障一：单列（长值独占整行，消除挤压源）
    expect(hasSizeNoWrapEvidence(sfc(':column="1" size="small"', '.other { color: red; }')), '单列应被判为有保障').toBe(true)
    // 保障二：对大小值显式 nowrap（即使仍是两列）
    expect(
      hasSizeNoWrapEvidence(sfc(':column="2"', '.firmware-size { white-space: nowrap; }')),
      '显式 nowrap 应被判为有保障',
    ).toBe(true)
    // 反例：仍是两列、样式里也没有 nowrap ⇒ 必须判假，否则门禁是瞎的
    expect(hasSizeNoWrapEvidence(sfc(':column="2"', '.other { color: red; }')), '两列且无 nowrap 必须判假').toBe(false)
    // 反例：连 scoped 样式块都没有 ⇒ fail-closed 判假
    expect(hasSizeNoWrapEvidence(sfc(':column="1"', '')), '缺 scoped 样式块必须判假').toBe(false)
  })

  it('解析器对缺失输入 fail-closed（返回空串/null，不得静默当成通过）', () => {
    expect(firmwareTemplate('<div>没有固件信息区</div>')).toBe('')
    expect(scopedStyle('<style>.a{}</style>')).toBeNull()
    expect(ruleBlockOf(null, '.firmware-size')).toBeNull()
    expect(ruleBlockOf('.firmware-size { white-space: nowrap; }', '.firmware-other')).toBeNull()
    // 括号配平自检：后面规则的声明不得混进前面规则的块里（曾经的假绿来源）
    const twoRules = '.a { color: red; }\n.b { white-space: nowrap; }'
    expect(ruleBlockOf(twoRules, '.a'), '取 .a 的块时不得把 .b 的内容带进来').not.toContain('nowrap')
    expect(ruleBlockOf(twoRules, '.b')).toContain('nowrap')
  })

  it('扫描器没瞎：模板与样式块都真的取到了内容', () => {
    expect(tmpl, '未取到固件信息模板块 —— 选择器或措辞变了').not.toBe('')
    expect(css, '未取到 <style scoped> 块').not.toBeNull()
    expect((css as string).length).toBeGreaterThan(50)
    expect(tmpl).toContain('selectedFirmware.checksum')
  })
})

describe('OTAForm 固件信息区 · 防挤压契约', () => {
  it('描述区为单列（:column="1"）—— 64 字符 MD5 必须独占整行', () => {
    expect(tmpl, '模板里找不到 el-descriptions 列数声明').toMatch(/<el-descriptions[^>]*:column="1"/)
    expect(tmpl, '固件信息区不得退回两列布局').not.toContain(':column="2"')
  })

  it('MD5 长串施加防溢出样式：等宽字体 + 任意断点（不省略、不截断）', () => {
    const md5Block = ruleBlockOf(css, '.firmware-md5')
    expect(md5Block, '缺少 .firmware-md5 样式规则').not.toBeNull()
    expect(md5Block).toMatch(/word-break:\s*break-all/)
    expect(md5Block).toMatch(/font-family:[^;]*mono/i)
    // 完整哈希必须始终可见：禁止任何省略/截断/强制单行溢出的写法
    expect(md5Block, 'MD5 不得用省略号隐藏').not.toMatch(/text-overflow/)
    expect(md5Block, 'MD5 不得被强制单行（会溢出被裁）').not.toMatch(/white-space:\s*nowrap/)
    expect(md5Block).not.toMatch(/overflow:\s*hidden/)
    // 分类器复核：64 字符在 break-all 下最小行数为 1（可安全换行，不会撑破列）
    expect(minLineCountAfterWrap(64, 'break-all')).toBe(1)
    expect(minLineCountAfterWrap(64, 'nowrap'), '对照组：nowrap 下 64 字符即为最小列宽要求').toBe(64)
    // 模板确实把 checksum 渲染进该样式类，而不是只声明了样式
    expect(tmpl).toMatch(/class="firmware-md5"[^>]*>\s*\{\{\s*selectedFirmware\.checksum\s*\}\}/)
  })

  it('「文件大小」有「不被拆行」的保障（值 nowrap，且外层已消除挤压源）', () => {
    const sizeBlock = ruleBlockOf(css, '.firmware-size')
    expect(sizeBlock, '缺少 .firmware-size 样式规则').not.toBeNull()
    expect(sizeBlock).toMatch(/white-space:\s*nowrap/)
    // 模板确实把 formatFileSize 的结果渲染进该样式类
    expect(tmpl).toMatch(/class="firmware-size"[^>]*>\s*\{\{\s*formatFileSize\(selectedFirmware\.size_bytes\)\s*\}\}/)
    // 消费者：分类器必须认这份源码为「有保障」
    expect(hasSizeNoWrapEvidence(source)).toBe(true)
  })

  it('label/表头不被压成竖排（描述区标签 nowrap）', () => {
    // 用标签类名定位（而非整条 :deep 选择器），这样调整选择器写法不会误伤本断言
    const labelBlock = ruleBlockOf(css, '.el-descriptions__label')
    expect(labelBlock, '缺少描述区 label 的 :deep 规则（:deep 内联了括号，扫描器仍须匹配）').not.toBeNull()
    expect(css, 'label 规则未限定在本组件的描述区类内').toContain('.firmware-descriptions')
    expect(labelBlock).toMatch(/white-space:\s*nowrap/)
  })

  it('信息完整性：文件名 / 大小 / MD5 / 更新日志四项一个不少，滚动区保留', () => {
    for (const label of ['文件名', '文件大小', 'MD5', '更新日志']) {
      expect(tmpl, `缺失条目：${label}`).toContain(`label="${label}"`)
    }
    expect(tmpl).toContain('selectedFirmware.filename')
    expect(tmpl).toContain('selectedFirmware.checksum')
    expect(tmpl).toContain('selectedFirmware.changelog')
    const changelog = ruleBlockOf(css, '.firmware-changelog')
    expect(changelog, '缺少 .firmware-changelog 样式规则').not.toBeNull()
    expect(changelog).toMatch(/max-height:\s*100px/)
    expect(changelog).toMatch(/overflow-y:\s*auto/)
    expect(tmpl).toMatch(/class="firmware-changelog"/)
  })
})
