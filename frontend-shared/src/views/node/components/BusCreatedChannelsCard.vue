<template>
        <section class="card bus-channels-card" data-created-channels>
          <div class="bus-channels-head">
            <b>已创建通道</b>
            <span class="bus-tag" :class="channels.length ? 'bus-tag-green' : 'bus-tag-gray'" data-created-channels-count>{{ channelsLoading ? '加载中…' : channels.length + ' 条' }}</span>
            <span class="bus-channels-hint">通道建立在硬件资源之上，是采集实例（与上面的「资源」不是同一层级）</span>
            <button class="link-btn" type="button" data-created-channels-all @click="navigateToNodeChannels"><el-icon :size="12"><View /></el-icon>在通道管理中查看</button>
          </div>
          <div v-if="channelsLoading && channels.length === 0" class="bus-channels-empty">正在读取通道列表…</div>
          <div v-else-if="channels.length === 0" class="bus-channels-empty" data-created-channels-empty>该节点暂无已创建通道 —— 在上方资源表选一行后点「新建通道」即可创建</div>
          <div v-else class="bus-channels-list">
            <div v-for="ch in channels" :key="ch.id" class="bus-channel-item" :data-channel-id="ch.id">
              <span class="bus-channel-name">{{ channelName(ch) }}</span>
              <el-tag size="small" effect="plain">{{ channelTypeLabel(ch) }}</el-tag>
              <span class="bus-channel-hw mono">{{ channelHardwareKey(ch) }}</span>
              <span class="channel-state-tag" :class="channelEnabled(ch) ? 'state-on' : 'state-off'">{{ channelEnabled(ch) ? '已启用' : '已禁用' }}</span>
              <span class="bus-channel-res">资源 {{ channelResourceLabel(ch) }}</span>
              <span class="bus-channel-actions">
                <!-- 内联编辑态：就在这一行里改，Enter 提交 / Esc 取消，不再弹窗遮页 -->
                <template v-if="baudInlineId === String(ch.id)">
                  <el-input-number
                    v-model="baudInlineValue"
                    size="small"
                    controls-position="right"
                    :min="1200"
                    :max="baudMax"
                    :step="1"
                    :disabled="baudInlineSubmitting"
                    class="baud-inline-input"
                    data-baud-inline-input
                    @keyup.enter="submitBaudInline(ch)"
                    @keyup.esc="cancelBaudInline"
                  />
                  <button class="link-btn" type="button" data-baud-inline-save :disabled="baudInlineSubmitting" @click="submitBaudInline(ch)">{{ baudInlineSubmitting ? '下发中…' : '保存' }}</button>
                  <button class="link-btn" type="button" data-baud-inline-cancel :disabled="baudInlineSubmitting" @click="cancelBaudInline">取消</button>
                  <span v-if="baudInlineError" class="baud-inline-error" data-baud-inline-error>{{ baudInlineError }}</span>
                </template>
                <template v-else>
                  <button class="link-btn" type="button" :data-edit-channel="ch.id" :disabled="nodeOffline" @click="editChannel(ch)"><el-icon :size="12"><EditPen /></el-icon>配置</button>
                  <button v-if="isUARTChannel(ch)" class="link-btn" type="button" :data-baud-channel="ch.id" :disabled="nodeOffline" @click="openBaudInline(ch)"><el-icon :size="12"><Tools /></el-icon>改波特率</button>
                </template>
              </span>
            </div>
          </div>
        </section>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { EditPen, Tools, View } from '@element-plus/icons-vue'
import type { Capabilities } from '@/api/node'
import { channelApi, type Channel } from '@/api/channel'
import { validateBaudrate } from '@/utils/validate'
import {
  baudCurrentRate,
  channelEnabled,
  channelHardwareKey,
  channelName,
  channelTypeLabel,
  isUARTChannel,
} from '../composables/channelDisplay'
import { type BusType, channelResourceLabel as resourceLabelOf } from '../composables/busResourceHelpers'

/**
 * 「已创建通道」卡：资源是设备上报的物理能力，通道是用户基于某条资源建的采集实例，
 * 两者是不同层级，所以这张卡与上面的资源表并列渲染而不是合并 —— 合并会再次把层级混为一谈。
 *
 * 为什么行内改波特率留在这里：它是**单条通道**的动作，编辑态只对当前行有意义；
 * 放进页面会让页面多出 4 个只服务于这一张卡的状态。批量改速率（按资源）在页面上的弹窗里，
 * 两者措辞刻意不同：单条说"已下发波特率 N"，批量才说"共 N 条"。
 */
const props = defineProps<{
  channels: Channel[]
  channelsLoading: boolean
  nodeOffline: boolean
  nodeSerial: string
  capabilities: Capabilities
  activeBusType: BusType
  /** 能力驱动上限：与页面批量弹窗共用同一口径（无能力数据时 921600）。 */
  baudMax: number
  /** 提交成功后必须刷新通道列表，否则卡片还显示旧波特率。 */
  refreshChannels: () => Promise<void>
}>()

const emit = defineEmits<{
  'edit-channel': [channel: Channel]
  'navigate-channels': []
}>()

function channelResourceLabel(ch: Channel): string {
  return resourceLabelOf(props.activeBusType, props.capabilities, ch)
}
function navigateToNodeChannels() { emit('navigate-channels') }
function editChannel(channel: Channel) { emit('edit-channel', channel) }

/** 内联编辑态：正在编辑的通道 id（'' = 未编辑）。就在这一行里改，Enter 提交 / Esc 取消。 */
const baudInlineId = ref<string>('')
const baudInlineValue = ref<number>(9600)
const baudInlineSubmitting = ref(false)
const baudInlineError = ref('')

/** 打开某一行的内联编辑；复用与批量弹窗完全相同的"当前值"读取口径。 */
function openBaudInline(ch: Channel) {
  if (props.nodeOffline) {
    ElMessage.warning('节点离线，无法修改波特率')
    return
  }
  if (!isUARTChannel(ch)) {
    ElMessage.warning('仅 UART 通道支持修改波特率')
    return
  }
  baudInlineId.value = String(ch.id)
  const current = baudCurrentRate(ch)
  baudInlineValue.value = current ?? 9600
  baudInlineError.value = ''
}

function cancelBaudInline() {
  baudInlineId.value = ''
  baudInlineError.value = ''
  baudInlineSubmitting.value = false
}

/**
 * 提交单条通道的波特率。成功/未改动/失败都**逐条如实**反馈，
 * 不复用批量提交的措辞（那会说"共 N 条"，在单条场景下是误导）。
 */
async function submitBaudInline(ch: Channel) {
  if (baudInlineSubmitting.value) return
  const baud = Number(baudInlineValue.value)
  if (!validateBaudrate(baud)) {
    baudInlineError.value = '请选择标准波特率'
    return
  }
  baudInlineSubmitting.value = true
  baudInlineError.value = ''
  const serial = props.nodeSerial
  try {
    const res = await channelApi.reconfigure(Number(ch.id), baud)
    if (serial !== props.nodeSerial) return
    await props.refreshChannels()
    if (res?.status === 'unchanged') ElMessage.info('目标波特率与当前一致，未做改动')
    else ElMessage.success(`已下发波特率 ${baud} bit/s`)
    cancelBaudInline()
  } catch (err: any) {
    if (serial !== props.nodeSerial) return
    // 就地显示错误，不关编辑态 —— 用户可以直接改一个值重试，不用重新打开。
    baudInlineError.value = err?.message || '下发失败'
  } finally {
    if (serial === props.nodeSerial) baudInlineSubmitting.value = false
  }
}
</script>

<style scoped>
/* 「已创建通道」卡的桌面规则；窄屏触控热区与间距在 nodeOverview.shared.css 的统一 768 块里。 */
/* A：已创建通道列表（与资源表并列，两个层级必须视觉可分） */
.bus-channels-card { padding: 0 16px; }
.bus-channels-head { min-height: 52px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; border-bottom: 1px solid var(--no-border-light); }
.bus-channels-head > b { color: var(--no-text); font-size: 16px; font-weight: 600; line-height: 24px; }
.bus-channels-hint { color: var(--no-text-muted); font-size: 12px; line-height: 18px; }
.bus-channels-head .link-btn { margin-left: auto; font-size: 12px; line-height: 18px; }
.bus-channels-empty { padding: 14px 0; color: var(--no-text-secondary); font-size: 13px; line-height: 20px; }
.bus-channels-list { padding: 4px 0; }
.bus-channel-item { min-height: 44px; display: flex; align-items: center; gap: 10px; border-bottom: 1px solid var(--no-border-light); font-size: 13px; line-height: 20px; }
.bus-channel-item:last-child { border-bottom: 0; }
.bus-channel-name { color: var(--no-text); font-weight: 500; }
.bus-channel-hw { color: var(--no-text-secondary); }
.bus-channel-res { color: var(--no-text-muted); font-size: 12px; }
.bus-channel-actions { margin-left: auto; }
.channel-state-tag { display: inline-flex; align-items: center; min-height: 20px; padding: 0 7px; border-radius: 4px; font-size: 12px; font-weight: 500; line-height: 18px; }
.channel-state-tag.state-on { color: var(--no-success-text); background: var(--no-success-bg); }
.channel-state-tag.state-off { color: var(--no-text-muted); background: var(--no-chip-off-bg); }
/* 通道行内联波特率编辑 */
.baud-inline-input { width: 120px; }
.baud-inline-error { color: var(--no-danger); font-size: 12px; }
</style>

<style scoped src="./nodeOverview.shared.css"></style>
