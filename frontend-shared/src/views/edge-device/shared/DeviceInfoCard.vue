<template>
  <el-card shadow="hover">
    <template #header><span>基本信息</span></template>
    <div v-if="isMobile" class="mobile-info-list">
      <div class="mobile-info-row">
        <span class="mobile-info-label">设备名称</span>
        <span class="mobile-info-value">{{ device.name || UNKNOWN }}</span>
      </div>
      <div class="mobile-info-row">
        <span class="mobile-info-label">设备类型</span>
        <span class="mobile-info-value">{{ deviceTypeText }}</span>
      </div>
      <div class="mobile-info-row">
        <span class="mobile-info-label">健康状态</span>
        <span class="mobile-info-value"><StatusBadge :status="device.status" effect="dark" /></span>
      </div>
      <div class="mobile-info-row">
        <span class="mobile-info-label">最后数据时间</span>
        <span class="mobile-info-value">{{ formatTime(device.last_data_time) }}</span>
      </div>

      <!--
        窄屏 10 行 × 44px = 498px，占掉半屏，而其中 5 项（协议/硬件类型/硬件ID/节点固件/配置版本）
        是排障时才看的次要属性。收起进「更多属性」，一行 44px 的成本换 5 行高度；
        展开后**一项不少**（不是删除、不是省略号），并记住本次会话的展开选择。
      -->
      <button
        v-if="secondaryRows.length"
        type="button"
        class="mobile-info-row mobile-info-toggle"
        :aria-expanded="moreOpen"
        aria-controls="mobile-info-more"
        @click="moreOpen = !moreOpen"
      >
        <span class="mobile-info-label">更多属性</span>
        <span class="mobile-info-value toggle-value">
          {{ moreOpen ? '收起' : `展开 ${secondaryRows.length} 项` }}
          <el-icon :size="12" :class="{ rotated: moreOpen }"><ArrowDown /></el-icon>
        </span>
      </button>
      <template v-if="moreOpen">
        <div class="mobile-info-row">
          <span class="mobile-info-label">通信协议</span>
          <span class="mobile-info-value">{{ device.protocol?.toUpperCase() || UNKNOWN }}</span>
        </div>
        <div class="mobile-info-row">
          <span class="mobile-info-label">硬件类型</span>
          <span class="mobile-info-value">{{ device.hardware_type ? getHardwareLabel(device.hardware_type) : UNKNOWN }}</span>
        </div>
        <div class="mobile-info-row">
          <span class="mobile-info-label">硬件ID</span>
          <span class="mobile-info-value">{{ device.hardware_id || UNKNOWN }}</span>
        </div>
        <div v-if="nodeLinkId" class="mobile-info-row">
          <span class="mobile-info-label">所属节点</span>
          <span class="mobile-info-value">
            <router-link class="node-link" :to="`/node/${nodeLinkId}/overview`">{{ nodeDisplayName }}</router-link>
          </span>
        </div>
        <div v-if="device.node?.firmware_version" class="mobile-info-row">
          <span class="mobile-info-label">节点固件</span>
          <span class="mobile-info-value">{{ device.node.firmware_version }}</span>
        </div>
        <div v-if="device.config_version" class="mobile-info-row">
          <span class="mobile-info-label">配置版本</span>
          <span class="mobile-info-value">{{ device.config_version }}</span>
        </div>
      </template>
      <div v-if="device.last_error_code && device.last_error_code > 0" class="mobile-info-row">
        <span class="mobile-info-label">错误码</span>
        <span class="mobile-info-value">
          <el-tag type="danger" size="small">
            {{ device.last_error_code }} - {{ getErrorInfo(device.last_error_code).label }}
          </el-tag>
        </span>
      </div>
    </div>
    <el-descriptions v-else :column="2" border>
      <el-descriptions-item label="设备名称">{{ device.name || UNKNOWN }}</el-descriptions-item>
      <el-descriptions-item label="设备类型">{{ deviceTypeText }}</el-descriptions-item>
      <el-descriptions-item label="通信协议">{{ device.protocol ? device.protocol.toUpperCase() : UNKNOWN }}</el-descriptions-item>
      <el-descriptions-item label="硬件类型">{{ device.hardware_type ? getHardwareLabel(device.hardware_type) : UNKNOWN }}</el-descriptions-item>
      <el-descriptions-item label="硬件ID">{{ device.hardware_id || UNKNOWN }}</el-descriptions-item>
      <el-descriptions-item v-if="nodeLinkId" label="所属节点">
        <router-link class="node-link" :to="`/node/${nodeLinkId}/overview`">{{ nodeDisplayName }}</router-link>
      </el-descriptions-item>
      <el-descriptions-item v-if="device.node?.firmware_version" label="节点固件">
        {{ device.node.firmware_version }}
      </el-descriptions-item>
      <el-descriptions-item v-if="device.config_version" label="配置版本">
        {{ device.config_version }}
      </el-descriptions-item>
      <el-descriptions-item label="健康状态">
        <StatusBadge :status="device.status" effect="dark" />
      </el-descriptions-item>
      <el-descriptions-item label="最后数据时间">{{ formatTime(device.last_data_time) }}</el-descriptions-item>
      <el-descriptions-item v-if="device.last_error_code && device.last_error_code > 0" label="错误码">
        <el-tag type="danger" size="small">
          {{ device.last_error_code }} - {{ getErrorInfo(device.last_error_code).label }}
        </el-tag>
      </el-descriptions-item>
    </el-descriptions>
  </el-card>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ArrowDown } from '@element-plus/icons-vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import { getErrorInfo } from '@/utils/errorCode'
import { getDeviceTypeLabel } from '@/utils/deviceType'
// 硬件类型展示统一走共享源（2026-10-03 文案统一）：原来这里直接 toUpperCase，
// 与其它页面用私有 map/中英混排的写法不一致，同一个 bus_type 在不同页面
// 会显示成 UART / uart / 串行 三种样子。
import { getHardwareLabel } from '@/utils/hardwareTag'
import { UNKNOWN } from '@/utils/format'
import { useResponsive } from '@/composables/useResponsive'
import type { EdgeDevice } from '@/api/edgeDevice'

const props = defineProps<{ device: EdgeDevice }>()

const { isMobile } = useResponsive()

const deviceTypeText = computed(() => getDeviceTypeLabel(props.device.device_type))

/**
 * 窄屏「更多属性」展开态（只影响 <=768px 的 mobile-info-list）。
 * 默认收起：4 项关键信息（名称/类型/健康/最后时间）足够判断设备状态，
 * 其余 5~6 项是排障时才看的次要属性；收起省 5 行 ≈ 220px，展开后一项不少。
 */
const moreOpen = ref(false)
/** 会出现在「更多属性」里的项数（用于按钮文案；按真实字段计算，不写死） */
const secondaryRows = computed(() => {
  const rows: string[] = ['protocol', 'hardware_type', 'hardware_id']
  if (nodeLinkId.value) rows.push('node')
  if (props.device.node?.firmware_version) rows.push('firmware')
  if (props.device.config_version) rows.push('config_version')
  return rows
})

// 所属节点跳转：优先用 preload 的节点主键，缺省时回退物理序列号（后端 findNodeByID 双形态兼容）
const nodeLinkId = computed(() => {
  const id = props.device.node?.id ?? props.device.node_id
  return id === 0 || id === '' || id === undefined || id === null ? null : id
})
const nodeDisplayName = computed(() => props.device.node?.name || String(props.device.node_id || UNKNOWN))

function formatTime(time: string | null | undefined) {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return UNKNOWN
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return UNKNOWN
  return date.toLocaleString('zh-CN')
}
</script>

<style scoped>
.node-link {
  color: var(--el-color-primary);
  text-decoration: none;
}
.node-link:hover {
  text-decoration: underline;
}

.mobile-info-list {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  overflow: hidden;
}

/* 「更多属性」是原生 button：必须清掉 UA 样式，否则会出现灰底 + 黑边 + 按钮字体 */
.mobile-info-toggle {
  appearance: none;
  -webkit-appearance: none;
  width: 100%;
  border: 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
  background: none;
  cursor: pointer;
  text-align: left;
  font: inherit;
  padding: 0;
  color: inherit;
}
.mobile-info-toggle:active {
  background: var(--el-fill-color-light);
}
.mobile-info-toggle .toggle-value {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--el-color-primary);
}
.mobile-info-toggle .el-icon {
  transition: transform 0.2s;
}
.mobile-info-toggle .el-icon.rotated {
  transform: rotate(180deg);
}

.mobile-info-row {
  display: grid;
  /* 标签列宽由最长字段推导（最后数据时间 ≈98px 含 padding），不再硬编码固定像素列宽 */
  grid-template-columns: minmax(max-content, 106px) minmax(0, 1fr);
  align-items: center;
  min-height: 44px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.mobile-info-row:last-child {
  border-bottom: 0;
}

.mobile-info-label {
  align-self: stretch;
  display: flex;
  align-items: center;
  padding: 9px 10px;
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-regular);
  font-size: 13px;
  font-weight: 500;
  /* 标签禁折行（防「最后数据时间」拆字），超长时省略而非拆字 */
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.mobile-info-value {
  min-width: 0;
  padding: 9px 12px;
  color: var(--el-text-color-primary);
  font-size: 13px;
  line-height: 1.45;
  overflow-wrap: anywhere;
  word-break: break-word;
}
</style>
