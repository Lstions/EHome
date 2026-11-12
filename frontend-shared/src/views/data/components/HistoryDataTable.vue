<template>
  <!-- data-page / data-total 是**可观测的翻页状态**：
       分页改成前端切片后，"当前在第几页"不再体现在请求参数里，
       而 el-table 的行在单测替身里不渲染插槽 —— 没有这两个属性就
       无法从外部验证"换条件后回到第 1 页"这条 MUST（§3.2.6）。
       这两个属性因此跟着表格一起搬进本组件（页面只把 currentPage / total 传下来）。 -->
  <div
    class="mobile-table-wrapper"
    data-testid="history-table"
    :data-page="page"
    :data-total="total"
  >
    <p class="mobile-table-hint">左右滑动查看完整历史数据</p>
    <el-table :data="rows" stripe>
      <el-table-column prop="created_at" label="采集时间" width="180">
        <template #default="{ row }">
          <span>{{ formatTime(row.timestamp || row.created_at || row.collected_at) }}</span>
        </template>
      </el-table-column>
      <!-- 经 parseRowData(row) 这**唯一**入口取数：
           行里的指标在 data 下（历史行由 aggregateHistoryRows 产出、实时行由
           WS payload 构造）。解析不出来返回 null ⇒ 显示「—」，
           与"值真的是 0"区分开。 -->
      <el-table-column prop="data" label="数据">
        <template #default="{ row }">
          <!-- 判据是 .values 而不是整个返回对象：后者恒为真对象，
               会让"解析不出来"也能匹配 v-if 分支（只有 formatData 兜住才没露馅）。 -->
          <span v-if="parseRowData(row).values" data-testid="row-values">{{ formatData(parseRowData(row).values) }}</span>
          <span v-else style="color: var(--el-text-color-placeholder);" data-testid="values-unknown">{{ UNKNOWN }}</span>
        </template>
      </el-table-column>
      <!--
        2026-11-11 原始数据退役，本表删除两列：

        · 「原始数据」列（原渲染 data_json.raw_hex）：device_data 整表删除，
          原始帧字节不再有 REST 数据源。帧表的 raw 列只对**关键帧**填充
          （internal/telemetry/dualwrite.go 的 appendEncoded 显式判
          FrameKind==Key），且没有对外接口 —— 不能拿来当列数据源，
          否则绝大多数行会是「—」，比删列更误导。
        · 「状态/错误码」列（原渲染 row.error_code）：这是**既有 bug** 的清理 ——
          /edge-devices/:id/data 的响应从来没有 error_code 字段
          （实测键：created_at, data_json, device_id, id, node_id, timestamp），
          该列恒为 undefined ⇒ 恒显示「正常」。删端点时顺手删掉它，
          不是本次改造引入的问题（方案 §1.5 已记录）。
      -->
    </el-table>
  </div>
</template>

<script setup lang="ts">
/**
 * 历史数据**桌面表格**（原 DataPanel.vue 模板里 <el-table> 那一段，纯搬移）。
 *
 * 为什么单独成组件：占两列的表格 + 一整套「删过的列不得回来」的注释是本页最容易被
 * 顺手改坏的一段（DataPanelRowData.spec.ts 用执行列插槽的表格替身直接钉住渲染结果）。
 * 组件边界按**渲染单元**切：它只吃行、页码与总数，不取数、不持有分页状态。
 *
 * 注意 .mobile-table-wrapper / .mobile-table-hint 必须留在这里：MobileTableWrapper
 * 门禁按**单个文件**判断"el-table 是否在包裹容器内"，包裹壳搬到页面会让本文件变红。
 */
import { UNKNOWN } from '@/utils/format'
import { parseRowData } from '../rowData'
import { formatData, formatTime } from '../rowDisplay'
import type { CollectedRow } from '../historyRows'

defineProps<{
  /** 当前页的行（一次采集一行）。 */
  rows: CollectedRow[]
  /** 当前页码与全量条数：渲染成 data-page / data-total 供外部断言翻页状态。 */
  page: number
  total: number
}>()
</script>
