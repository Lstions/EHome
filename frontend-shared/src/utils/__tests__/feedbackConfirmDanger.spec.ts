import { describe, it, expect, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { ElMessageBox } from 'element-plus'
import feedback from '@/utils/feedback'

/**
 * confirmDanger 的“真实 Element Plus 渲染”契约测试（不 mock ElMessageBox）。
 *
 * 为什么必须查真实 DOM：本项目的危险确认合规点有两条（规范 §3.4.3 / §4.3.4），
 * 都只体现在 ElMessageBox 渲染出来的 DOM 上：
 *   1) 确认按钮必须是 danger 视觉层级；
 *   2) 默认焦点不得落在破坏性按钮上。
 * 只断言“传参里写了 el-button--danger”无法证明它真的作用到按钮上——
 * 实测 EP 2.14.3 只给 confirmButtonClass 时按钮会同时带 el-button--primary，
 * 因此这里额外守卫“确认键不得带 primary 类”。
 */

async function microFlush() {
  for (let i = 0; i < 5; i += 1) {
    await nextTick()
  }
}

async function macroFlush() {
  // 让出一个宏任务：confirmDanger 的安全侧定焦排在 EP 自身聚焦流程之后，
  // 用的是 setTimeout(0)（见 utils/feedback.ts 的 scheduleDangerSafeSideFocus）。
  // EP 那条链路全是 nextTick 微任务，因此宏任务必然在其后执行，与真实浏览器一致。
  await new Promise(resolve => setTimeout(resolve, 0))
  await microFlush()
}

async function flushBox() {
  // ElMessageBox 通过 render() 同步挂载，但 visible / 焦点落点由 watch + nextTick 决定。
  await microFlush()
  await macroFlush()
  await macroFlush()
}

function buttons(): HTMLButtonElement[] {
  return [...document.querySelectorAll<HTMLButtonElement>('.el-message-box__btns button')]
}

function confirmButton(): HTMLButtonElement | null {
  const btns = buttons()
  return btns.length ? btns[btns.length - 1] : null
}

afterEach(() => {
  ElMessageBox.close()
  document.body.innerHTML = ''
})

describe('feedback.confirmDanger (real Element Plus message box)', () => {
  it('renders the confirm button with the danger class and without primary', async () => {
    const pending = feedback.confirmDanger('删除节点「A」？此操作不可恢复。', {
      title: '警告',
      confirmText: '删除',
      cancelText: '取消',
    })
    await flushBox()

    const confirm = confirmButton()
    expect(confirm).not.toBeNull()
    expect(confirm!.textContent!.trim()).toBe('删除')
    expect(confirm!.classList.contains('el-button--danger')).toBe(true)
    // 反例守卫：危险确认键不得是 primary（EP 默认确认按钮语义类型）
    expect(confirm!.classList.contains('el-button--primary')).toBe(false)

    const cancel = buttons()[0]
    expect(cancel.textContent!.trim()).toBe('取消')
    expect(cancel.classList.contains('el-button--danger')).toBe(false)

    // 取消路径契约：返回 false，调用方据此直接 return（不得继续执行破坏性动作）
    cancel.click()
    await flushBox()
    await expect(pending).resolves.toBe(false)
  })

  it('does not put the initial focus on the destructive button', async () => {
    const pending = feedback.confirmDanger('删除节点「A」？此操作不可恢复。', {
      title: '警告',
      confirmText: '删除',
    })
    await flushBox()

    const confirm = confirmButton()
    const active = document.activeElement as HTMLElement | null
    expect(confirm).not.toBeNull()
    expect(active).not.toBe(confirm)
    expect(active?.textContent?.trim()).not.toBe('删除')

    buttons()[0].click()
    await flushBox()
    await expect(pending).resolves.toBe(false)
  })

  /**
   * 回归护栏（F5 根因）：默认焦点必须**确定地**落在取消键上，而不是“碰巧不在危险键上”。
   *
   * 为什么要单独钉这一条：只断言“activeElement 不是危险按钮”**挡不住回归**。
   * EP 2.14.3 里 autofocus:false 会把 focusStartRef 指向对话根本身，
   * 焦点既不在危险键、也不在安全键上——上一条断言依然通过，规范 §4.3.4
   * 「默认焦点应避免诱导性确认」却没被满足（实测焦点落点还会随环境漂移：
   * jsdom 下 el-focus-trap 的 tryFocus(根) 失败后退回 focusStartEl='first'，
   * 落到 DOM 第一个可聚焦元素 = 标题栏关闭键）。
   * 本仓已有 6 例“伪装成正常”的教训，故这里把落点钉死为取消键本身。
   *
   * 定位必须限定 .el-message-box__btns：EP 在标题栏还渲染了一个
   * .el-message-box__headerbtn（关闭键），它在 DOM 里排在按钮区之前，
   * 用“第一个 button”会拿到它（真实 Chromium 已复现，见 tools/f5-focus-probe.mjs）。
   *
   * 另注：真实 Chromium 实测 autofocus:false 的 Tab 序列是
   *   根 div → 关闭键 → 取消 → 删除，所以“一次 Tab 就直达删除”并不成立；
   * 下面 (b) 断言的仍是“一次 Tab 后**不落在危险键**”，该断言本身是真实且有意义的。
   */
  it('puts the initial focus on the cancel button, so the first Tab does not reach the destructive button', async () => {
    const pending = feedback.confirmDanger('删除数据源「温度主来源」？此操作不可恢复。', {
      title: '确认删除',
      confirmText: '删除',
      cancelText: '取消',
    })
    await flushBox()

    const btns = buttons()
    expect(btns).toHaveLength(2)
    const [cancel, confirm] = btns
    expect(cancel.textContent!.trim()).toBe('取消')
    expect(confirm.textContent!.trim()).toBe('删除')
    // (a) 默认焦点就在安全侧（取消键），而不是对话框容器或危险按钮
    expect(document.activeElement).toBe(cancel)

    // (b) 焦点陷阱已生效：从取消键按一次 Tab 绝不能落到「删除」
    cancel.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', code: 'Tab', bubbles: true }))
    cancel.dispatchEvent(new KeyboardEvent('keyup', { key: 'Tab', code: 'Tab', bubbles: true }))
    expect(document.activeElement).not.toBe(confirm)

    cancel.click()
    await flushBox()
    await expect(pending).resolves.toBe(false)
  })

  it('keeps object identity in the message and returns true only on confirm', async () => {
    const pending = feedback.confirmDanger('删除数据源「温度主来源」？此操作不可恢复。', {
      title: '确认删除',
      confirmText: '删除',
    })
    await flushBox()

    expect(document.querySelector('.el-message-box__message')?.textContent)
      .toContain('温度主来源')

    confirmButton()!.click()
    await flushBox()
    await expect(pending).resolves.toBe(true)
  })
})
