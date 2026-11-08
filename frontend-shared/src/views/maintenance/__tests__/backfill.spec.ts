import { describe, expect, it } from 'vitest'
import {
  BACKFILL_POLL_MS,
  BACKFILL_STATE_TEXT,
  clampBackfillPercent,
  formatEta,
  formatFrames,
  isBackfillSuppressed,
  isBackfillTerminal,
  shouldShowBackfill,
} from '../backfill'
import type { BackfillProgress } from '@/types/system'

/**
 * 回填展示口径（契约 §4.6）—— 纯函数层。
 *
 * 这里守的是**三条会误导用户的表达**，都不是样式问题：
 *   ① frames_total = 0 是「总数未知」而不是「共 0 帧」——写成 "1,234 / 0" 像分母错了；
 *   ② eta_seconds = 0 是「还没算出来」而不是「马上完成」——写成 "剩余 0 秒" 像卡住了；
 *   ③ failed **不是**终态 —— 容器重启后会从断点续跑，停止轮询会让卡片永远停在「已中断」。
 */

function makeProgress(patch: Partial<BackfillProgress> = {}): BackfillProgress {
  return {
    state: 'running',
    percent: 42.6,
    current_partition: 'unified_data_202610',
    frames_done: 1_234_567,
    frames_total: 3_419_991,
    eta_seconds: 1200,
    retention_permanent: false,
    errors: [],
    ...patch,
  }
}

describe('回填状态文案', () => {
  it('五种状态都有中文文案（含 disabled —— 运维应急阀的产物，契约初稿漏了它）', () => {
    expect(Object.keys(BACKFILL_STATE_TEXT).sort()).toEqual(['disabled', 'done', 'failed', 'idle', 'running'])
    for (const text of Object.values(BACKFILL_STATE_TEXT)) {
      expect(text.length).toBeGreaterThan(0)
    }
  })

  it('done/idle/disabled 是终态，轮询要停', () => {
    expect(isBackfillTerminal('done')).toBe(true)
    expect(isBackfillTerminal('idle')).toBe(true)
    expect(isBackfillTerminal('disabled')).toBe(true)
  })

  it('running/failed 不是终态 —— failed 继续轮询才能在后端重启后自动恢复', () => {
    expect(isBackfillTerminal('running')).toBe(false)
    expect(
      isBackfillTerminal('failed'),
      'failed 被当成终态 ⇒ 后端重启续跑后卡片永远停在「已中断」，用户只能手刷',
    ).toBe(false)
  })

  it('未知状态（后端新增了状态而前端没跟上）按「非终态」处理，不静默停轮询', () => {
    expect(isBackfillTerminal('something_new')).toBe(false)
    expect(isBackfillTerminal(null)).toBe(false)
    expect(isBackfillTerminal(undefined)).toBe(false)
  })
})

describe('是否显示回填卡', () => {
  it('running / failed / disabled 显示，done / idle 隐藏', () => {
    expect(shouldShowBackfill(makeProgress({ state: 'running' }))).toBe(true)
    expect(shouldShowBackfill(makeProgress({ state: 'failed' }))).toBe(true)
    // disabled 必须显示：运维关掉回填 ⇒ 老数据永远不会迁移，用户有权知道。
    // 静默隐藏会让「数据没迁」这件事永不可见（生产会因运维关阀真实出现此态）。
    expect(shouldShowBackfill(makeProgress({ state: 'disabled' })), 'disabled 被隐藏了 ⇒ 用户永远看不到「回填已关闭」').toBe(true)
    expect(shouldShowBackfill(makeProgress({ state: 'done' }))).toBe(false)
    expect(shouldShowBackfill(makeProgress({ state: 'idle' }))).toBe(false)
  });

  it('isBackfillSuppressed 只对 disabled 为真（「不跑但没出错」的判据集中一处）', () => {
    expect(isBackfillSuppressed('disabled')).toBe(true)
    for (const state of ['idle', 'running', 'done', 'failed', null, undefined] as const) {
      expect(isBackfillSuppressed(state), state + ' 不该被判成 suppressed').toBe(false)
    }
  })

  it('done 必须隐藏：回填是一次性任务，完成后不该常驻首屏', () => {
    expect(shouldShowBackfill(makeProgress({ state: 'done', percent: 100 }))).toBe(false)
  })

  it('null（接口不可用/未登录）时不显示，但也不算错误 —— 页面其余部分照常', () => {
    expect(shouldShowBackfill(null)).toBe(false)
    expect(shouldShowBackfill(undefined)).toBe(false)
  })
})

describe('帧数文案：frames_total = 0 表示未知，不是 0 帧', () => {
  it('总数已知时显示 a / b', () => {
    expect(formatFrames(1_234_567, 3_419_991)).toBe('1,234,567 / 3,419,991 帧')
  })

  it('总数为 0（未知）时**不得**出现 "/ 0" 这种像分母错了的写法', () => {
    const text = formatFrames(1_234_567, 0)
    expect(text, '总数未知时写出了 0 分母').not.toContain('/ 0')
    expect(text).toContain('1,234,567')
    expect(text).toContain('总数统计中')
  })

  it('总数缺失（omitempty）与 0 同义', () => {
    expect(formatFrames(100, undefined)).toBe(formatFrames(100, 0))
    expect(formatFrames(100, null)).toBe(formatFrames(100, 0))
  })

  it('已完成数为 0 时显示 0，不显示占位符（0 帧是真的 0，不是未知）', () => {
    expect(formatFrames(0, 0)).toContain('0')
    expect(formatFrames(0, 0)).not.toContain('—')
  })

  it('非法输入不抛异常且不出现 NaN', () => {
    expect(formatFrames(Number.NaN, Number.NaN)).not.toContain('NaN')
    expect(formatFrames(-1, -1)).not.toContain('NaN')
  })
})

describe('ETA 文案：0 = 尚未算出，不是「马上完成」', () => {
  it('0 / 缺失 / 负数一律显示未知占位符（—），绝不显示「剩余 0 秒」', () => {
    for (const value of [0, undefined, null, -5, Number.NaN]) {
      expect(formatEta(value as number), '把「未知」渲染成了具体秒数').toBe('—')
    }
  })

  it('秒 / 分钟 / 小时三档边界', () => {
    expect(formatEta(45)).toBe('约 45 秒')
    expect(formatEta(59)).toBe('约 59 秒')
    // 60s 进位到分钟档
    expect(formatEta(60)).toBe('约 1 分钟')
    expect(formatEta(23 * 60)).toBe('约 23 分钟')
    expect(formatEta(59 * 60)).toBe('约 59 分钟')
    // 60 分钟进位到小时档
    expect(formatEta(3600)).toBe('约 1 小时')
    expect(formatEta(3600 + 5 * 60)).toBe('约 1 小时 5 分钟')
    expect(formatEta(2 * 3600)).toBe('约 2 小时')
  })

  it('整小时不显示「0 分钟」（"约 2 小时 0 分钟"是机器味）', () => {
    expect(formatEta(7200)).not.toContain('0 分钟')
  })

  it('二十分钟量级（真实场景）读得出来', () => {
    expect(formatEta(20 * 60)).toBe('约 20 分钟')
    expect(formatEta(40 * 60)).toBe('约 40 分钟')
  })
})

describe('百分比夹取', () => {
  it('正常值四舍五入到整数', () => {
    expect(clampBackfillPercent(42.4)).toBe(42)
    expect(clampBackfillPercent(42.6)).toBe(43)
  })

  it('越界与非法值都夹/归到 0..100（显示层不能信上游）', () => {
    expect(clampBackfillPercent(-1)).toBe(0)
    expect(clampBackfillPercent(101)).toBe(100)
    expect(clampBackfillPercent(Number.NaN)).toBe(0)
    expect(clampBackfillPercent(Number.POSITIVE_INFINITY)).toBe(0)
    expect(clampBackfillPercent(undefined)).toBe(0)
  })
})

describe('轮询间隔', () => {
  it('5s —— 与需求一致，且导出常量避免魔法数字两处漂移', () => {
    expect(BACKFILL_POLL_MS).toBe(5000)
  })
})
