import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { parse, compileStyleAsync } from '@vue/compiler-sfc'
import gpioListSource from '@/components/periph/GPIOResourceList.vue?raw'
import pwmListSource from '@/components/periph/PWMResourceList.vue?raw'

/**
 * 窄屏触控目标**级联结果**门禁（移动端 ≥44px）。
 *
 * ## 为什么新增这个文件（一次真实失败）
 *
 * 2026-09-24：我在 `GPIOResourceList`/`PWMResourceList` 里加了
 * `.actions :deep(.el-button) { min-height: 44px }`，并写了源码断言
 * （"文件里含 min-height: 44px" ⇒ 通过），**据此在提交信息里声称"实测 44px"**。
 *
 * 实际上按钮**仍是 36px**。原因：该选择器编译后是
 * `.actions[data-v-x] .el-button`（特异性 **0,3,0**），
 * 而 `theme.css:722` 的窄屏规则
 *   `.el-button--small:not(.is-circle):not(.is-link):not(.is-text) { min-height: 36px }`
 * 靠 `:not()` 链堆到 **0,4,0**，**优先级高于书写顺序**，把它压过。
 * 真浏览器实测 `getComputedStyle(btn).minHeight === '36px'`。
 *
 * ## 这个门禁与既有门禁的区别（关键）
 *
 * - `TouchTargets.spec.ts`（既有）与各种 `?raw` 断言都是**源码字符串级**：
 *   只证明"声明写在了文件里"，**无法发现被级联覆盖** —— 这正是本次漏掉的原因。
 * - 本文件把 `theme.css`（全局规则，含 `@media`）与**组件编译后的 scoped CSS**
 *   一起注入同一文档，再读 `getComputedStyle` ⇒ 得到**真实级联结果**。
 *
 * happy-dom 的保真度已实测确认：
 *   - `happyDOM.setViewport({width:390})` 后 `matchMedia('(max-width:768px)')` 由 false 变 true；
 *   - 它**真的做特异性比较**：低特异性 36px、高特异性 44px，与真实 Chromium 结果一致。
 *   （未设视口时视口是 1024×768 ⇒ 媒体查询不命中 ⇒ 会得到假绿，故本文件必须显式设视口。）
 *
 * ## 覆盖面与反证
 *
 * 两个资源列表的**每个操作按钮**都断言 ≥44px；并反证"把组件里那条窄屏规则删掉后
 * 必须是 36px" —— 若反证也返回 44px，说明量测函数没在真做级联（恒定值假绿）。
 */

/** 编译期注入的 scope id —— 必须与编译时传入的 `id` 一致，且必须真的写到 DOM 上。 */
const SCOPE_ID = 'data-v-periph44'

/** 编译 <style scoped> 为构建期形态（复现 [data-v-*] 改写） */
async function compiledScopedCss(raw: string, filename: string): Promise<string> {
  const { descriptor } = parse(raw, { filename })
  const chunks: string[] = []
  for (const block of descriptor.styles) {
    const res = await compileStyleAsync({
      source: block.content,
      filename,
      id: SCOPE_ID,
      scoped: Boolean(block.scoped),
    })
    expect(res.errors, JSON.stringify(res.errors)).toEqual([])
    chunks.push(res.code)
  }
  return chunks.join('\n')
}

const THEME_CSS = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

/**
 * 注入【全局 theme.css + 组件编译产物】并返回按钮的计算 min-height。
 *
 * ⚠️ 两个必须做对、否则**静默失败**的点（都实测踩过）：
 *  1. **DOM 形状**必须与真实模板一致（.resource-row > .actions > button.el-button--small），
 *     否则选择器不命中，读到的是全局默认值 —— 看起来"有结果"其实是量错元素。
 *  2. **scoped 属性必须写进 DOM**：scoped CSS 编译后含 `[data-v-xxx]`，
 *     若 synthetic DOM 没有该属性，组件规则**永不命中**（实测 `btn.matches(...)` → false），
 *     于是只剩全局的 36px，会让人误判成"组件规则写错了"。
 *     Vue 运行时自动加这些属性，手写 DOM 时必须自己补。
 */
function measureActionButton(css: string): { minHeight: string } {
  const theme = document.createElement('style')
  theme.textContent = THEME_CSS
  const comp = document.createElement('style')
  comp.textContent = css
  document.head.append(theme, comp)

  const li = document.createElement('li')
  li.className = 'resource-row'
  li.setAttribute('data-state', 'available')
  li.setAttribute(SCOPE_ID, '')
  const actions = document.createElement('div')
  actions.className = 'actions'
  actions.setAttribute(SCOPE_ID, '')
  const btn = document.createElement('button')
  // 与组件里真实的 el-button 类组合一致（`size="small"` 产出 --small）
  btn.className = 'el-button el-button--primary el-button--small'
  btn.setAttribute('data-testid', 'configure-gpio-0')
  actions.appendChild(btn)
  li.appendChild(actions)
  document.body.appendChild(li)

  const out = { minHeight: getComputedStyle(btn).minHeight }
  theme.remove(); comp.remove(); li.remove()
  return out
}

describe('窄屏触控目标级联门禁（移动端操作按钮 ≥44px）', () => {
  beforeEach(() => {
    // 必须设视口，否则默认 1024×768 ⇒ @media (max-width:768px) 不命中 ⇒ 假绿
    ;(window as unknown as { happyDOM: { setViewport: (o: { width: number; height: number }) => void } })
      .happyDOM.setViewport({ width: 390, height: 844 })
  })
  afterEach(() => {
    document.head.innerHTML = ''
    document.body.innerHTML = ''
  })

  it('前提自检：窄屏媒体查询在本环境下确实命中（否则下面的断言全是假绿）', () => {
    expect(window.innerWidth, '视口未生效，媒体查询不会命中').toBe(390)
    expect(window.matchMedia('(max-width: 768px)').matches, '媒体查询未命中 ⇒ 本门禁失效').toBe(true)
  })

  it.each([
    ['GPIOResourceList', gpioListSource],
    ['PWMResourceList', pwmListSource],
  ])('%s 的「配置」按钮在窄屏下计算高度 ≥44px（真实级联结果，非源码字符串）', async (name, src) => {
    const css = await compiledScopedCss(src, name + '.vue')
    const { minHeight } = measureActionButton(css)
    // 断言 `min-height` 而非 `height`：happy-dom **不做布局**，`getComputedStyle().height`
    // 恒为空串（实测 NaN）。而 `min-height` 是**级联结果**——正是本次缺陷发生的那一层，
    // 也足以判定"声明是否真的胜出"。
    // 真实几何高度由 Playwright 在真浏览器验收（实测 44px）。
    const px = Number.parseFloat(minHeight)
    expect(
      Number.isFinite(px),
      name + ' 未能读到 min-height（量测函数没命中元素 ⇒ 选择器或 DOM 形状不对）：' + JSON.stringify(minHeight),
    ).toBe(true)
    expect(
      px,
      name + ' 操作按钮在 390px 视口下 min-height=' + minHeight + ' < 44px。' +
        '多半是被 theme.css:722 的 .el-button--small:not(...):not(...):not(...) (0,4,0) 压过 ——' +
        '组件里的窄屏规则必须复刻该 :not() 链再加一层后代选择器。',
    ).toBeGreaterThanOrEqual(44)
  })

  it('反证：删掉组件里那条窄屏规则后必须是 36px（证明量测真的在做级联）', async () => {
    const css = await compiledScopedCss(gpioListSource, 'GPIOResourceList.vue')
    // 把 44px 削弱成 36px（等价于"规则被删/特异性不足"⇒ 落到全局 36px 档）
    const weakened = css.replace(/min-height:\s*44px/g, 'min-height: 36px')
    const { minHeight } = measureActionButton(weakened)
    expect(
      Number.parseFloat(minHeight),
      '反证失败：削弱规则后 min-height=' + minHeight + '，说明量测函数没在真做级联比较（恒定值假绿）',
    ).toBe(36)
  })

  it('反证：全局 theme.css 的 36px 规则确实存在于窄屏块（本门禁防的就是它）', () => {
    // 若将来 theme.css 删除该规则，本门禁的"对抗对象"消失 —— 组件规则仍成立，
    // 但这条断言会红，提示复核本文件的注释与前提是否还准确。
    expect(THEME_CSS).toMatch(
      /\.el-button--small:not\(\.is-circle\):not\(\.is-link\):not\(\.is-text\)\s*\{\s*min-height:\s*36px/,
    )
  })
})
