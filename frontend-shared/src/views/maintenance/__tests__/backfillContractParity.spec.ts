import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { BACKFILL_STATE_TEXT, isBackfillTerminal, shouldShowBackfill } from '../backfill'

/**
 * 回填进度 JSON 契约门禁（回归锁）—— **前后端双向**。
 *
 * 为什么必须有它（这不是形式主义）：
 * 后端 `internal/api.BackfillSnapshot` 是 `internal/backfill.Progress` 的**镜像**
 * （api 包刻意不 import backfill，理由见 backend/internal/api/backfill_status.go 约束 1），
 * 而前端 `types/system.ts` 的 `BackfillProgress` 又是那份镜像的第三份。**三段链条
 * 没有任何编译器保护**：后端把 `eta_seconds` 改成 `etaSeconds`、或打上 omitempty、
 * 或前端抄错一个字母，都不会被 go build / vue-tsc 发现 ——
 * 表现只是「进度卡永远显示 —」「ETA 永远未知」，而所有单测全绿
 * （因为前端单测喂的是自己造的快照）。
 *
 * 判据（fail-closed，四条）：
 *   ① 后端载荷恰好 8 个 key（契约 §4.6），前端 interface 逐字声明同一组；
 *   ② 双向断言：Go 有 TS 没有 = 前端看不到该信息；TS 有 Go 没有 = 前端在读 undefined；
 *   ③ 分母守卫：扫到的 key 数必须 == 8（解析器坏了要吵，而不是静默通过）；
 *   ④ 五个 State 字面量逐字一致（`disabled` 漏掉 ⇒ 运维关阀时前端空白）。
 *
 * 变红条件：后端改字段名/加 omitempty/加字段，或前端抄错/漏抄。
 */

const ROOT = process.cwd()
const GO_FILE = join(ROOT, '..', 'backend', 'internal', 'api', 'backfill_status.go')
const TS_FILE = join(ROOT, 'src', 'types', 'system.ts')

/** 取 Go struct 定义体（按 struct 名，花括号配平）。 */
function goStructBody(source: string, structName: string): string {
  const at = source.indexOf('type ' + structName + ' struct {')
  if (at < 0) throw new Error('Go 源码里找不到 struct ' + structName + ' —— 路径或命名变了，本门禁需要同步')
  const open = source.indexOf('{', at)
  let depth = 0
  for (let i = open; i < source.length; i += 1) {
    if (source[i] === '{') depth += 1
    else if (source[i] === '}') {
      depth -= 1
      if (depth === 0) return source.slice(open + 1, i)
    }
  }
  throw new Error('struct ' + structName + ' 的花括号没有闭合')
}

/** 从 struct 体里取全部 json tag 名（跳过 "-" 与无 tag 行）。 */
function jsonFields(structBody: string): string[] {
  const out: string[] = []
  for (const m of structBody.matchAll(/`json:"([^"]+)"`/g)) {
    const name = m[1].split(',')[0].trim()
    if (name && name !== '-') out.push(name)
  }
  return out
}

/** 找出被打上 omitempty 的字段（后端契约明确要求**全部不加**）。 */
function omitemptyFields(structBody: string): string[] {
  const out: string[] = []
  for (const m of structBody.matchAll(/`json:"([^"]+)"`/g)) {
    const parts = m[1].split(',')
    if (parts.slice(1).map((s) => s.trim()).includes('omitempty')) out.push(parts[0].trim())
  }
  return out
}

/** 从 TS interface 块里取字段名（去注释）。 */
function tsInterfaceFields(source: string, interfaceName: string): string[] {
  const at = source.indexOf('export interface ' + interfaceName + ' {')
  if (at < 0) throw new Error('TS 类型里找不到 interface ' + interfaceName + ' —— 本门禁需要同步')
  const open = source.indexOf('{', at)
  const close = source.indexOf('\n}', open)
  if (close < 0) throw new Error('interface ' + interfaceName + ' 没有闭合')
  const body = source.slice(open + 1, close).replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
  return [...body.matchAll(/^\s*([a-z_][a-z0-9_]*)\??\s*:/gim)].map((m) => m[1])
}

describe('回填进度 JSON 契约：Go BackfillSnapshot ↔ TS BackfillProgress', () => {
  it('后端契约文件存在（不存在就报出来，绝不静默跳过）', () => {
    expect(
      existsSync(GO_FILE),
      'Go 源码不在预期路径：' + GO_FILE + '（后端可能改名/挪目录，本门禁需要同步）',
    ).toBe(true)
  })

  it('载荷恰好 8 个 key，且与 TS interface 逐字一致（双向）', () => {
    const go = jsonFields(goStructBody(readFileSync(GO_FILE, 'utf8'), 'BackfillSnapshot'))
    const ts = tsInterfaceFields(readFileSync(TS_FILE, 'utf8'), 'BackfillProgress')

    // 分母守卫：契约 §4.6 定义了 8 个 key；扫少了说明解析器坏了
    expect(go.length, 'Go BackfillSnapshot 只扫到 ' + go.length + ' 个 json tag（契约 §4.6 是 8 个）').toBe(8)
    expect(ts.length, 'TS BackfillProgress 只声明了 ' + ts.length + ' 个字段').toBe(8)

    expect(ts.slice().sort(), 'TS BackfillProgress 字段与后端 BackfillSnapshot 不一致').toEqual(go.slice().sort())
  })

  it('后端不得给任何字段加 omitempty（契约：8 个 key 恒定出现）', () => {
    const omits = omitemptyFields(goStructBody(readFileSync(GO_FILE, 'utf8'), 'BackfillSnapshot'))
    expect(
      omits,
      '出现 omitempty ⇒ 该 key 在零值时会消失，前端必须写防御分支，且「0 = 未知」的语义被破坏',
    ).toEqual([])
  })

  it('五个 State 字面量与后端常量逐字一致（含 disabled）', () => {
    const goSource = readFileSync(GO_FILE, 'utf8')
    const goStates = [
      ...goSource.matchAll(/BackfillState(?:Idle|Running|Done|Failed|Disabled)\s*=\s*"([a-z]+)"/g),
    ].map((m) => m[1])
    // 分母守卫：契约 §4.6 的 API 载荷是 5 态
    expect(goStates.length, 'Go BackfillState 常量只扫到 ' + goStates.length + ' 个（应为 5）').toBe(5)

    // 前端 Record<BackfillState, string> 的键集合必须与之逐字一致
    expect(Object.keys(BACKFILL_STATE_TEXT).sort(), 'BACKFILL_STATE_TEXT 的键与后端状态字面量不一致').toEqual(
      goStates.slice().sort(),
    )
    // 且这 5 态在前端都有确定的显示口径（不会有状态掉进"未定义"分支）
    for (const state of goStates) {
      const text = BACKFILL_STATE_TEXT[state as keyof typeof BACKFILL_STATE_TEXT]
      expect(typeof text, '状态 ' + state + ' 在前端没有文案').toBe('string')
      expect(text.length).toBeGreaterThan(0)
    }
  })

  it('disabled 必须可显示（运维关阀时不能空白）且可停止轮询（不再变化）', () => {
    // 这两条是**产品口径**而不是机械一致性问题，但放这里一起跑：
    // 它们共同定义「后端出现 disabled 时前端会怎样」，任何一端改了这里都会红。
    expect(shouldShowBackfill({ state: 'disabled' } as never), 'disabled 被隐藏 ⇒ 用户看不到「回填已关闭」').toBe(true)
    expect(isBackfillTerminal('disabled'), 'disabled 未被视为终态 ⇒ 会一直问一个不会变的值').toBe(true)
  })

  it('契约外的内部量（rows_done / partitions）不得出现在前端类型里', () => {
    const ts = tsInterfaceFields(readFileSync(TS_FILE, 'utf8'), 'BackfillProgress')
    for (const internalField of ['rows_done', 'partitions']) {
      expect(
        ts,
        internalField + ' 是引擎内部量，后端刻意没往外暴露；前端若声明了它，读到的永远是 undefined',
      ).not.toContain(internalField)
    }
  })
})
