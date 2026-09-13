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

async function flushBox() {
  // ElMessageBox 通过 render() 同步挂载，但 visible / 焦点落点由 watch + nextTick 决定。
  for (let i = 0; i < 5; i += 1) {
    await nextTick()
  }
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
