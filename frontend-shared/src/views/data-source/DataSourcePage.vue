<template>
  <div class="data-source-page">
    <PageHeader
      title="数据来源"
      subtitle="来源总览由设备与来源组自动生成（只读）；主备/复合配置在「来源组」里维护"
    />

    <el-tabs :model-value="activeTab" class="ds-tabs" @update:model-value="onTabChange">
      <!--
        Tab1「来源总览」：只读。
        这里**刻意没有**任何新建/编辑/删除按钮 —— 不是隐藏，而是本 Tab 的
        组件契约里根本没有写操作（见 OverviewTab 的 props 与 emits）。
        后端 GET /data-sources/overview 也只注册 GET（写方法 405）。
      -->
      <el-tab-pane label="来源总览" name="overview">
        <OverviewTab
          :items="items"
          :loading="loading"
          :error="error"
          @open-group="openGroup"
          @retry="loadOverview"
        />
      </el-tab-pane>

      <!--
        Tab2「来源组」：唯一的可写入口（用户手动创建）。
        直接复用既有 SourceGroupList，不重写其逻辑 —— 避免两套 CRUD 实现漂移。
      -->
      <el-tab-pane label="来源组" name="groups">
        <SourceGroupList embedded />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup lang="ts">
/**
 * 数据来源页（数据流架构 v1 §五 M4）。
 *
 * # 为什么把「数据源」与「来源组」合成一个页面
 *
 * 改造前是两个独立菜单项（「数据源」「来源组」），用户看到前者永远是空的
 * （它曾是纯人工配置表，生产 0 行）就会困惑：设备明明在传数据，为什么没来源？
 *
 * 合并后语义清晰：
 * - **来源总览**（只读）：系统自动派生的事实，"数据从哪来"一眼可见
 * - **来源组**（可写）：用户唯一需要配置的东西，"要不要主备/复合"
 *
 * # Tab 由 route.query 驱动，不用内部 state
 *
 * 这样链接可分享、可收藏、可从别处直接跳到「来源组」Tab。
 */
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import PageHeader from '@/components/common/PageHeader.vue'
import OverviewTab from './OverviewTab.vue'
import SourceGroupList from '@/views/source-group/SourceGroupList.vue'
import { sourceOverviewApi, type SourceOverviewItem } from '@/api/sourceOverview'

const route = useRoute()
const router = useRouter()

const items = ref<SourceOverviewItem[]>([])
const loading = ref(false)
const error = ref('')

/** activeTab 完全由 URL 决定（单一真相）。 */
const activeTab = computed<'overview' | 'groups'>(() =>
  route.query.tab === 'groups' ? 'groups' : 'overview',
)

function onTabChange(name: string | number) {
  const tab = name === 'groups' ? 'groups' : 'overview'
  // 用 replace 避免污染浏览器历史（切 Tab 不该产生后退点）。
  void router.replace({ query: { ...route.query, tab } })
}

/** 跳到来源组 Tab（从总览的「来源组」链接点击时）。 */
function openGroup() {
  onTabChange('groups')
}

/**
 * 加载来源总览。
 *
 * @param silent true = 静默刷新（不显示 loading、失败不清空已有数据）。
 *
 * 为什么需要 silent 模式：实时刷新每 5s 跑一次，若每次都显示 loading，
 * 表格会不停闪烁；若失败就清空，用户会看到"数据突然消失"（实则一次网络抖动）。
 * 静默刷新让"表一直在，值在跳"，这才是实时的观感。
 */
async function loadOverview(silent = false) {
  if (!silent) {
    loading.value = true
    error.value = ''
  }
  try {
    const res = await sourceOverviewApi.list()
    items.value = res.items ?? []
    if (silent) error.value = ''
  } catch (e) {
    if (!silent) {
      // 首次加载失败：显示错误（用户需要知道为什么空）
      error.value = e instanceof Error ? e.message : String(e)
      items.value = []
    }
    // 静默刷新失败：保留上一次的数据，不打断用户阅读 —— 下一次轮询会自愈。
  } finally {
    if (!silent) loading.value = false
  }
}

// ── 实时刷新（自适应轮询）──
//
// # 为什么用轮询而不是 WebSocket
//
// 本页展示的是**跨设备聚合视图**（一个来源可能由多台设备构成）。WS 是按
// device_id 推送的，要覆盖全部来源就得订阅"所有设备"，既复杂又容易漏
// （新增设备后忘记订阅 ⇒ 那个来源永远静止）。而 /overview 是只读聚合端点，
// 一次请求就拿到全量快照 —— 轮询在这里更简单且不会漏。
//
// # 为什么 5s 且"页面隐藏时暂停"
//
// 采集周期常见 5s（设备 IntervalMs），5s 轮询足以让用户感觉是实时的。
// 页面不可见时暂停：用户切到别的标签页后继续每 5s 请求是纯浪费
// （后端要查库 + 计算），这在多用户场景下会放大成无意义的负载。
const REFRESH_MS = 5000
let refreshTimer: ReturnType<typeof setInterval> | null = null

function startRefresh() {
  stopRefresh()
  refreshTimer = setInterval(() => {
    // 只在总览 Tab 且页面可见时刷新
    if (activeTab.value !== 'overview') return
    if (typeof document !== 'undefined' && document.hidden) return
    void loadOverview(true)
  }, REFRESH_MS)
}

function stopRefresh() {
  if (refreshTimer !== null) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
}

/** 页面重新可见时立刻补一次（暂停期间的数据已经过时了）。 */
function onVisibilityChange() {
  if (typeof document !== 'undefined' && !document.hidden && activeTab.value === 'overview') {
    void loadOverview(true)
  }
}

// 只在总览 Tab 时加载（切到来源组不需要总览数据）。
watch(activeTab, (tab) => {
  if (tab === 'overview' && items.value.length === 0 && !loading.value) {
    void loadOverview()
  }
})

onMounted(() => {
  if (activeTab.value === 'overview') void loadOverview()
  startRefresh()
  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', onVisibilityChange)
  }
})

onUnmounted(() => {
  stopRefresh()
  if (typeof document !== 'undefined') {
    document.removeEventListener('visibilitychange', onVisibilityChange)
  }
})
</script>

<style scoped>
.data-source-page {
  padding: 0;
}
.ds-tabs {
  margin-top: 12px;
}
</style>
