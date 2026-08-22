import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
  alertApi,
  type AlertRule,
  type AlertEvent,
  type CreateAlertRuleRequest,
  type UpdateAlertRuleRequest,
  type AlertEventListParams,
} from '@/api/alert'

/**
 * 阈值告警 store (方案 v0.4 §5.1.4 任务C)
 * 规则 CRUD + 事件时间线数据; CRUD 后本地同步更新, 失败回滚由调用方提示。
 */
export const useAlertStore = defineStore('alert', () => {
  // ── 规则 ──
  const rules = ref<AlertRule[]>([])
  const rulesLoading = ref(false)

  // ── 事件 ──
  const events = ref<AlertEvent[]>([])
  const eventsLoading = ref(false)
  const firingCount = computed(() => events.value.filter(e => e.state === 'firing').length)

  /** 拉取规则列表 */
  async function fetchRules() {
    rulesLoading.value = true
    try {
      // alertApi 用 unwrap 模式 (拦截器返回 envelope, 解包后直接是数据)。
      rules.value = await alertApi.listRules()
    } finally {
      rulesLoading.value = false
    }
  }

  /** 创建规则; 成功后本地追加 */
  async function createRule(req: CreateAlertRuleRequest) {
    const created = await alertApi.createRule(req)
    rules.value = [created, ...rules.value]
    return created
  }

  /** 更新规则; 成功后本地替换 */
  async function updateRule(id: number, req: UpdateAlertRuleRequest) {
    const updated = await alertApi.updateRule(id, req)
    const idx = rules.value.findIndex(r => r.id === id)
    if (idx >= 0) rules.value[idx] = updated
    return updated
  }

  /** 删除规则; 成功后本地移除 */
  async function deleteRule(id: number) {
    await alertApi.deleteRule(id)
    rules.value = rules.value.filter(r => r.id !== id)
  }

  /** 切换启用开关 (表格 switch); 成功后本地替换 */
  async function setRuleEnabled(id: number, enabled: boolean) {
    const updated = await alertApi.setRuleEnabled(id, enabled)
    const idx = rules.value.findIndex(r => r.id === id)
    if (idx >= 0) rules.value[idx] = updated
    return updated
  }

  /** 拉取事件列表 (时间线) */
  async function fetchEvents(params?: AlertEventListParams) {
    eventsLoading.value = true
    try {
      events.value = await alertApi.listEvents(params)
    } finally {
      eventsLoading.value = false
    }
  }

  /** 标记事件已读 (经规则回链 Notification) */
  async function markEventsRead(ids: number[]) {
    await alertApi.markEventsRead(ids)
  }

  return {
    rules,
    rulesLoading,
    events,
    eventsLoading,
    firingCount,
    fetchRules,
    createRule,
    updateRule,
    deleteRule,
    setRuleEnabled,
    fetchEvents,
    markEventsRead,
  }
})
