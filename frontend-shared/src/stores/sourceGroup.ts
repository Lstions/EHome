import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  sourceGroupApi,
  type SourceGroup,
  type SourceGroupListParams,
  type CreateSourceGroupRequest,
  type UpdateSourceGroupRequest,
} from '@/api/sourceGroup'

/**
 * 来源组 store（设计 §4 / 契约 M0）。
 *
 * 与 stores/dataSource.ts 同范式：列表 + CRUD + 状态操作；成功后本地替换，失败写 error
 * 并 rethrow（由页面提示）。差异（语义要点）：
 *   - composite 组**没有 active**，activate 在后端 400 —— store 不做语义猜测，只透传；
 *   - revision 是并发/清窗依据：更新时把当前 revision 提交，页面展示它也便于排障。
 */
export const useSourceGroupStore = defineStore('sourceGroup', () => {
  const items = ref<SourceGroup[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)

  const primaryCount = computed(() => items.value.filter(g => g.kind === 'primary_backup').length)
  const compositeCount = computed(() => items.value.filter(g => g.kind === 'composite').length)
  const enabledCount = computed(() => items.value.filter(g => g.enabled).length)

  function captureError(err: unknown) {
    error.value = err instanceof Error ? err.message : String(err)
  }
  function clearError() {
    error.value = null
  }
  function replaceItem(updated: SourceGroup) {
    const idx = items.value.findIndex(g => g.id === updated.id)
    if (idx >= 0) items.value[idx] = updated
    else items.value.push(updated)
  }

  async function fetchList(params?: SourceGroupListParams) {
    loading.value = true
    error.value = null
    try {
      const res = await sourceGroupApi.list(params)
      items.value = Array.isArray(res.items) ? res.items : []
      total.value = typeof res.total === 'number' ? res.total : items.value.length
      return res
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  async function createGroup(data: CreateSourceGroupRequest) {
    const created = await sourceGroupApi.create(data)
    items.value = [created, ...items.value]
    total.value += 1
    return created
  }

  async function updateGroup(id: number, data: UpdateSourceGroupRequest) {
    const updated = await sourceGroupApi.update(id, data)
    replaceItem(updated)
    return updated
  }

  async function removeGroup(id: number) {
    await sourceGroupApi.remove(id)
    items.value = items.value.filter(g => g.id !== id)
    total.value = Math.max(0, total.value - 1)
  }

  async function activateGroup(id: number) {
    const updated = await sourceGroupApi.activate(id)
    replaceItem(updated)
    return updated
  }

  async function deactivateGroup(id: number) {
    const updated = await sourceGroupApi.deactivate(id)
    replaceItem(updated)
    return updated
  }

  /** 拉取单个组（编辑前取最新 members/revision；列表接口若已带 members 可省） */
  async function fetchGroup(id: number) {
    const group = await sourceGroupApi.get(id)
    replaceItem(group)
    return group
  }

  /** 启用/停用（复用 update；只提交 enabled+revision） */
  async function setEnabled(group: SourceGroup, enabled: boolean) {
    return updateGroup(group.id, { enabled, revision: group.revision })
  }

  return {
    items,
    total,
    loading,
    error,
    primaryCount,
    compositeCount,
    enabledCount,
    clearError,
    fetchList,
    fetchGroup,
    createGroup,
    updateGroup,
    removeGroup,
    activateGroup,
    deactivateGroup,
    setEnabled,
  }
})
