<template>
  <div class="card-body">
    <div class="spec-list">
      <div class="spec-item">
        <span class="label">设备类型</span>
        <el-tag size="small">{{ getDeviceTypeLabel(config.device_type) }}</el-tag>
      </div>

      <div class="spec-item">
        <span class="label">通信协议</span>
        <el-tag v-if="config.protocol" :type="config.protocol === 'modbus' ? 'primary' : 'info'" size="small">
          {{ config.protocol.toUpperCase() }}
        </el-tag>
        <span v-else class="text-muted">-</span>
      </div>

      <div class="spec-item">
        <span class="label">总线类型</span>
        <el-tag :type="getHardwareTagType(config.hardware_type)" size="small">{{ getHardwareLabel(config.hardware_type) }}</el-tag>
      </div>

      <div class="spec-item" v-if="config.config">
        <span class="label">参数配置</span>
        <div class="params-preview">
          <el-tag
            v-for="(v, k) in getParamsPreview(config.config)"
            :key="k"
            size="small"
            type="info"
          >
            {{ k }}: {{ v }}
          </el-tag>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { DeviceConfig } from '@/api/deviceConfig'
import { getHardwareLabel, getHardwareTagType } from '@/utils/hardwareTag'
import { getDeviceTypeLabel, getParamsPreview } from '../deviceConfigDisplay'

/**
 * 模板卡片正文（设备类型 / 通信协议 / 总线类型 / 参数预览）。
 *
 * 边界依据：这段模板只消费单条 config，不触碰列表状态、路由与对话框事件 ——
 * 是整张卡片里唯一"纯展示"的一段，故按「自包含模板片段 → 子组件」拆出。
 * scoped CSS 随片段整体搬入：父组件的 scoped 样式打不进子组件内部 DOM，
 * 样式留在页面会变成孤儿选择器（.card-body / .spec-* / .params-preview）。
 * 卡片头的 .config-icon / .card-footer 不搬：前者受 FillContrastContract
 * 白名单按页面路径约束（白色字面值须就近中文说明），后者被 DeviceConfigList.spec
 * 直接从页面 scoped CSS 断言，二者都留在页面。
 */
defineProps<{ config: DeviceConfig }>()
</script>

<style scoped>
.card-body {
  padding: 16px 0;
  border-top: 1px solid var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-fill-color-light);
}

.spec-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.spec-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.spec-item .label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.params-preview {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 200px;
  justify-content: flex-end;
}

.text-muted {
  color: var(--el-text-color-secondary);
}
</style>
