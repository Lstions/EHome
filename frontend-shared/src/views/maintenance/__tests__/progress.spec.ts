import { describe, expect, it } from 'vitest'
import {
  BASE_POLL_MS,
  currentStageText,
  stepDisplayLabel,
  MAX_POLL_MS,
  PROBE_GRACE_MS,
  RETRY_POLL_MS,
  WATCHDOG_POLL_MS,
  clampPercent,
  countByState,
  formatElapsed,
  httpStatusOf,
  isAuthDenial,
  maintenancePollDelay,
  stepStateText,
} from '../progress'
import type { MaintenanceStep } from '@/types/system'

/**
 * 维护页纯函数助手（进度/耗时/退避）。
 *
 * 这些函数承载三条**产品判据**，所以各自单独钉住，而不是只在组件渲染里间接覆盖：
 *   ① 就绪判据只看 ready（这里对应「百分比可能停在中间」的 clamp 行为）；
 *   ② 轮询必须**永不停止**、但有退避上限；
 *   ③ 已耗时只增不减且不出现脏值。
 */

describe('stepStateText：五态文案', () => {
  it('五种合法状态各有一句中文文案', () => {
    expect(stepStateText('pending')).toBe('等待中')
    expect(stepStateText('running')).toBe('进行中')
    expect(stepStateText('done')).toBe('已完成')
    expect(stepStateText('failed')).toBe('失败')
    expect(stepStateText('skipped')).toBe('已跳过')
  })

  it('未知状态回落为「等待中」而不是抛错（后端新增状态不应白屏）', () => {
    expect(stepStateText('brand_new_state')).toBe('等待中')
    expect(stepStateText('')).toBe('等待中')
  })
})

describe('stepDisplayLabel：阶段名的空值兜底（空串会渲染成一行什么都没有）', () => {
  it('有 label 就用 label', () => {
    expect(stepDisplayLabel({ id: 'parent_indexes', label: '补齐父表索引' })).toBe('补齐父表索引')
  })

  it('label 为空串 / 纯空白 → 回落到 id（id 比破折号更可报障）', () => {
    expect(stepDisplayLabel({ id: 'parent_indexes', label: '' })).toBe('parent_indexes')
    expect(stepDisplayLabel({ id: 'parent_indexes', label: '   ' })).toBe('parent_indexes')
    expect(stepDisplayLabel({ id: 'parent_indexes' })).toBe('parent_indexes')
  })

  it('label 与 id 都缺失 → 占位符 —（绝不留空）', () => {
    expect(stepDisplayLabel({})).toBe('—')
    expect(stepDisplayLabel({ id: '', label: '' })).toBe('—')
  })
})

describe('currentStageText：当前阶段的空值兜底（failed 时 current_step 可能为空）', () => {
  it('有阶段名时显示阶段名', () => {
    expect(currentStageText('补齐父表索引')).toBe('当前阶段：补齐父表索引')
  })

  it('空串 / undefined / 纯空白 → 「已停止」，不留悬空的「当前阶段：」', () => {
    expect(currentStageText('')).toBe('当前阶段：已停止')
    expect(currentStageText(undefined)).toBe('当前阶段：已停止')
    expect(currentStageText('  ')).toBe('当前阶段：已停止')
  })
})

describe('formatElapsed：已耗时格式化', () => {
  it('小于一分钟只给秒', () => {
    expect(formatElapsed(0)).toBe('0 秒')
    expect(formatElapsed(999)).toBe('0 秒')
    expect(formatElapsed(1000)).toBe('1 秒')
    expect(formatElapsed(59_400)).toBe('59 秒')
  })

  it('一分钟以上补零到两位秒（避免宽度抖动）', () => {
    expect(formatElapsed(60_000)).toBe('1 分 00 秒')
    expect(formatElapsed(65_000)).toBe('1 分 05 秒')
    expect(formatElapsed(3_725_000)).toBe('62 分 05 秒')
  })

  it('负数 / NaN / Infinity 一律回落为 0（服务端时钟回拨不显示 -1 秒）', () => {
    expect(formatElapsed(-5000)).toBe('0 秒')
    expect(formatElapsed(Number.NaN)).toBe('0 秒')
    expect(formatElapsed(Number.POSITIVE_INFINITY)).toBe('0 秒')
  })
})

describe('maintenancePollDelay：退避有上限、且永不返回 0/负', () => {
  it('连得上时 2s 快问', () => {
    expect(BASE_POLL_MS).toBe(2000)
    expect(maintenancePollDelay(0)).toBe(BASE_POLL_MS)
  })

  it('连续失败按 2s → 3s → 5s 封顶，且不再增长（防打爆后端）', () => {
    expect(maintenancePollDelay(1)).toBe(RETRY_POLL_MS)
    expect(maintenancePollDelay(2)).toBe(MAX_POLL_MS)
    expect(maintenancePollDelay(10)).toBe(MAX_POLL_MS)
    expect(maintenancePollDelay(1000)).toBe(MAX_POLL_MS)
  })

  it('任何入参都返回正数（否则 setTimeout(0) 会变成忙循环）', () => {
    for (const n of [-5, -1, 0, 1, 2, 99]) {
      const delay = maintenancePollDelay(n)
      expect(delay, '第 ' + n + ' 次失败返回了非正数间隔').toBeGreaterThan(0)
    }
  })
})

describe('isAuthDenial：401/403 判据（不是"凭次数放行"，而是凭状态码语义）', () => {
  // 推理链（与 progress.ts 注释同一份）：
  //   维护期后端对业务请求统一回 503；/api/v1/system/maintenance 是免鉴权白名单的一员，
  //   维护引擎不可能对它回 401/403。能收到 401/403，说明请求已穿过维护引擎，
  //   打到了完整引擎的鉴权中间件 ⇒ 全量路由已注册完成 ⇒ 后端**已完全就绪**。
  it('401 / 403 判定为「后端已就绪」', () => {
    expect(isAuthDenial(401)).toBe(true)
    expect(isAuthDenial(403)).toBe(true)
  })

  it('503（维护期的正确信号）绝不放行', () => {
    expect(isAuthDenial(503)).toBe(false)
  })

  it('其它状态码与 undefined 一律不放行（含 500/502/504 与网络错误）', () => {
    for (const s of [400, 404, 429, 500, 502, 504, undefined]) {
      expect(isAuthDenial(s), '状态码 ' + String(s) + ' 不应触发放行').toBe(false)
    }
  })
})

describe('httpStatusOf：从错误里取 HTTP 状态码', () => {
  it('取得到 number 时原样返回', () => {
    expect(httpStatusOf({ response: { status: 401 } })).toBe(401)
  })

  it('取不到一律 undefined（不把异常当 401 处理）', () => {
    expect(httpStatusOf(new Error('network'))).toBeUndefined()
    expect(httpStatusOf(null)).toBeUndefined()
    expect(httpStatusOf({ response: { status: 'x' } })).toBeUndefined()
  })
})

describe('clampPercent：0..100 取整', () => {
  it('正常值四舍五入', () => {
    expect(clampPercent(0)).toBe(0)
    expect(clampPercent(33.4)).toBe(33)
    expect(clampPercent(66.6)).toBe(67)
    expect(clampPercent(100)).toBe(100)
  })

  it('越界与脏值收敛到边界（失败时 percent 可能停在中间，不能崩）', () => {
    expect(clampPercent(-1)).toBe(0)
    expect(clampPercent(100.4)).toBe(100)
    expect(clampPercent(Number.NaN)).toBe(0)
    expect(clampPercent(Number.POSITIVE_INFINITY)).toBe(0)
  })
})

describe('countByState：失败摘要的分母', () => {
  const steps: MaintenanceStep[] = [
    { id: 'a', label: 'A', weight: 1, state: 'done', progress: 100 },
    { id: 'b', label: 'B', weight: 1, state: 'failed', progress: 40, error: 'boom' },
    { id: 'c', label: 'C', weight: 1, state: 'failed', progress: 10, error: 'boom2' },
    { id: 'd', label: 'D', weight: 1, state: 'pending', progress: 0 },
  ]

  it('按状态计数', () => {
    expect(countByState(steps, 'failed')).toBe(2)
    expect(countByState(steps, 'done')).toBe(1)
    expect(countByState(steps, 'skipped')).toBe(0)
  })
})

describe('PROBE_GRACE_MS：首次探测宽限期', () => {
  it('宽限期必须 > 0 且 < 基础轮询间隔（否则正常启动会闪维护页）', () => {
    expect(PROBE_GRACE_MS).toBeGreaterThan(0)
    expect(PROBE_GRACE_MS).toBeLessThan(BASE_POLL_MS)
  })

  it('宽限期即"无内容可见"的上限，必须足够短（<=1s），长时间空白比闪帧更糟', () => {
    expect(PROBE_GRACE_MS).toBeLessThanOrEqual(1000)
  })
})

describe('WATCHDOG_POLL_MS：fail-open 后仍要能重回维护页', () => {
  it('看门狗用最慢档（不与正常轮询抢频率），但必须是有限值（不能是 0/Infinity）', () => {
    expect(WATCHDOG_POLL_MS).toBe(MAX_POLL_MS)
    expect(Number.isFinite(WATCHDOG_POLL_MS)).toBe(true)
    expect(WATCHDOG_POLL_MS).toBeGreaterThan(0)
  })
})
