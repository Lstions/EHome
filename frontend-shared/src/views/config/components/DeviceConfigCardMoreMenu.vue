<template>
  <el-dropdown-menu>
    <el-dropdown-item command="setDefault" :disabled="config.is_default">
      设为默认
    </el-dropdown-item>
    <el-dropdown-item command="toggle">
      {{ config.status === 'active' ? '禁用' : '启用' }}
    </el-dropdown-item>
    <el-dropdown-item command="export">导出配置</el-dropdown-item>
    <el-dropdown-item command="delete" divided>
      <span style="color: var(--el-color-danger);">删除</span>
    </el-dropdown-item>
  </el-dropdown-menu>
</template>

<script setup lang="ts">
import type { DeviceConfig } from '@/api/deviceConfig'

/**
 * 卡片「更多」下拉的菜单项（只渲染，不决策）。
 *
 * 边界依据：菜单项只表达"有哪些动作、哪一项禁用"，动作名仍由 el-dropdown 的
 * command 机制冒泡给页面的 @command —— 分发与二次确认（handleMoreAction、
 * deviceConfigApi.delete + feedback.confirmDanger）必须留在页面，这是
 * listPageConventions.spec.ts「危险操作必须二次确认」的扫描对象。
 * 本片段不持有状态、不调接口，也没有 scoped 样式。
 */
defineProps<{ config: DeviceConfig }>()
</script>
