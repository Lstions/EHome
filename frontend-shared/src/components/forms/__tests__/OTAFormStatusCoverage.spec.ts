import { describe, it, expect } from 'vitest'
import otaFormSource from '../OTAForm.vue?raw'

/**
 * OTA 任务状态的覆盖完整性（P5a，2026-09-17 实测）。
 *
 * 缺陷：statusMap 只覆盖 7 项，漏掉后端 ota.go:24-31 定义的
 * verifying / timeout / needs_retry；渲染是 `TABLE[status] || status`
 * ⇒ 这三态在中文界面**原样显示英文**。
 *
 * ⚠️ 本文件存在的理由：改前跑既有 108 条 OTA/Node 测试**全部通过**，
 * 即使把缺陷改回去也全绿 —— 既有测试根本没覆盖状态映射。
 * 所以这里用 ?raw 源码断言把"后端全集 ↔ 前端映射"的对应关系钉死。
 */

// 后端权威全集（backend/internal/ota/ota.go:24-31）
const BACKEND_OTA_STATUSES = [
  'pending', 'downloading', 'verifying', 'installing',
  'success', 'failed', 'timeout', 'needs_retry',
] as const

/**
 * 从源码里截取指定常量的字面量块。
 *
 * 支持两种形态：对象字面量 `{...}` 与数组字面量 `[...]`（new Set 用方括号）。
 * 必须配平截取而不是"从 { 到文件尾" —— 后者会把后续规则一并吞进来，
 * 造成"删掉本规则也能靠后面的规则蒙混过关"的假绿（本仓已实际发生过）。
 */
function ruleBlockOf(source: string, constName: string): string {
  // 锚点用"定义行"（const NAME = / const NAME:）而不是裸名字：
  // 裸名字会命中注释里对该常量的描述，截到错误的块。
  const anchor = constName.startsWith('const ') ? constName : 'const ' + constName
  const start = source.indexOf(anchor)
  if (start < 0) throw new Error('找不到常量定义: ' + anchor)
  if (start < 0) throw new Error('找不到常量: ' + constName)
  // 取最早出现的 { 或 [ 作为块起点（new Set([...]) 用方括号）
  const candidates = [source.indexOf('{', start), source.indexOf('[', start)].filter(i => i >= 0)
  if (candidates.length === 0) throw new Error('找不到字面量块: ' + constName)
  const open = Math.min(...candidates)
  const opener = source[open]
  const closer = opener === '{' ? '}' : ']'
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === opener) depth++
    else if (source[i] === closer) {
      depth--
      if (depth === 0) return source.slice(open, i + 1)
    }
  }
  throw new Error('常量块未闭合: ' + constName)
}

describe('OTAForm OTA 状态覆盖（P5a）', () => {
  const block = ruleBlockOf(otaFormSource, 'OTA_STATUS_TEXT')

  it('分类器自检：截取到的确实是 OTA_STATUS_TEXT 块而非整个文件', () => {
    expect(block.startsWith('{')).toBe(true)
    expect(block).toContain('downloading')
    // 若截取逻辑退化成"到文件尾"，块里会混进后续无关代码
    expect(block).not.toContain('const OTA_TERMINAL_SUCCESS')
  })

  it('覆盖后端全部 8 个状态：缺一个就会在中文界面露出英文', () => {
    for (const status of BACKEND_OTA_STATUSES) {
      expect(block, '缺少状态映射: ' + status).toContain(status + ':')
    }
  })

  it('verifying / timeout / needs_retry 都有中文文案（不是英文原文）', () => {
    for (const status of ['verifying', 'timeout', 'needs_retry']) {
      const m = block.match(new RegExp(status + ":\\s*'([^']+)'"))
      expect(m, status + ' 无文案').toBeTruthy()
      const text = m![1]
      // 必须含中文，且不得等于状态名本身
      expect(/[\u4e00-\u9fa5]/.test(text), status + ' 文案应为中文: ' + text).toBe(true)
      expect(text).not.toBe(status)
    }
  })

  it('timeout / needs_retry 属不成功终态（否则轮询永不停止，UI 永远转圈）', () => {
    const term = ruleBlockOf(otaFormSource, 'OTA_TERMINAL_FAILURE')
    expect(term).toContain('timeout')
    expect(term).toContain('needs_retry')
    expect(term).toContain('failed')
  })

  it('轮询收尾用的是终态集合，而不是只判 failed', () => {
    expect(otaFormSource).toContain('OTA_TERMINAL_FAILURE.has(record.status)')
    expect(otaFormSource).toContain('OTA_TERMINAL_SUCCESS.has(record.status)')
    // 防回归：不得退回"只判 failed 一条分支"的旧写法
    // （精确到 else-if 形态，避免命中说明性注释里的历史描述）
    expect(otaFormSource).not.toContain("else if (record.status === 'failed')")
  })
})

describe('OTAForm 更新日志换行（P8）', () => {
  it('更新日志区保留换行（pre-wrap），否则多行日志挤成一行', () => {
    expect(otaFormSource).toMatch(/\.firmware-changelog\s*\{[^}]*white-space:\s*pre-wrap/)
  })

  it('scoped 规则块配平截取自检（防"M5 假绿"重演）', () => {
    // 截取 .firmware-changelog 规则块，确认它自身含 pre-wrap，
    // 而不是靠后续规则里的 pre-wrap 蒙混过关
    expect(otaFormSource.indexOf('.firmware-changelog')).toBeGreaterThan(-1)
    // 直接断言该规则块内部（用花括号配平），而不是"文件里任何地方有 pre-wrap"
    const src = otaFormSource
    const idx = src.indexOf('.firmware-changelog')
    const open = src.indexOf('{', idx)
    let depth = 0, end = open
    for (let i = open; i < src.length; i++) {
      if (src[i] === '{') depth++
      else if (src[i] === '}') { depth--; if (depth === 0) { end = i; break } }
    }
    const own = src.slice(open, end + 1)
    expect(own).toContain('white-space: pre-wrap')
    expect(own).toContain('max-height')
  })
})
