import { describe, expect, it } from 'vitest'
import type { AutomationRule } from '@/api/automation'
import { nextRunAtText, nextRunText } from '../automationFormat'

/**
 * 「下次执行时间」展示门禁（2026-11-17）。
 *
 * 反向验证（改坏实现必须变红）：
 *   · 把 status 分类合并成"有 at 就显示时间" → waiting_data / no_event 用例红；
 *   · 忽略 timezone 直接用浏览器时区格式化 → 跨时区用例红；
 *   · 把"今天/明天"改成绝对日期 → 今天/明天用例红；
 *   · 忽略 require_confirmed → 需确认用例红。
 */

const TZ = 'Asia/Shanghai'

/** 造一条最小规则（只填被断言到的字段）。 */
function rule(over: Partial<AutomationRule> = {}): AutomationRule {
  return {
    id: 1,
    name: '关闭BMS充电',
    enabled: true,
    trigger_type: 'sun_event',
    trigger_sun_kind: 'sunset',
    action_type: 'notification',
    action_level: 'info',
    cooldown_sec: 30,
    max_daily_exec: 10,
    require_confirmed: false,
    created_at: '2026-11-01T00:00:00Z',
    updated_at: '2026-11-01T00:00:00Z',
    ...over,
  }
}

/** 2026-11-17 22:19:23 +08:00 的毫秒值 */
const NOW = Date.UTC(2026, 10, 17, 14, 19, 23)

describe('下次执行时间：状态分类', () => {
  it('后端未接线 next_run 时显示占位符，而不是"没有下次执行"', () => {
    const v = nextRunText(rule(), NOW)
    expect(v.text).toBe('—')
    expect(v.tone).toBe('muted')
    expect(v.hasTime).toBe(false)
  })

  it('scheduled 且日期是明天 → "明天 HH:mm"', () => {
    const v = nextRunText(rule({
      next_run: {
        status: 'scheduled',
        at: '2026-11-18T17:41:35+08:00',
        timezone: TZ,
        basis: 'sun',
        computed_at: '2026-11-17T22:19:23+08:00',
      },
    }), NOW)
    expect(v.text).toBe('明天 17:41')
    expect(v.tone).toBe('ok')
    expect(v.hasTime).toBe(true)
  })

  it('scheduled 且就在今天 → "今天 HH:mm"', () => {
    const v = nextRunText(rule({
      next_run: {
        status: 'scheduled',
        at: '2026-11-17T23:05:00+08:00',
        timezone: TZ,
        computed_at: '2026-11-17T22:19:23+08:00',
      },
    }), NOW)
    expect(v.text).toBe('今天 23:05')
  })

  it('scheduled 且是更远的日期 → "MM-DD HH:mm"', () => {
    const v = nextRunText(rule({
      next_run: {
        status: 'scheduled',
        at: '2026-11-25T06:42:13+08:00',
        timezone: TZ,
        computed_at: '2026-11-17T22:19:23+08:00',
      },
    }), NOW)
    expect(v.text).toBe('11-25 06:42')
  })

  it('时刻已过（页面停留跨过触发点）→ "即将触发"，不是明年的日期', () => {
    const v = nextRunText(rule({
      next_run: {
        status: 'scheduled',
        at: '2026-11-17T17:41:35+08:00',
        timezone: TZ,
        computed_at: '2026-11-17T22:19:23+08:00',
      },
    }), NOW)
    expect(v.text).toBe('即将触发')
    expect(v.tone).toBe('warn')
    expect(v.hasTime).toBe(false)
  })

  it('数据驱动规则：不给时间，只说明在等什么', () => {
    const v = nextRunText(rule({
      trigger_type: 'sensor_threshold',
      next_run: { status: 'waiting_data', timezone: TZ, computed_at: '2026-11-17T22:19:23+08:00' },
    }), NOW)
    expect(v.text).toBe('等待数据满足条件')
    expect(v.hasTime).toBe(false)
  })

  it('数据驱动 + 冷却中：给出"最早何时可再次触发"', () => {
    const v = nextRunText(rule({
      trigger_type: 'sensor_threshold',
      next_run: {
        status: 'waiting_data',
        at: '2026-11-17T23:00:00+08:00',
        timezone: TZ,
        basis: 'cooldown',
        computed_at: '2026-11-17T22:19:23+08:00',
      },
    }), NOW)
    expect(v.text).toBe('可再触发 今天 23:00')
    expect(v.hasTime).toBe(false)
  })

  it('其余状态各有独立措辞（不得合并成一个"-"）', () => {
    const mk = (status: 'disabled' | 'no_event' | 'unsupported' | 'unavailable', detail?: string) =>
      nextRunText(rule({ next_run: { status, detail, computed_at: '2026-11-17T22:19:23+08:00' } }), NOW)
    expect(mk('disabled').text).toBe('已停用')
    expect(mk('no_event').text).toBe('今日无日出/日落')
    expect(mk('unsupported').text).toBe('触发器暂未实现')
    expect(mk('unavailable').text).toContain('配置异常')
    expect(mk('unavailable', '位置未配置').text).toContain('位置未配置')
    // 配置异常是**需要用户行动**的，必须 warn 而不是 muted。
    expect(mk('unavailable').tone).toBe('warn')
    expect(mk('disabled').tone).toBe('muted')
  })

  it('确认制规则必须标注"需确认"（到点只是落 pending_confirm）', () => {
    const v = nextRunText(rule({
      require_confirmed: true,
      next_run: {
        status: 'scheduled',
        at: '2026-11-18T17:41:35+08:00',
        timezone: TZ,
        computed_at: '2026-11-17T22:19:23+08:00',
      },
    }), NOW)
    expect(v.text).toBe('明天 17:41（需确认）')
  })
})

describe('下次执行时间：按时区渲染', () => {
  it('同一绝对时刻在不同规则时区显示不同钟面值', () => {
    // 同一时刻：UTC 2026-11-17 20:00。NOW = UTC 2026-11-17 14:19。
    const at = '2026-11-17T20:00:00+00:00'
    // 东八区 = 11-18 04:00 ⇒ 相对东八区的"今天"是明天
    expect(nextRunAtText(at, 'Asia/Shanghai', NOW)).toBe('明天 04:00')
    // 纽约 EST（UTC-5）= 11-17 15:00 ⇒ 同日
    expect(nextRunAtText(at, 'America/New_York', NOW)).toBe('今天 15:00')
  })

  it('"今天/明天"按**展示时区**判定，不是浏览器时区', () => {
    // 该断言的意义：若实现偷偷用浏览器本地时区算 dayDiff，
    // 跨时区规则就会出现"今天"和"明天"双双判错。
    const at = '2026-11-18T02:00:00+00:00' // UTC 11-18 02:00
    expect(nextRunAtText(at, 'Asia/Shanghai', NOW)).toBe('明天 10:00') // 11-18 10:00
    expect(nextRunAtText(at, 'America/New_York', NOW)).toBe('今天 21:00') // 11-17 21:00
  })

  it('非法时区名退回本地时区而不是抛错炸掉整张表', () => {
    expect(() => nextRunAtText('2026-11-18T09:41:35+00:00', 'Not/AZone', NOW)).not.toThrow()
  })

  it('非法时刻返回占位符', () => {
    expect(nextRunAtText('not-a-date', TZ, NOW)).toBe('—')
  })
})
