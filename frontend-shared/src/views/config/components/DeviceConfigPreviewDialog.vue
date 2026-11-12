<template>
  <el-dialog v-model="visible" title="配置预览" width="640px">
    <el-descriptions v-if="config" :column="2" border>
      <el-descriptions-item label="模板名称">{{ config.name }}</el-descriptions-item>
      <el-descriptions-item label="状态">
        <el-tag :type="config.status === 'active' ? 'success' : 'info'">
          {{ config.status === 'active' ? '启用' : '禁用' }}
        </el-tag>
      </el-descriptions-item>
      <el-descriptions-item label="设备类型">{{ getDeviceTypeLabel(config.device_type) }}</el-descriptions-item>
      <el-descriptions-item label="硬件类型">{{ getHardwareLabel(config.hardware_type) }}</el-descriptions-item>
      <el-descriptions-item label="通信协议">{{ config.protocol?.toUpperCase() || UNKNOWN }}</el-descriptions-item>
      <el-descriptions-item label="创建时间">{{ formatTime(config.created_at) }}</el-descriptions-item>
      <el-descriptions-item label="描述" :span="2">{{ config.description || '无' }}</el-descriptions-item>
    </el-descriptions>

    <div v-if="config?.config" class="config-json">
      <h4>配置参数 (JSON)</h4>
      <pre>{{ JSON.stringify(config.config, null, 2) }}</pre>
    </div>
  </el-dialog>
</template>

<script setup lang="ts">
import type { DeviceConfig } from '@/api/deviceConfig'
import { UNKNOWN } from '@/utils/format'
import { getHardwareLabel } from '@/utils/hardwareTag'
import { formatTime, getDeviceTypeLabel } from '../deviceConfigDisplay'

/**
 * 配置预览对话框（只读）。
 *
 * 边界依据：可见性由页面 v-model 控制，内容只消费一条 config，不含任何
 * 页面状态与接口调用 —— 是页面上最独立的一段模板，故按
 * 「自包含模板片段 → 子组件」拆出；.config-json 的 scoped 样式随片段搬走。
 * defineModel 保留拆分前 el-dialog v-model="previewVisible" 的双向绑定语义。
 */
defineProps<{ config: DeviceConfig | null }>()

const visible = defineModel<boolean>({ required: true })
</script>

<style scoped>
/* 预览对话框 */
.config-json {
  margin-top: 20px;
}

.config-json h4 {
  margin: 0 0 10px;
  font-size: 14px;
  color: var(--el-text-color-regular);
}

.config-json pre {
  margin: 0;
  padding: 12px;
  background: var(--el-fill-color-light);
  border-radius: 6px;
  font-size: 12px;
  font-family: monospace;
  max-height: 300px;
  overflow: auto;
}
</style>
