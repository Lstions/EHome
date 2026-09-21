import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import {
  AVAILABILITY_REASON_CODES,
  AVAILABILITY_REASONS,
  classifyUnavailable,
  selfHealableUnavailable,
} from '../actionAvailability'
import type { EffectiveAction } from '@/api/deviceOperation'

/**
 * 「不可用」不得静默：**每个**后端 reason 都必须有用户可行动指引。
 *
 * 为什么需要这条门禁（2026-09-21，主控提醒的正是这一类错误）：
 * 上一轮只修了 capability_stale 一条分支，就在自己设定的验证面内宣布完成。
 * 但同一页面的不可用原因是一个**集合**，任何一条没有指引，用户就再一次
 * 面对「操作不可用但不知为何」。所以这里把后端的 reason_code / reason 文案
 * 全集**从源码里扫出来**，逐个断言前端分类器给出了 non-empty 的处置建议。
 *
 * 反面用法（门禁自身也会坏）：所有断言都依赖扫描器扫到的集合，
 * 若路径写错扫到空集，测试会「全绿」。因此每条都有分母下界断言。
 */

const BACKEND = resolve(process.cwd(), '..', 'backend')

function goFiles(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      if (entry === 'node_modules') continue
      goFiles(full, acc)
    } else if (entry.endsWith('.go') && !entry.endsWith('_test.go')) {
      acc.push(full)
    }
  }
  return acc
}

/** 从后端源码扫出全部 AvailabilityCode: "..." 取值。 */
export function scanAvailabilityCodes(files: string[]): string[] {
  const found = new Set<string>()
  for (const file of files) {
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(/AvailabilityCode:\s*"([a-z_]+)"/g)) found.add(match[1])
  }
  return [...found].sort()
}

function effective(reasonCode: string, reason: string): EffectiveAction {
  return {
    definition: {
      id: 'probe', version: 1, name: 'probe', description: '', device_type: 'sn3001_rain',
      semantics: 'read', risk: 'low', transport: 'channel_cmd_v2',
    },
    available: false,
    reason,
    reason_code: reasonCode,
  }
}

describe('ActionAvailability 分类器自检（防止分类器坏掉后永远绿灯）', () => {
  it('capability_stale 必须被判为可自愈，并给出可点的动作', () => {
    const got = classifyUnavailable(effective('capability_stale', 'ChannelCmdV2 capability is unavailable or stale'))
    expect(got.key).toBe('capability_stale')
    expect(got.selfHealable).toBe(true)
    expect(got.action.length).toBeGreaterThan(8)
    expect(got.action).toContain('立即恢复')
  })

  it('无需操作的冻结类必须 selfHealable=false，且不得诱导用户白点', () => {
    for (const code of ['protocol_unverified', 'hardware_evidence_required', 'command_engine_gate']) {
      const got = classifyUnavailable(effective(code, 'whatever'))
      expect(got.key).toBe(code)
      expect(got.selfHealable).toBe(false)
      expect(got.action).toContain('无需操作')
    }
  })

  it('空 reason_code 时按后端文案分类（节点离线 / 通道不可用 / 开关关闭）', () => {
    expect(classifyUnavailable(effective('', 'edge device or node is unavailable')).key).toBe('node_unavailable')
    expect(classifyUnavailable(effective('', 'action channel is unavailable')).key).toBe('channel_unavailable')
    expect(classifyUnavailable(effective('', 'device control v2 is disabled')).key).toBe('dispatch_disabled')
    expect(classifyUnavailable(effective('', 'peripheral config is unavailable or disabled')).key).toBe('periph_config_unavailable')
  })

  it('完全未知的原因必须落到 unknown 兜底，且**原文回显**（不得静默吞掉）', () => {
    const got = classifyUnavailable(effective('brand_new_code', '3 天后会有新原因'))
    expect(got.key).toBe('unknown')
    expect(got.summary).toContain('3 天后会有新原因')
    expect(got.selfHealable).toBe(false)
    expect(got.action.length).toBeGreaterThan(8)
  })

  it('selfHealableUnavailable 只放行可自愈项（防止把冻结类也塞进自愈按钮）', () => {
    const items = [
      effective('capability_stale', 'x'),
      effective('protocol_unverified', 'y'),
      effective('hardware_evidence_required', 'z'),
    ]
    expect(selfHealableUnavailable(items).map((i) => i.reason_code)).toEqual(['capability_stale'])
  })
})

describe('覆盖率门禁：后端每一个 reason 都必须有用户可行动指引', () => {
  const files = goFiles(BACKEND)

  it('扫描器自身有效（Go 文件数与命中码数都是合理分母，不是空集合假绿）', () => {
    expect(files.length, '扫到的 Go 文件数 —— 路径写错会让下面的断言全部空转').toBeGreaterThan(50)
    const codes = scanAvailabilityCodes(files)
    expect(codes, '扫到的 AvailabilityCode —— 正则失效会让门禁形同虚设').toContain('hardware_evidence_required')
    expect(codes).toContain('protocol_unverified')
  })

  it('AVAILABILITY_REASON_CODES 覆盖后端扫出的全部 AvailabilityCode', () => {
    const codes = scanAvailabilityCodes(files)
    const missing = codes.filter((code) => !(AVAILABILITY_REASON_CODES as readonly string[]).includes(code))
    expect(missing, '后端新增了前端没登记的能力码: ' + missing.join(', ')).toEqual([])
  })

  it('每个已登记的 reason_code 都能给出 non-empty 的处置建议', () => {
    for (const code of AVAILABILITY_REASON_CODES) {
      if (code === '') continue
      const got = classifyUnavailable(effective(code, ''))
      expect(got.key, `${code} 落到了兜底分支（说明分类器缺这一条）`).toBe(code)
      expect(got.summary.length, `${code} 缺少「为什么」`).toBeGreaterThan(8)
      expect(got.action.length, `${code} 缺少「怎么办」`).toBeGreaterThan(8)
    }
  })

  it('每个 Catalog 门禁文案都能被分类（空 reason_code 时唯一可依据的就是它）', () => {
    for (const reason of AVAILABILITY_REASONS) {
      const got = classifyUnavailable(effective('', reason))
      expect(got.key, `文案 ${reason} 落到了兜底分支`).not.toBe('unknown')
      expect(got.action.length).toBeGreaterThan(8)
    }
  })

  it('门禁文案与后端源码逐字一致（后端改文案而前端没跟上时会变红）', () => {
    const serviceSource = readFileSync(join(BACKEND, 'internal', 'commandexec', 'service.go'), 'utf8')
    for (const reason of AVAILABILITY_REASONS) {
      expect(serviceSource, `后端 service.go 里已找不到文案 ${reason}`).toContain(reason)
    }
    // 分母自证：确实从源码里读到了 gate 文案函数。
    expect(serviceSource).toContain('func reasonForGate')
  })
})
