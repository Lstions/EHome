/**
 * 节点展示名（单一真源）。
 *
 * 背景（2026-10-21 A-nodes 审计 P1）：生产后端 nodes.name 允许为空（实测 3/3 全空），
 * 而调用点直接渲染 name，于是出现：
 *   · 节点列表删除确认：`确定要删除节点 "" 吗？`（不知道删的是哪个）；
 *   · 通道页节点筛选下拉：3 个空白选项（无法按节点筛选）；
 *   · 节点卡片标题空白 / 读屏名变成「查看节点  详情」。
 *
 * 回退顺序（信息量从高到低）：
 *   ① name.trim() —— 用户起的名字；
 *   ② `节点 <node_id>` —— 物理序列号（后端 json tag 是 string，生产形如 'F0F5BDFFFE02'）；
 *   ③ `节点 <id>` —— nodes 表自增主键（旧后端/mock 可能不返回 node_id）；
 *   ④ '未命名节点' —— 连主键都没有（正常数据不可能走到，避免渲染空串）。
 *
 * 注意 trim：名字可能是纯空白，此时同样视为"没有名字"。
 * 这是个**纯函数**（无 Vue/无 IO），可被列表页/通道页/详情页共同复用，
 * 避免"每个页面各写一套回退"再次漂移。
 */
export interface NodeDisplayLike {
  name?: string | null
  node_id?: string | number | null
  id?: string | number | null
}

function nonEmptyText(value: unknown): string {
  if (value === undefined || value === null) return ''
  return String(value).trim()
}

export function nodeDisplayName(node?: NodeDisplayLike | null): string {
  const name = typeof node?.name === 'string' ? node.name.trim() : ''
  if (name) return name
  const serial = nonEmptyText(node?.node_id)
  if (serial) return `节点 ${serial}`
  const id = nonEmptyText(node?.id)
  if (id) return `节点 ${id}`
  return '未命名节点'
}
