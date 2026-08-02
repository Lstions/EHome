<template>
  <el-dialog
    v-model="visible"
    title="创建边缘设备"
    width="560px"
    :close-on-click-modal="false"
    :close-on-press-escape="!submitting"
    :show-close="!submitting"
    class="quick-create-device-dialog"
  >
    <div class="step-tip">
      <el-icon><InfoFilled /></el-icon>
      <span>在节点 <strong>{{ nodeName || nodeId }}</strong> 上直接创建设备，无需预先创建设备模板</span>
    </div>

    <el-form ref="formRef" :model="form" :rules="rules" label-position="top" style="margin-top: 16px;">
      <!-- 设备型号(解析器) -->
      <el-form-item label="设备型号" prop="parserId">
        <el-select
          v-model="form.parserId"
          placeholder="选择设备型号(决定原始字节到物理量的解析方式)"
          filterable
          style="width: 100%;"
          @change="onParserChange"
        >
          <el-option
            v-for="p in availableParsers"
            :key="p.id"
            :label="`${p.name} (${p.id})`"
            :value="p.id"
          >
            <div class="parser-option">
              <span>{{ p.name }}</span>
              <el-tag
                v-for="bus in p.hardware_types"
                :key="bus"
                size="small"
                style="margin-left: 6px;"
              >{{ bus.toUpperCase() }}</el-tag>
            </div>
          </el-option>
        </el-select>
      </el-form-item>

      <!-- 通道(该节点已有通道,按解析器总线类型过滤) -->
      <el-form-item label="通道" prop="channelId">
        <el-select
          v-model="form.channelId"
          placeholder="选择该节点下的已有通道"
          style="width: 100%;"
          :disabled="!form.parserId || channelsLoading"
          :loading="channelsLoading"
        >
          <el-option
            v-for="ch in filteredChannels"
            :key="ch.id"
            :label="`${(ch.hardware_type || '').toUpperCase()} ${ch.hardware_id}${ch.address ? ' / ' + ch.address : ''}`"
            :value="ch.id"
          />
        </el-select>
        <!-- R2: 区分"加载中"与"加载完但无匹配通道"两种状态 -->
        <div v-if="form.parserId && channelsLoading" class="channel-hint channel-hint--loading">
          正在加载通道…
        </div>
        <div v-else-if="form.parserId && filteredChannels.length === 0" class="channel-hint">
          该节点暂无匹配 {{ selectedParserBusTypesText }} 的通道，请先在下方"总线配置"中创建
        </div>
      </el-form-item>

      <!-- 设备名称 -->
      <el-form-item label="设备名称" prop="name">
        <el-input v-model="form.name" placeholder="请输入边缘设备名称" />
      </el-form-item>

      <!-- 采集间隔 -->
      <el-form-item label="采集间隔 (ms)" prop="interval_ms">
        <el-input-number v-model="form.interval_ms" :min="100" :max="3600000" :step="100" />
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="visible = false" :disabled="submitting">取消</el-button>
      <el-button type="primary" :loading="submitting" :disabled="!canSubmit" @click="handleSubmit">
        创建设备
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { InfoFilled } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useParserStore } from '@/stores/parser'
import { edgeDeviceApi } from '@/api/edgeDevice'
import type { Parser } from '@/api/parser'
import type { Channel } from '@/api/channel'

export interface QuickCreateDeviceDialogProps {
  modelValue: boolean
  nodeId: string          // 节点物理序列号(EdgeDevice.NodeID)
  nodeName?: string       // 节点显示名
  channels: Channel[]     // 该节点已有通道
  channelsLoading?: boolean // 通道加载中(R2: 区分"加载中"与"无匹配通道")
}

const props = withDefaults(defineProps<QuickCreateDeviceDialogProps>(), {
  nodeName: '',
  channels: () => [],
  channelsLoading: false,
})

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  'created': []
}>()

const parserStore = useParserStore()

const visible = computed({
  get: () => props.modelValue,
  set: (val) => emit('update:modelValue', val),
})

const formRef = ref()
const submitting = ref(false)

const form = reactive({
  parserId: '' as string,
  channelId: undefined as number | undefined,
  name: '',
  interval_ms: 1000,
})

const rules = {
  parserId: [{ required: true, message: '请选择设备型号', trigger: 'change' }],
  channelId: [{ required: true, message: '请选择通道', trigger: 'change' }],
  name: [{ required: true, message: '请输入设备名称', trigger: 'blur' }],
}

// 可用设备型号(有硬件类型定义的)
const availableParsers = computed(() =>
  parserStore.parsers.filter(p => (p.hardware_types || []).length > 0),
)

const selectedParser = computed<Parser | null>(() =>
  availableParsers.value.find(p => p.id === form.parserId) || null,
)

// 按解析器总线类型过滤该节点通道(hardware_type 大小写归一)
const filteredChannels = computed(() => {
  if (!selectedParser.value) return []
  const buses = (selectedParser.value.hardware_types || []).map(b => b.toLowerCase())
  return props.channels.filter(ch =>
    ch && buses.includes((ch.hardware_type || '').toLowerCase()),
  )
})

const selectedParserBusTypesText = computed(() =>
  (selectedParser.value?.hardware_types || []).map(b => b.toUpperCase()).join('/'),
)

// R2: 通道加载中状态(来自父级 NodeDetail 的 devicesLoading)
const channelsLoading = computed(() => props.channelsLoading)

const selectedChannel = computed<Channel | null>(() =>
  props.channels.find(ch => ch.id === form.channelId) || null,
)

const canSubmit = computed(() =>
  Boolean(form.parserId && form.channelId && form.name.trim()),
)

const onParserChange = () => {
  // 切换设备型号时清空已选通道(可能不再匹配)
  form.channelId = undefined
}

const reset = () => {
  form.parserId = ''
  form.channelId = undefined
  form.name = ''
  form.interval_ms = 1000
}

const handleSubmit = async () => {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
  } catch {
    return
  }
  const ch = selectedChannel.value
  if (!ch || !selectedParser.value) return

  submitting.value = true
  try {
    // driver-backed 创建: device_config_id=0(无模板),type=parser.id
    await edgeDeviceApi.create({
      name: form.name.trim(),
      node_id: String(props.nodeId),
      channel_id: ch.id,
      hardware_id: ch.hardware_id,
      type: selectedParser.value.id,
      interval_ms: form.interval_ms,
    })
    ElMessage.success('创建成功')
    visible.value = false
    reset()
    emit('created')
  } catch (error: any) {
    ElMessage.error(error?.message || '创建失败')
  } finally {
    submitting.value = false
  }
}

watch(() => props.modelValue, (val) => {
  if (val && parserStore.parsers.length === 0) {
    parserStore.fetchParsers()
  }
  // P3: 取消/关闭时重置表单,避免下次打开残留上次输入
  if (!val) {
    reset()
  }
})
</script>

<style scoped>
.step-tip {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 16px;
  background: var(--el-color-success-light-9);
  border-radius: 6px;
  font-size: 13px;
  color: var(--el-text-color-regular);
}

.parser-option {
  display: flex;
  align-items: center;
}

.channel-hint {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-color-warning);
}

.channel-hint--loading {
  color: var(--el-text-color-secondary);
}
</style>
