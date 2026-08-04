<template>
  <div class="logical-device-candidate-select">
    <!-- loading 骨架 (§1.3: candidates 是异步聚合查询, 不阻塞表单) -->
    <div v-if="loading" class="candidate-loading" data-testid="candidate-loading" role="status" aria-label="正在加载候选列表">
      <div class="candidate-skeleton" v-for="n in 3" :key="n" />
      <span class="candidate-loading-text">正在加载可继承的逻辑设备…</span>
    </div>

    <!-- 类型未知: 候选查询必须带 type (§1.3), 型号选定前给出引导 -->
    <div v-else-if="!hasType" class="candidate-awaiting-type" data-testid="candidate-awaiting-type">
      <el-icon><InfoFilled /></el-icon>
      <span>请先选择设备型号（下一步），然后在此选择要继承的历史设备</span>
    </div>

    <!-- 失败降级: 提示 + 重试, 不阻塞"作为新设备创建" -->
    <el-alert
      v-else-if="loadError"
      type="warning"
      :closable="false"
      show-icon
      data-testid="candidate-error"
    >
      <template #title>候选列表加载失败</template>
      <span>{{ loadError }}</span>
      <el-button link type="primary" size="small" @click="load" style="margin-left: 8px;">重试</el-button>
    </el-alert>

    <!-- 空候选: 无历史数据可继承 -->
    <el-empty
      v-else-if="candidates.length === 0"
      description="该类型暂无可继承的历史数据，将作为新设备创建"
      :image-size="60"
      data-testid="candidate-empty"
    />

    <!-- 候选列表: 权重排序 (服务端已排), 单选 -->
    <div v-else class="candidate-list" data-testid="candidate-list" role="radiogroup" aria-label="可继承的逻辑设备">
      <label
        v-for="c in candidates"
        :key="c.id"
        class="candidate-card"
        :class="{ selected: modelValue === c.id }"
      >
        <input
          type="radio"
          class="candidate-radio"
          name="logical-device-candidate"
          :checked="modelValue === c.id"
          :value="c.id"
          @change="select(c)"
        />
        <div class="candidate-info">
          <div class="candidate-title">
            <span class="candidate-name">{{ c.name }}</span>
            <el-tag size="small" :type="weightTagType(c.match_weight)">{{ weightLabel(c.match_weight) }}</el-tag>
          </div>
          <div class="candidate-meta">
            <span>{{ c.device_type }}</span>
            <span class="meta-sep">·</span>
            <span>{{ c.instance_count }} 个实例</span>
            <span class="meta-sep">·</span>
            <span>保留 {{ c.retention_days }} 天</span>
            <span class="meta-sep">·</span>
            <span>最后数据 {{ formatTime(c.last_data_at) }}</span>
            <template v-if="c.row_estimate !== undefined">
              <span class="meta-sep">·</span>
              <span>约 {{ c.row_estimate }} 条数据</span>
            </template>
          </div>
        </div>
      </label>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { InfoFilled } from '@element-plus/icons-vue'
import { edgeDeviceApi, type LogicalDeviceCandidate } from '@/api/edgeDevice'

// 方案 v3.3 §3.1/§1.3: 创建继承候选列表组件 (EdgeDeviceList 3 步向导
// 步骤 0 与 QuickCreateDeviceDialog 折叠区共用)。
//
// 语义: 单选一个逻辑设备作为继承目标 (update:modelValue = id); 清空选择
// 时 emit null (父级按"作为新设备创建"处理)。loading 骨架 + 失败降级,
// 失败不阻塞创建流程 (父级仍可提交不带 logical_device_id 的请求)。

// withDefaults: active 缺省必须为 true (EdgeDeviceList 步骤 0 不传 active)。
// 不能用裸 `active?: boolean` 声明——Vue 对缺省 Boolean prop 强制置 false,
// 会导致 `props.active !== false` 恒假、候选列表永不加载。
const props = withDefaults(
  defineProps<{
    // 继承目标的设备类型 (candidates 必填参数)。为空不加载。
    type: string
    // 创建位置上下文 — 用于权重排序 (服务端计算)。
    nodeId?: string
    hardwareId?: string
    channelId?: number
    // 已选逻辑设备 id; null = 未选择 (作为新设备创建)。
    modelValue: number | null
    // 延迟加载开关: false 时不发请求 (折叠区未展开)。缺省 true。
    active?: boolean
  }>(),
  { active: true },
)

const emit = defineEmits<{
  'update:modelValue': [value: number | null]
}>()

const candidates = ref<LogicalDeviceCandidate[]>([])
const loading = ref(false)
const loadError = ref('')
// generation guard: 快速切换 type/node 时丢弃过期响应。
let loadGeneration = 0

// type 是否就绪 (candidates 必填参数)。未就绪显示引导而非空列表。
const hasType = computed(() => Boolean(props.type?.trim()))

const load = async () => {
  const type = props.type?.trim()
  if (!type) {
    candidates.value = []
    return
  }
  const generation = ++loadGeneration
  loading.value = true
  loadError.value = ''
  try {
    const list = await edgeDeviceApi.getCandidates({
      type,
      node_id: props.nodeId || undefined,
      hardware_id: props.hardwareId || undefined,
      channel_id: props.channelId,
    })
    if (generation !== loadGeneration) return
    candidates.value = list
    // 已选项不再出现在新候选集 (如 type 变化) 时清空。
    if (props.modelValue !== null && !list.some(c => c.id === props.modelValue)) {
      emit('update:modelValue', null)
    }
  } catch (e: any) {
    if (generation !== loadGeneration) return
    loadError.value = e?.message || '网络错误'
    candidates.value = []
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

const select = (c: LogicalDeviceCandidate) => {
  // 再次点击已选项 → 取消选择 (回到"作为新设备创建")。
  emit('update:modelValue', props.modelValue === c.id ? null : c.id)
}

// 权重文案 (§1.3 五档): 用匹配语义而非数字。
const weightLabel = (w: number): string => {
  switch (w) {
    case 100: return '原位置重建'
    case 80: return '同节点同地址'
    case 60: return '同节点'
    case 40: return '同地址异节点'
    default: return '同类型'
  }
}

const weightTagType = (w: number): '' | 'success' | 'warning' | 'info' => {
  if (w >= 100) return 'success'
  if (w >= 60) return ''
  return 'info'
}

const formatTime = (t: string | null): string => {
  if (!t) return '无'
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return '无'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// 延迟加载: active && type 就绪才请求; 上下文变化重查。
// active === false (折叠区未展开/向导未到该步) 时不发请求。
watch(
  () => [props.active !== false, props.type, props.nodeId, props.hardwareId, props.channelId] as const,
  ([isActive]) => {
    if (!isActive) return
    void load()
  },
  { immediate: true },
)

defineExpose({ load, candidates, loading, loadError })
</script>

<style scoped>
.logical-device-candidate-select {
  width: 100%;
}

.candidate-loading {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 8px 0;
}

.candidate-skeleton {
  height: 44px;
  border-radius: 6px;
  background: linear-gradient(90deg, var(--el-fill-color-light) 25%, var(--el-fill-color) 37%, var(--el-fill-color-light) 63%);
  background-size: 400% 100%;
  animation: candidate-skeleton 1.4s ease infinite;
}

@keyframes candidate-skeleton {
  0% { background-position: 100% 50%; }
  100% { background-position: 0 50%; }
}

.candidate-loading-text {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.candidate-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 320px;
  overflow-y: auto;
}

.candidate-card {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--el-border-color);
  border-radius: 8px;
  cursor: pointer;
  transition: border-color 0.15s, background 0.15s;
}

.candidate-card:hover {
  border-color: var(--el-color-primary-light-5);
}

.candidate-card.selected {
  border-color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
}

.candidate-radio {
  margin-top: 4px;
}

.candidate-info {
  flex: 1;
  min-width: 0;
}

.candidate-title {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.candidate-name {
  font-weight: 600;
  font-size: 14px;
}

.candidate-meta {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  display: flex;
  flex-wrap: wrap;
  gap: 2px;
}

.meta-sep {
  margin: 0 4px;
  color: var(--el-border-color);
}
</style>
