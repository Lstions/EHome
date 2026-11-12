<template>
  <div class="detail-panels">
      <el-row :gutter="16">
        <!-- HTTP 监控 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Connection /></el-icon> HTTP 监控</span>
              </div>
            </template>
            <el-descriptions :column="isMobile ? 1 : 2" border>
              <el-descriptions-item label="请求总数">
                {{ metricText(metrics?.http?.requests_total, true) }}
              </el-descriptions-item>
              <el-descriptions-item label="处理中请求">
                {{ metric(metrics?.http?.requests_in_flight) }}
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>

        <!-- MQTT 监控 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Promotion /></el-icon> MQTT 监控</span>
              </div>
            </template>
            <el-descriptions :column="isMobile ? 1 : 2" border>
              <el-descriptions-item label="接收消息">
                {{ metricText(metrics?.mqtt?.messages_received, true) }}
              </el-descriptions-item>
              <el-descriptions-item label="发送消息">
                {{ metricText(metrics?.mqtt?.messages_sent, true) }}
              </el-descriptions-item>
              <el-descriptions-item label="连接错误">
                <span :class="{ 'error-text': (metrics?.mqtt?.connection_errors || 0) > 0 }">
                  {{ metric(metrics?.mqtt?.connection_errors, true) }}
                </span>
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>
      </el-row>

      <el-row :gutter="16" style="margin-top: 20px;">
        <!-- 设备状态 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Monitor /></el-icon> 设备状态</span>
              </div>
            </template>
            <div class="status-bars">
              <div class="status-item">
                <span class="status-label">在线</span>
                <el-progress
                  :percentage="deviceOnlinePercent"
                  :stroke-width="20"
                  :color="'var(--color-success)'"
                >
                  <span>{{ metric(metrics?.device?.online) }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress
                  :percentage="deviceOfflinePercent"
                  :stroke-width="20"
                  :color="'var(--color-danger)'"
                >
                  <span>{{ metric(metrics?.device?.offline) }}</span>
                </el-progress>
              </div>
              <!-- 等待数据：已创建但尚未收到任何数据（2026-10-03 缺陷 5）。
                   它既不是在线也不是离线。此前后端把这类设备算进离线，
                   用户新建设备后会看到"离线"告警。有值时才有意义，
                   故为 0 时整行不显示，避免给正常系统增加噪声。 -->
              <div class="status-item" v-if="(metrics?.device?.pending || 0) > 0">
                <span class="status-label">等待数据</span>
                <el-progress
                  :percentage="devicePendingPercent"
                  :stroke-width="20"
                  :color="'var(--color-warning)'"
                >
                  <span>{{ metric(metrics?.device?.pending) }}</span>
                </el-progress>
              </div>
            </div>
          </el-card>
        </el-col>

        <!-- 节点状态 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Cpu /></el-icon> 节点状态</span>
              </div>
            </template>
            <div class="status-bars">
              <div class="status-item">
                <span class="status-label">在线</span>
                <el-progress
                  :percentage="nodeOnlinePercent"
                  :stroke-width="20"
                  :color="'var(--color-success)'"
                >
                  <span>{{ metric(metrics?.node?.online) }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress
                  :percentage="nodeOfflinePercent"
                  :stroke-width="20"
                  :color="'var(--color-danger)'"
                >
                  <span>{{ metric(metrics?.node?.offline) }}</span>
                </el-progress>
              </div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <el-row :gutter="16" style="margin-top: 20px;">
        <!-- 数据采集 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><DataLine /></el-icon> 数据采集</span>
              </div>
            </template>
            <el-descriptions :column="isMobile ? 1 : 2" border>
              <!-- 这两个数**不可相减**（2026-11-07，L1「值不变不入库」无条件生效后）：
                     两者差在两处，且这两处都不是"丢失"：
                     ① **统计窗口不同** —— 已采集是 Prometheus Counter（进程启动以来，服务重启归零）；
                        已存储帧数是全表累计（受保留期约束）。故标签上分别标「进程启动以来」/「全局」，
                        这本身就是给用户的第一层提示：两个数不属于同一个窗口。
                     ② **落库过滤不同** —— L1 无条件生效后，"所有字段值都没变且未到 60s 心跳"的帧
                        不再写库。已采集仍然 +1（它数的是解析成功的事件），已存储帧数不 +1。
                   若照旧并排叫「已采集 / 已存储」，随运行时间差额越来越大，用户必然读成
                   「采了 1 万只存了 3 千 ⇒ 丢了 7 千」。实际一条都没丢：值不变的行不重复入库
                   是**压缩**，不是丢弃（回归实测：帧级跳过率 17.7%，仍保留 82% 的帧）。
                   tooltip 必须把「压缩不是丢失」说清楚 —— 这是本次改造的用户认知关键。 -->
              <el-descriptions-item>
                <template #label>
                  <el-tooltip
                    content="收到并解析成功的采样帧累计数（进程启动以来，服务重启归零）。它数的是「帧」，不受「值不变不入库」影响 —— 每一帧都计数。"
                    placement="top"
                  >
                    <span class="desc-term">已采集<span class="desc-scope">{{ SCOPE_PROCESS }}</span></span>
                  </el-tooltip>
                </template>
                {{ metricText(metrics?.data?.points_collected, true) }}
              </el-descriptions-item>
              <el-descriptions-item>
                <template #label>
                  <el-tooltip
                    content="数据库中已落库的采样帧总数（全表累计，受保留期约束）。值不变且未到 60 秒心跳的帧不重复入库，故它的增长慢于「已采集」——差额是压缩，不是丢失。"
                    placement="top"
                  >
                    <span class="desc-term">已存储帧数<span class="desc-scope">{{ SCOPE_GLOBAL }}</span></span>
                  </el-tooltip>
                </template>
                {{ metricText(metrics?.data?.points_stored, true) }}
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>

        <!-- WebSocket -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Connection /></el-icon> WebSocket</span>
              </div>
            </template>
            <el-descriptions :column="isMobile ? 1 : 2" border>
              <el-descriptions-item label="活跃连接">
                {{ metric(metrics?.websocket?.connections_active) }}
              </el-descriptions-item>
              <el-descriptions-item label="消息总数">
                {{ metricText(metrics?.websocket?.messages_total, true) }}
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>
      </el-row>
  </div>
</template>

<script setup lang="ts">
/**
 * 详细指标面板组（原 Monitor.vue「详细面板」v-else 分支的六张卡整块搬入）：
 * HTTP / MQTT / 设备状态 / 节点状态 / 数据采集 / WebSocket。
 * DOM / class / 文案 / tooltip / 进度条配色一字未改（含「已采集 / 已存储帧数」的口径说明）。
 *
 * 边界依据（为什么六卡一起搬，而不是按卡片再拆）：
 *   · 六卡共用同一套 el-row/el-col 栅格与同一组派生百分比（设备/节点在线、离线、等待数据）；
 *     逐卡拆开会把同一组分母计算复制多份，正是「不要为看起来小就把逻辑切碎」；
 *   · 4 处 var(--color-*) 进度条配色是成组出现的契约（F17），
 *     Monitor.spec 按 success/danger/success/danger 的顺序断言渲染结果，分组才可判定；
 *   · 百分比与「指标是否可信」全部由页面经 props 下传，本组件只渲染，不重算口径。
 *
 * 刻意留在页面的：三态链（失败错误块 / 首屏骨架 / 本组件）——「失败不得伪装成 0」
 * 的判据按整页 DOM 断言，三支必须彼此紧邻（v-else 链）。
 */
import { Connection, Monitor, Cpu, DataLine, Promotion } from '@element-plus/icons-vue'
import type { MetricsSummary } from '@/api/monitor'
import { metricOf, metricTextOf, SCOPE_PROCESS, SCOPE_GLOBAL } from '../monitorDisplay'

const props = defineProps<{
  /** 指标摘要；null = 尚未拉取（渲染「—」而不是 0） */
  metrics: MetricsSummary | null
  /** 指标是否可信（接口成功且未失败）：决定读数是数值还是「—」 */
  metricsReady: boolean
  /** <768px 时描述列表改单列（与 useResponsive 同断点，由页面下传） */
  isMobile: boolean
  /** 设备三态占比：分母含 pending（与页面 deviceTotal 同源），条内数值由 metrics 渲染 */
  deviceOnlinePercent: number
  deviceOfflinePercent: number
  devicePendingPercent: number
  /** 节点两态占比 */
  nodeOnlinePercent: number
  nodeOfflinePercent: number
}>()

/**
 * 进度条配色（F17）：直接传 var(--color-*) 字符串。
 *
 * el-progress 的 :color 最终落到 barStyle.backgroundColor（EP progress 源码里
 * 非渐变分支即 backgroundColor），而这是**由浏览器解析**的声明值 ——
 * var() 在此处照常生效，无需 JS 解析，也无需 MutationObserver 跟随主题。
 *
 * 此前是「JS 读取主题 token + MutationObserver 跟随切主题」：结果虽正确，
 * 但按规范 §3.6.2「色值取自 token 层」应以 CSS 变量为首选；
 * JS 解析只在真正取不到 CSS 的场景（ECharts 等 Canvas 渲染）才保留。
 * （源码守卫测试断言本页源码不再出现该工具函数名，故此处不写其标识符。）
 */

/** 取值口径：与页面、控制面健康卡共用 monitorDisplay.metricOf（未就绪一律「—」）。 */
const metric = (value: number | null | undefined, isCounter = false) =>
  metricOf(value, props.metricsReady, isCounter)
const metricText = (value: number | null | undefined, isCounter = false) =>
  metricTextOf(value, props.metricsReady, isCounter)
</script>

<style scoped>
/* 详细面板样式：原先写在 Monitor.vue 的 scoped 样式里，随片段一起搬。
   .card-header 被「控制面健康」共用，收在 monitor.shared.css；窄屏 .status-label 规则
   随它所属的状态条一起搬进本文件的 @media (max-width: 480px) 块。 */
/* 描述列表里的术语（悬停出 tooltip）：与 .control-metric .term 同一交互范式，
   但不能复用那个类 —— 它被限定在 .control-metric 作用域内。 */
.desc-term {
  cursor: help;
  text-decoration: underline dotted var(--el-text-color-placeholder);
  text-underline-offset: 3px;
}

/* 描述列表里的范围词：与统计卡 .stat-scope 同一视觉（浅底 chip），
   但字号更小一档 —— 描述列表的标签栏本身已是次级字号。 */
.desc-scope {
  display: inline-block;
  margin-left: 6px;
  padding: 0 5px;
  border-radius: 8px;
  font-size: 10px;
  line-height: 15px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
  vertical-align: 1px;
}

.status-bars {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.status-item {
  display: flex;
  align-items: center;
  gap: 12px;
}

.status-label {
  width: 40px;
  font-size: 14px;
  color: var(--el-text-color-regular);
}

.status-item .el-progress {
  flex: 1;
}

.error-text {
  color: var(--el-color-danger);
  font-weight: bold;
}

@media (max-width: 480px) {
  .status-label {
    width: 36px;
    font-size: 13px;
  }
}
</style>

<style scoped src="./monitor.shared.css"></style>
