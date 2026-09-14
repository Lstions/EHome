import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ElMessage } from 'element-plus'
import { feedback, extractErrorMessage } from '@/utils/feedback'

/**
 * I-1「ElMessage.error 收敛」的出口契约测试。
 *
 * 背景：本轮把 src 下 108 行 / 109 处裸 ElMessage.error 全部改走 utils/feedback。
 * 收敛后所有错误提示共用同一条出口，因此这条出口本身的行为就是全局契约：
 *   1) 优先展示服务端原因（response.data.message → response.data.msg → error.message）；
 *   2) 错误统一停留 5 秒（裸 EP 默认 3 秒），并带关闭按钮。
 * 这里不 mock element-plus：直接断言真实 ElMessage 收到的 options。
 */
vi.mock('element-plus', async importOriginal => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: Object.assign(vi.fn(), {
      success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(),
    }),
  }
})

const message = vi.mocked(ElMessage)

/**
 * 取最近一次 `ElMessage(...)` 的文案。
 *
 * 为什么要这个助手: `ElMessage` 的入参是**联合类型**（`string | options | VNode | (() => VNode)`），
 * 直接 `message.mock.calls[0][0].message` 会 TS2532/TS2339 —— 而 **vitest 不做类型检查**，
 * 所以"测试全绿"与"typecheck 干净"在这里又会脱节（本仓台账 §3.32 已记录过两次同类事故）。
 * 收窄放在这一处，用例里就都是标准类型。
 */
function lastMessageText(): string {
  const calls = message.mock.calls
  const arg = calls[calls.length - 1]?.[0]
  if (typeof arg === 'string') return arg
  if (arg && typeof arg === 'object' && 'message' in arg) return String((arg as { message: unknown }).message)
  return ''
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('extractErrorMessage — 服务端原因优先级', () => {
  it('response.data.message 优先于 error.message', () => {
    const err = Object.assign(new Error('Request failed with status code 500'), {
      response: { data: { message: '后端具体原因' } },
    })
    expect(extractErrorMessage(err)).toBe('后端具体原因')
  })

  it('无 data.message 时回落到 data.msg', () => {
    const err = Object.assign(new Error('axios 文案'), {
      response: { data: { msg: '后端 msg 字段' } },
    })
    expect(extractErrorMessage(err)).toBe('后端 msg 字段')
  })

  it('无服务端响应时回落到 error.message', () => {
    expect(extractErrorMessage(new Error('本地原因'))).toBe('本地原因')
  })

  it('字符串错误直通', () => {
    expect(extractErrorMessage('直接抛字符串')).toBe('直接抛字符串')
  })

  it('null/undefined 回落到 fallback', () => {
    expect(extractErrorMessage(null, '兜底')).toBe('兜底')
    expect(extractErrorMessage(undefined, '兜底')).toBe('兜底')
  })

  it('非 Error 对象回落到 fallback（不把 [object Object] 抛给用户）', () => {
    expect(extractErrorMessage({ foo: 1 }, '兜底')).toBe('兜底')
  })
})

describe('feedback.error / feedback.handleError — 统一出口契约', () => {
  it('error() 用 error 级别 + 5 秒停留 + 关闭按钮', () => {
    feedback.error('刷新失败')

    expect(message).toHaveBeenCalledWith(expect.objectContaining({
      message: '刷新失败',
      type: 'error',
      duration: 5000,
      showClose: true,
    }))
  })

  it('handleError() 提取服务端 message 并以 error 级别弹出', () => {
    const err = Object.assign(new Error('boom'), {
      response: { data: { message: '引脚 12 已被 UART0 占用' } },
    })
    feedback.handleError(err)

    expect(message).toHaveBeenCalledWith(expect.objectContaining({
      message: '引脚 12 已被 UART0 占用',
      type: 'error',
      duration: 5000,
    }))
  })

  it('handleError() 的第二参数是「兜底」而非「前缀」：有 message 时不拼接前缀', () => {
    // 这是本次收敛的一处语义变化，显式固化为契约，避免后人误以为是回归。
    // 取舍：可感知损失是丢失了操作上下文；换来的是服务端原因不被本地前缀稀释。
    feedback.handleError(new Error('timeout'), '保存失败')

    expect(message).toHaveBeenCalledWith(expect.objectContaining({
      message: 'timeout',
    }))
  })

  it('handleError() 在无可用 message 时才使用兜底文案', () => {
    feedback.handleError(new Error(''), '保存失败')

    expect(message).toHaveBeenCalledWith(expect.objectContaining({
      message: '保存失败',
    }))
  })

  // ── handleErrorWithContext: 保留操作上下文（本轮 I-1 复核新增） ──────────────
  //
  // 背景: 本仓后端**总是**带 message（api/envelope.go:66 的 Error() 必写 Message），
  // 而 handleError 的第二参数是「兜底」⇒ 调用方写的操作上下文几乎永远看不到。
  // 对"同一句错误会出现在多个操作里"的场景（ChannelPanel 的添加/更新 GPIO）必须保留上下文。
  it('handleErrorWithContext() 把操作上下文与服务端原因**同时**呈现', () => {
    feedback.handleErrorWithContext(new Error('timeout'), '添加 GPIO 失败')

    expect(message).toHaveBeenCalledWith(expect.objectContaining({
      message: '添加 GPIO 失败: timeout',
    }))
  })

  it('handleErrorWithContext() 无 message 时用兜底补全细节（上下文仍在前）', () => {
    feedback.handleErrorWithContext({ foo: 1 }, '删除 PWM 失败')

    expect(message).toHaveBeenCalledWith(expect.objectContaining({
      message: '删除 PWM 失败: 操作失败',
    }))
  })

  it('handleErrorWithContext() 与 handleError() 的差异是**可断言**的（防止两者被合并成一个）', () => {
    // 同一个输入, 两个出口必须给出不同文案 —— 否则"显式拼接"这个能力等于不存在。
    message.mockClear()
    feedback.handleError(new Error('timeout'), '添加 GPIO 失败')
    const viaFallback = lastMessageText()
    message.mockClear()
    feedback.handleErrorWithContext(new Error('timeout'), '添加 GPIO 失败')
    const viaContext = lastMessageText()

    expect(viaFallback).toBe('timeout')                        // 兜底: 上下文被丢弃
    expect(viaContext).toBe('添加 GPIO 失败: timeout')          // 拼接: 上下文保留
    expect(viaContext).not.toBe(viaFallback)
  })

  it('错误停留时长严格大于 Element Plus 默认的 3 秒', () => {
    feedback.error('x')
    const opts = message.mock.calls[0][0] as { duration?: number }
    expect(opts.duration).toBeGreaterThan(3000)
  })
})
