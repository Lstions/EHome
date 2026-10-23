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
import { computed, onMounted, ref, watch } from 'vue'
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

async function loadOverview() {
  loading.value = true
  error.value = ''
  try {
    const res = await sourceOverviewApi.list()
    items.value = res.items ?? []
  } catch (e) {
    // 降级：不阻塞页面，只提示（来源组 Tab 仍可用）。
    error.value = e instanceof Error ? e.message : String(e)
    items.value = []
  } finally {
    loading.value = false
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
