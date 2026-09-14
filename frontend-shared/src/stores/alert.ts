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

  // ── 事件 (服务端真分页, 与 useAutomationStore 同范式) ──
  const events = ref<AlertEvent[]>([])
  const eventsLoading = ref(false)
  /** 过滤后**全量**条数 (不是当前页条数): 分页器据此算总页数。 */
  const eventsTotal = ref(0)
  const eventsPage = ref(1)
  const eventsPageSize = ref(20)
  /**
   * 当前页的 firing 计数。
   *
   * 注意口径: 真分页后 events 只装**当前页**, 因此本值是"本页 firing 数"而非
   * "全量 firing 数"。后端没有提供 state=firing 的计数端点, 前端也就无法在不
   * 额外拉全量的前提下算出全量 —— 故保留本值但由视图层显式标注口径
   * (AlertRules.vue: "本页 N firing"), 不让它冒充全量。
   */
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

  /**
   * 拉取事件列表 (时间线, 服务端分页)。
   *
   * 契约 (P1.2): 响应 data = {items,total,page,page_size}。
   * - `events` 只装**当前页**, 不是全量切片 —— 否则分页器会退化成装饰;
   * - `eventsTotal` 用后端回显的 total (过滤后全量), 不是 `events.length`;
   * - 回显的 page/page_size 反写本地, 保证 UI 与后端实际使用的值一致
   *   (例如后端把 page_size=0 clamp 成 20 时, 分页器不会停在 0)。
   */
  async function fetchEvents(params?: AlertEventListParams) {
    eventsLoading.value = true
    try {
      const req: AlertEventListParams = {
        page: params?.page ?? eventsPage.value,
        page_size: params?.page_size ?? eventsPageSize.value,
        ...(params?.rule_id ? { rule_id: params.rule_id } : {}),
        ...(params?.state ? { state: params.state } : {}),
        ...(params?.start_time ? { start_time: params.start_time } : {}),
        ...(params?.end_time ? { end_time: params.end_time } : {}),
      }
      const res = await alertApi.listEvents(req)
      events.value = res.items
      eventsTotal.value = res.total
      eventsPage.value = res.page
      eventsPageSize.value = res.page_size
    } finally {
      eventsLoading.value = false
    }
  }

  /** 翻页 / 改页长: 先更新本地派生状态, 再按显式参数重查。 */
  async function setEventsPage(page: number, pageSize?: number) {
    eventsPage.value = page
    if (pageSize !== undefined) eventsPageSize.value = pageSize
    await fetchEvents({ page: eventsPage.value, page_size: eventsPageSize.value })
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
    eventsTotal,
    eventsPage,
    eventsPageSize,
    firingCount,
    fetchRules,
    createRule,
    updateRule,
    deleteRule,
    setRuleEnabled,
    fetchEvents,
    setEventsPage,
    markEventsRead,
  }
})
