<template>
  <el-dialog
    v-model="dialogVisible"
    title="删除边缘设备"
    width="520px"
    align-center
    :close-on-click-modal="false"
    aria-label="删除边缘设备确认"
  >
    <template v-if="device">
      <!-- 基本信息：本地数据，打开即显示，无延迟 (方案 v3.3 §2.1) -->
      <div class="delete-device-facts">
        <div class="fact-row">
          <span class="fact-label">设备名称</span>
          <span class="fact-value">{{ device.name || UNKNOWN }}</span>
        </div>
        <div class="fact-row">
          <span class="fact-label">设备类型</span>
          <span class="fact-value">{{ device.device_type || UNKNOWN }}</span>
        </div>
        <div class="fact-row">
          <span class="fact-label">所属节点</span>
          <span class="fact-value">{{ device.node?.name || ('#' + device.node_id) }}</span>
        </div>
        <div class="fact-row">
          <span class="fact-label">通道</span>
          <span class="fact-value">{{ channelLabel }}</span>
        </div>
      </div>

      <!-- 数据规模区：异步加载，loading 骨架，失败降级为不显示
           （2026-10-23 逻辑设备移除后，这里展示的是**本设备自己的**数据量与
           系统级保留期 —— 原「逻辑设备信息」区展示的是逻辑身份的实例数/保留天数，
           那是用户无法操作、也无法验证的内部概念。） -->
      <div v-if="infoLoading" class="data-info-skeleton" aria-label="正在加载设备数据量">
        <div class="skeleton-line skeleton-line--long" />
        <div class="skeleton-line skeleton-line--short" />
      </div>
      <div v-else-if="rowEstimate !== null" class="data-info">
        <div class="data-info-title">本设备历史数据</div>
        <div class="data-info-items">
          <span class="data-info-item">
            数据量估算：<strong>约 {{ formatRowCount(rowEstimate) }} 条</strong>
          </span>
          <span class="data-info-item">
            保留期：<strong>系统级（默认 90 天）</strong>
          </span>
        </div>
      </div>

      <!-- 数据处置 radio (方案 v3.3 §2.1) -->
      <div class="data-action-section">
        <div class="data-action-title">历史数据处理</div>
        <el-radio-group v-model="dataAction" class="data-action-group">
          <el-radio value="keep" class="data-action-radio">
            保留历史数据<span v-if="retentionSuffix" class="data-action-hint">（{{ retentionSuffix }}）</span>
          </el-radio>
          <el-radio value="delete" class="data-action-radio data-action-radio-danger">
            <span class="danger-text">同时删除历史数据（后台分批清理；清理开始后不可恢复）</span>
          </el-radio>
        </el-radio-group>
        <p class="data-action-note">
          只删设备行是<strong>软删</strong>：保留数据的删除可在「恢复已删除设备」中找回。
          勾选删除数据后，后台清理开始前仍可恢复并撤回删除请求；清理开始即不可恢复。
        </p>
      </div>
    </template>

    <template #footer>
      <el-button @click="handleCancel">取消</el-button>
      <el-button type="danger" :loading="submitting" @click="handleConfirm">确认删除</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { UNKNOWN } from '@/utils/format'

const props = withDefaults(defineProps<{
  visible: boolean
  device: EdgeDevice | null
  submitting?: boolean
}>(), {
  submitting: false,
})

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  // deleteData: 是否同时删除历史数据 (方案 §2.3 delete_data 参数)
  (e: 'confirm', deleteData: boolean): void
}>()

const dialogVisible = computed({
  get: () => props.visible,
  set: (value: boolean) => emit('update:visible', value),
})

// 「通道」= 设备所挂的**总线标识**（如 UART0）。
//
// 为什么不能再用 hardware_type + hardware_id 拼：在**边缘设备**上这两个字段的语义是
//   · hardware_type —— 设备上为空/无意义；
//   · hardware_id   —— **Modbus 从站地址**（雨量计为 "1"），不是总线名。
// 拼出来就是 "UART 1" 这种误导值（2026-09-17 生产实测：真实挂在 UART0 的雨量计显示 "UART 1"）。
//
// 真正的总线名在关联通道上：device.channel_hardware_id（= 后端 channel.hardware_id，"UART0"/"UART1"）。
// 取不到（老数据 / 紧凑列表没有 channel）时回退为 UNKNOWN（'—'），
// **绝不**退回 hardware_type + hardware_id 拼接 —— 那等于把设备地址当总线名展示。
const channelLabel = computed(() => {
  if (!props.device) return UNKNOWN
  const busId = props.device.channel_hardware_id || props.device.channel?.hardware_id
  return busId || UNKNOWN
})

// 数据处置：默认保留历史数据
const dataAction = ref<'keep' | 'delete'>('keep')

// 数据量估算异步加载 (失败降级为不显示该区域，不阻塞删除)
//
// 口径（2026-10-23 逻辑设备移除后）：原来的 /edge-devices/:id/logical-device-info
// 已随逻辑身份层一起删除。现在用 data-summary 以「本设备自己作为唯一来源」
// 查询 —— 该端点返回的是 unified_data/device_data 中 WHERE device_id=该设备
// 的行数估算，正是删除确认要告诉用户的东西。
//
// null 的语义是「估算不出来」（请求失败/超时），**不是 0 行**：为 null 时整块
// 信息区不渲染，而不是显示「约 0 条」这种会误导用户的假事实。
const infoLoading = ref(false)
const rowEstimate = ref<number | null>(null)
let infoRequestSeq = 0

// 保留期后缀：系统级保留期统一生效（契约 §4.5，不再有 per-device retention），
// 因此这里不再是「该设备保留 N 天」，而是一个与设备无关的系统事实。
const retentionSuffix = computed(() => '受系统级保留期约束（默认 90 天）')

watch(() => props.visible, (open) => {
  if (open && props.device) {
    dataAction.value = 'keep'
    rowEstimate.value = null
    const seq = ++infoRequestSeq
    const deviceId = props.device.id
    infoLoading.value = true
    edgeDeviceApi.getDataSummary(deviceId, [deviceId])
      .then((summary) => {
        if (seq !== infoRequestSeq) return
        // 2026-10-25 新契约：目标自身行数在 target_rows（兼容旧字段 target.rows）。
        const rows = typeof summary.target_rows === 'number'
          ? summary.target_rows
          : (typeof summary.target?.rows === 'number' ? summary.target.rows : null)
        rowEstimate.value = rows
      })
      .catch(() => {
        // 降级：请求失败时不显示信息区，删除流程不受阻
        if (seq !== infoRequestSeq) return
        rowEstimate.value = null
      })
      .finally(() => {
        if (seq === infoRequestSeq) infoLoading.value = false
      })
  } else {
    // 关闭时使在途请求失效，防止陈旧响应污染下次打开
    infoRequestSeq++
    infoLoading.value = false
  }
}, { immediate: true })

const formatRowCount = (count: number): string => count.toLocaleString('zh-CN')

const handleCancel = () => {
  dialogVisible.value = false
}

const handleConfirm = () => {
  emit('confirm', dataAction.value === 'delete')
}
</script>

<style scoped>
.delete-device-facts {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  border-radius: 8px;
  background: var(--el-fill-color-light, #f5f7fa);
}

.fact-row {
  display: flex;
  align-items: baseline;
  gap: 12px;
  font-size: 13px;
}

.fact-label {
  flex-shrink: 0;
  width: 64px;
  color: var(--el-text-color-secondary, #909399);
}

.fact-value {
  color: var(--el-text-color-primary, #303133);
  word-break: break-all;
}

.data-info-skeleton {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 12px;
  padding: 12px;
  border-radius: 8px;
  border: 1px dashed var(--el-border-color, #dcdfe6);
}

.skeleton-line {
  height: 12px;
  border-radius: 6px;
  /* I-4：改用主题 token（--border-color-light #ebeef5 / --bg-color-page #f5f7fa 同值），
     原为写死 hex ⇒ 暗色下骨架条仍是亮色，与所在对话框底色几乎同色（看不清）。 */
  background: linear-gradient(
    90deg,
    var(--border-color-light, #ebeef5) 25%,
    var(--bg-color-page, #f5f7fa) 50%,
    var(--border-color-light, #ebeef5) 75%
  );
  background-size: 200% 100%;
  animation: skeleton-pulse 1.2s ease-in-out infinite;
}

.skeleton-line--long {
  width: 80%;
}

.skeleton-line--short {
  width: 50%;
}

@keyframes skeleton-pulse {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}

.data-info {
  margin-top: 12px;
  padding: 12px;
  border-radius: 8px;
  border: 1px solid var(--el-border-color-lighter, #ebeef5);
}

.data-info-title {
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary, #303133);
}

.data-info-items {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
  font-size: 13px;
  color: var(--el-text-color-regular, #606266);
}

.data-info-item strong {
  color: var(--el-text-color-primary, #303133);
}

.data-action-section {
  margin-top: 16px;
}

.data-action-title {
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary, #303133);
}

.data-action-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.data-action-radio {
  height: auto;
}

.data-action-hint {
  color: var(--el-text-color-secondary, #909399);
}

.danger-text {
  color: var(--el-color-danger, #f56c6c);
}

.data-action-note {
  margin: 10px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-text-color-secondary);
}

/* 危险 radio 选中时圆圈变红 */
.data-action-radio-danger :deep(.el-radio__input.is-checked .el-radio__inner) {
  border-color: var(--el-color-danger);
  background: var(--el-color-danger);
}

.data-action-radio-danger :deep(.el-radio__input.is-checked + .el-radio__label) {
  color: var(--el-color-danger);
}
</style>
