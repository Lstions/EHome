import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

/**
 * 前后端维护态 JSON 契约门禁（回归锁）。
 *
 * 为什么需要它：维护页的全部展示都建立在后端 `internal/bootstrap` 的 json tag 上，
 * 而这是**两个包、两个人、两套类型系统**（Go struct ↔ TS interface）。
 * 任何一侧改字段名（`elapsed_ms` → `elapsedMs`、`build_commit` → `buildCommit`）
 * 都**不会**被 vue-tsc 或 go build 发现 —— 只会让前端进度条永远显示 0%、
 * 版本号永远不出现，而且测试全绿（因为前端单测用的是自己造的快照）。
 *
 * 判据（fail-closed）：
 *   ① 从 Go 源码扫出 Snapshot / Step 的全部 json tag；
 *   ② 断言 TS 侧 `src/types/system.ts` 逐字声明了同一组字段；
 *   ③ 双向断言（Go 有 TS 没有 = 前端看不到该信息；TS 有 Go 没有 = 前端在读 undefined）；
 *   ④ 分母守卫：扫到的字段数必须 >= 契约 §3.1 的已知下限。
 *
 * 变红条件：后端改字段名 / 前端抄错字段名 / 任一侧新增字段而另一侧没跟上。
 */

const ROOT = process.cwd()
const GO_FILE = join(ROOT, '..', 'backend', 'internal', 'bootstrap', 'tracker.go')
const TS_FILE = join(ROOT, 'src', 'types', 'system.ts')

/** 取 Go struct 定义块（按 struct 名）。 */
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

/** 从 struct body 里取全部 json tag 名（跳过 "-" 与无 tag 行）。 */
function jsonFields(structBody: string): string[] {
  const out: string[] = []
  for (const m of structBody.matchAll(/`json:"([^"]+)"`/g)) {
    const name = m[1].split(',')[0].trim()
    if (name && name !== '-') out.push(name)
  }
  return out
}

/** 从 TS interface 块里取出字段名（去注释）。 */
function tsInterfaceFields(source: string, interfaceName: string): string[] {
  const at = source.indexOf('export interface ' + interfaceName + ' {')
  if (at < 0) throw new Error('TS 类型里找不到 interface ' + interfaceName + ' —— 本门禁需要同步')
  const open = source.indexOf('{', at)
  const close = source.indexOf('\n}', open)
  if (close < 0) throw new Error('interface ' + interfaceName + ' 没有闭合')
  const body = source.slice(open + 1, close).replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
  return [...body.matchAll(/^\s*([a-z_][a-z0-9_]*)\??\s*:/gim)].map((m) => m[1])
}

describe('维护态 JSON 契约：Go struct ↔ TS interface 字段必须逐字一致', () => {
  it('backend/internal/bootstrap 存在（否则本门禁无从判定，直接报出而不是静默跳过）', () => {
    expect(existsSync(GO_FILE), 'Go 源码不在预期路径：' + GO_FILE).toBe(true)
  })

  it('Snapshot 字段全集一致', () => {
    const go = jsonFields(goStructBody(readFileSync(GO_FILE, 'utf8'), 'Snapshot'))
    const ts = tsInterfaceFields(readFileSync(TS_FILE, 'utf8'), 'MaintenanceSnapshot')

    // 分母守卫：契约 §3.1 列了 11 个字段，扫出更少说明解析器坏了
    expect(go.length, 'Go Snapshot 只扫到 ' + go.length + ' 个 json tag，解析器或契约已变').toBeGreaterThanOrEqual(10)

    expect(ts.slice().sort(), 'TS MaintenanceSnapshot 字段与后端 Snapshot 不一致').toEqual(go.slice().sort())
  })

  it('Step 字段全集一致', () => {
    const go = jsonFields(goStructBody(readFileSync(GO_FILE, 'utf8'), 'Step'))
    const ts = tsInterfaceFields(readFileSync(TS_FILE, 'utf8'), 'MaintenanceStep')

    expect(go.length, 'Go Step 只扫到 ' + go.length + ' 个 json tag').toBeGreaterThanOrEqual(8)
    expect(ts.slice().sort(), 'TS MaintenanceStep 字段与后端 Step 不一致').toEqual(go.slice().sort())
  })

  it('五态取值与后端 StepState 常量逐字一致', () => {
    const goSource = readFileSync(GO_FILE, 'utf8')
    const goStates = [...goSource.matchAll(/Step(?:Pending|Running|Done|Failed|Skipped)\s+StepState\s*=\s*"([a-z]+)"/g)].map((m) => m[1])
    // 分母守卫：契约 §3.1 定义了 5 种状态
    expect(goStates.length, 'Go StepState 常量只扫到 ' + goStates.length + ' 个').toBe(5)
    const tsSource = readFileSync(TS_FILE, 'utf8')
    for (const state of goStates) {
      expect(tsSource, 'TS MaintenanceStepState 缺少 ' + state).toContain("'" + state + "'")
    }
  })
})
