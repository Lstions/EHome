<template>
  <div class="data-source-page">
    <PageHeader
      title="数据来源"
      subtitle="设备解析成功上报的字段目录（只读）；把多台设备组合成一个来源请到「来源组」"
    />

    <el-tabs :model-value="activeTab" class="ds-tabs" @update:model-value="onTabChange">
      <!--
        Tab1「字段目录」：只读。
        这里**刻意没有**任何新建/编辑/删除按钮 —— 不是隐藏，而是本 Tab 的
        组件契约里根本没有写操作（见 OverviewTab 的 props 与 emits）。
        后端 GET /data-sources/overview 也只注册 GET（写方法 405）。
      -->
      <el-tab-pane label="字段目录" name="overview">
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
 * 数据来源页。
 *
 * # 两个 Tab 的职责划分（2026-10-23 纠正过一次概念混淆）
 *
 * - **字段目录**（只读）：设备解析成功之后，系统里**有哪些字段** ——
 *   类别、中文描述、数据类型、单位、实时值、更新时间、数据新鲜度。
 *   它由采集结果自动生成，用户不需要也不能维护。
 * - **来源组**（可写）：把**多台设备**组合成一个数据来源（主备 / 复合）。
 *   这是用户唯一需要手工配置的东西。
 *
 * # 曾经的设计错误（必须记住，否则会重犯）
 *
 * 早期把「数据来源」当成主备配置台账来渲染，于是只读的字段目录里出现了
 * 「来源类型 / 成员 / 当前来源 / 来源组」四个**配置概念**的列：
 *   · 用户在只读页看到配置状态却无法配置；
 *   · 「当前来源」与「逻辑设备」重复显示同一台设备；
 *   · 「成员」在单设备时恒为 1，零信息量。
 * 根因是混淆了两张表的语义：data_sources 是**字段出现记录**，
 * 不是"多设备里选哪台供数"的配置。后者属于 source_groups。
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
