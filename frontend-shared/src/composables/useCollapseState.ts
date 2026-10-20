import { ref, watch } from 'vue'

/**
 * BMS 详情页「诊断类区块」折叠状态（2026-10-20 移动端改版 §3.3）。
 *
 * 为什么按 deviceId 存 localStorage 而不是组件内 ref：
 *   同一个用户会看多台设备，A 设备展开的区块不该影响 B 设备；下次进入要记得上次的展开选择，
 *   否则每次刷新都要重新点开（这些区块默认折叠，记不住就等于每次多 3 次点击）。
 *
 * 存储格式：`bms-collapse:<deviceId>` = JSON 字符串数组，**只存已展开的 id**。
 * 只存展开项的好处：新增区块时天然是折叠态（默认值），不需要迁移旧数据。
 *
 * 失败策略（fail-open）：localStorage 不可用（隐私模式 / 配额满 / SSR）时退化为内存态，
 * 折叠交互仍然可用，只是不跨会话记忆 —— 绝不因为读不到存储就抛错或锁死折叠。
 */

const STORAGE_PREFIX = 'bms-collapse:'

function readOpenIds(key: string | null): string[] {
  if (!key || typeof localStorage === 'undefined') return []
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.filter((x): x is string => typeof x === 'string')
  } catch {
    // 写入时可能是旧版本格式或被外部污染：当作「没有记忆」处理
    return []
  }
}

function writeOpenIds(key: string | null, ids: string[]): void {
  if (!key || typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(key, JSON.stringify(ids))
  } catch {
    // 隐私模式 / 配额满：不记忆即可，不影响本次会话内的折叠交互
  }
}

export interface CollapseState {
  isOpen: (id: string) => boolean
  toggle: (id: string) => void
  setOpen: (id: string, open: boolean) => void
}

export function useCollapseState(deviceId: () => string | number | null | undefined) {
  const openIds = ref<Set<string>>(new Set())
  let currentKey: string | null = null

  function keyFor(): string | null {
    const id = deviceId()
    if (id === null || id === undefined || id === '' || Number(id) === 0) return null
    return STORAGE_PREFIX + String(id)
  }

  function reload(): void {
    currentKey = keyFor()
    openIds.value = new Set(readOpenIds(currentKey))
  }

  function commit(next: Set<string>): void {
    openIds.value = next
    writeOpenIds(currentKey ?? keyFor(), [...next])
  }

  const state: CollapseState = {
    isOpen: (id: string) => openIds.value.has(id),
    toggle: (id: string) => {
      const next = new Set(openIds.value)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      commit(next)
    },
    setOpen: (id: string, open: boolean) => {
      const next = new Set(openIds.value)
      if (open) next.add(id)
      else next.delete(id)
      commit(next)
    },
  }

  // 设备切换：路由参数变化后重新读该设备自己的记忆
  watch(deviceId, reload, { immediate: true })

  return state
}