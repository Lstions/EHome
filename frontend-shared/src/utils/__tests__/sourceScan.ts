import { readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

/**
 * 全站源码扫描的公共能力（供各「源码扫描门禁」复用，避免每个门禁各写一份状态机）。
 *
 * 归属说明：放在 __tests__ 下但**不带 .spec**，所以 vitest 不会把它当测试用例；
 * 同时门禁的源码遍历会跳过 __tests__ 目录，所以它自身的字符串不会干扰扫描结果。
 */

/** 反引号字符；用 charCode 构造，避免本文件自身出现模板字符串字面量 */
export const BT = String.fromCharCode(96)

/** 递归收集业务源码；__tests__ 是断言「不存在违规」的反向用例，故跳过 */
export function walkSourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === '__tests__' || name === 'node_modules') continue
      out.push(...walkSourceFiles(p))
    } else if (name.endsWith('.vue') || name.endsWith('.ts')) {
      out.push(p)
    }
  }
  return out
}

/**
 * 把注释区域替换成等长空格，**保留换行**（所以行号仍然准确）。
 * 必须遮蔽注释，否则「解释为什么禁止某个写法」的文档注释本身就会误报。
 * 字符串状态机保证出现在字符串里的注释起始符不会被当成注释起点。
 * 已知边界（fail-closed 方向）：不支持模板字符串内部再嵌引号的极端写法，
 * 那种情况下可能把一个字符串提前判结束 —— 只会导致**多报**，不会漏报。
 */
export function stripComments(source: string): string {
  type State = 'code' | 'line' | 'block' | 'html' | 'single' | 'double' | 'template'
  let state: State = 'code'
  let out = ''
  let i = 0
  while (i < source.length) {
    const ch = source[i]
    const next = source[i + 1]
    if (state === 'code') {
      if (ch === '/' && next === '/') { state = 'line'; out += '  '; i += 2; continue }
      if (ch === '/' && next === '*') { state = 'block'; out += '  '; i += 2; continue }
      if (source.startsWith('<!--', i)) { state = 'html'; out += '    '; i += 4; continue }
      if (ch === "'") { state = 'single'; out += ch; i += 1; continue }
      if (ch === '"') { state = 'double'; out += ch; i += 1; continue }
      if (ch === BT) { state = 'template'; out += ch; i += 1; continue }
      out += ch; i += 1; continue
    }
    if (state === 'line') {
      if (ch === '\n') { state = 'code'; out += ch } else { out += ' ' }
      i += 1; continue
    }
    if (state === 'block') {
      if (ch === '*' && next === '/') { state = 'code'; out += '  '; i += 2; continue }
      out += ch === '\n' ? '\n' : ' '; i += 1; continue
    }
    if (state === 'html') {
      if (source.startsWith('-->', i)) { state = 'code'; out += '   '; i += 3; continue }
      out += ch === '\n' ? '\n' : ' '; i += 1; continue
    }
    // 字符串/模板串内部：原样保留（模板串里的违规词同样要被发现）
    if (ch === '\\') { out += ch + (next === undefined ? '' : next); i += 2; continue }
    const quote = state === 'single' ? "'" : state === 'double' ? '"' : BT
    if (ch === quote) state = 'code'
    out += ch; i += 1; continue
  }
  return out
}
