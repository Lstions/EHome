<template>
  <!--
    BMS 设备详情页（2026-10-17 由评审 demo 转正）。

    · 数据全部来自真实后端：设备详情 / 实时流 / 受控操作目录 / 操作历史 / 轮询指令配置。
    · 路由：edge-device/:id 经 EdgeDeviceDetailRouter 按 device_type 分派到本页。
    · 滚动由 MainLayout 的 .el-main.main-content 负责，本页不自建滚动容器。
    · 四条用户诉求（保护明细默认展开 / 流式布局 / 实时流不抢滚动 / 轮询上限）的落点见各区段注释。
  -->
  <div class="bms-detail-page">
    <!--
      页头：统一共享组件 DeviceHeader（2026-10-21 接入，用户诉求「边缘设备顶部
      实时连接/刷新数据那里统一一个组件」）。BMS 不再自建 <header class="page-head">：
      返回按钮 / 名称 / 状态标签 / 面包屑 / 操作组全部由共享组件渲染，与逆变器页、
      通用设备页同款 —— 窄屏确定性两行（行1 返回+名称+⋯；行2 刷新独占整行），
      桌面单行完整操作组；同时补上「实时连接」状态标签（WS 已连接时显示）。

      · builtin-dialogs=false：编辑/删除继续用本页自己的弹窗
        （BMS 删除要选择历史数据处理方式，共享内置对话框没有这段）；
      · #subtitle 插槽：保留本页「节点/通道/设备号/固件/轮询数」的窄屏紧凑版与桌面长文案；
      · 事件直连本页既有 handler（goBackToList / onSyncToHA / refreshAll /
        onEditDevice / onDeleteDevice），行为与改前一致。
    -->
    <DeviceHeader
      :device="device"
      :ws-connected="wsConnected"
      :syncing-h-a="syncingHA"
      :refreshing="refreshing"
      :title="deviceName"
      :tags="headerTags"
      :builtin-dialogs="false"
      @back="goBackToList"
      @sync-to-h-a="onSyncToHA"
      @refresh="refreshAll"
      @edit="onEditDevice"
      @delete="onDeleteDevice"
    >
      <template #subtitle>
        <!-- 窄屏紧凑版：与桌面版信息等价（节点序列号/总线/设备号/固件/轮询启用数），
             用 # 与 · 分隔；每个 span 是不可断的「词」，放不下整词换行。 -->
        <span v-if="isMobileViewport" class="bms-sub">
          <span>{{ nodeLabel }}</span>
          <span>·</span><span>{{ channelLabel }}</span>
          <span>·</span><span>#{{ deviceId }}</span>
          <span>·</span><span>FW {{ firmwareLabel }}</span>
          <span>·</span><span>轮询 <b>{{ enabledCount }}<template v-if="hasPollLimit">/{{ pollLimitValue }}</template></b></span>
        </span>
        <template v-else>
          节点 {{ nodeLabel }} · 通道 {{ channelLabel }} · 设备 #{{ deviceId }} · 节点固件 {{ firmwareLabel }}
          <template v-if="hasPollLimit"> · 轮询已启用 <b>{{ enabledCount }}/{{ pollLimitValue }}</b> 条</template>
          <template v-else> · 轮询已启用 <b>{{ enabledCount }}</b> 条</template>
        </template>
      </template>
    </DeviceHeader>

    <main class="page-body">
      <!-- 设备详情加载失败：常驻页面，不随 toast 消失 -->
      <el-alert v-if="detailError" type="error" :closable="false" show-icon class="inline-alert page-error">
        <template #title>设备详情加载失败（下方数据可能不完整）</template>
        <div class="alert-detail">{{ detailError }}</div>
      </el-alert>

      <!-- 实时数据四态（2026-10-21 修复「刚进页面把加载中显示成空数据」）：
           loading → 卡片骨架；error → 本错误条 + 重试（下方数字「—」= 未知，不是设备值）；
           data → 真实数据；empty → 明确「设备未上报」。错误与空态文案彻底分开。 -->
      <el-alert v-if="realtimeError" type="error" :closable="false" show-icon class="inline-alert realtime-alert">
        <template #title>实时数据加载失败（下方数字显示「—」表示未知，不是设备当前值）</template>
        <div class="alert-detail">
          {{ snapshotErrorText }}
          <el-button size="small" text type="primary" @click="retryRealtime">重试</el-button>
        </div>
      </el-alert>
      <el-alert v-else-if="realtimeEmpty" type="info" :closable="false" show-icon class="inline-alert realtime-alert">
        <template #title>设备暂未上报实时数据</template>
        <div class="alert-detail">
          下方数字显示「—」表示未知（不是 0）。设备按轮询周期上报后会自动出现；也可以现在重试一次。
          <el-button size="small" text type="primary" @click="retryRealtime">重试</el-button>
        </div>
      </el-alert>

      <!-- 指标卡（BmsMetricsRow）：数值派生在 useBmsRealtime，四张卡统一受 realtimeLoading 驱动 -->
      <BmsMetricsRow
        :soc="soc"
        :soc-progress="socProgress"
        :remaining-capacity="remainingCapacity"
        :total-voltage="totalVoltage"
        :current-display="currentDisplay"
        :capacity-sub-text="capacitySubText"
        :voltage-sub-text="voltageSubText"
        :current-hint="currentHint"
        :realtime-loading="realtimeLoading"
      />

      <!-- 诉求①：MOS 直控 + 诉求④：保护状态（BmsStatusCards）——
           fet_status / protection_status 的位语义与「未知不等于关断 / 不等于正常」的文案随卡片一起搬走；
           四态（加载 / 失败 / 空 / 数据）仍由页面 useBmsRealtime 判定后下发，卡片只负责渲染。 -->
      <BmsStatusCards
        :realtime-loading="realtimeLoading"
        :realtime-error="realtimeError"
        :snapshot-error-text="snapshotErrorText"
        :retry-realtime="retryRealtime"
        :request-mos-change="requestMosChange"
        :mos-phase="mosPhase"
        :mos-error="mosError"
        :has-fet-status="hasFetStatus"
        :fet-hex="fetHex"
        :charge-on="chargeOn"
        :discharge-on="dischargeOn"
        :can-control-mos="canControlMos"
        :device-online="deviceOnline"
        :status-text="statusTag.text"
        :latest-data="latestData"
      />

      <!-- 保护触发历史（新增）：计数口径是 protection_status 的 0→1 跃变次数，不是数值求和
           （实测非零值恒为 1 —— 它是状态位序列，求和会得到「1239 次」这种错数）。
           窄屏默认折叠，摘要由卡片自己 emit（「近 30 天 6 次」/「近 30 天无触发」）。 -->
      <section class="row diag-row">
        <CollapseBlock
          block-id="protection-history"
          title="保护触发历史"
          :summary="diagSummary.protection"
          :collapsible="isMobileViewport"
          :lazy="true"
          :open="collapse.isOpen('protection-history')"
          @toggle="collapse.toggle('protection-history')"
        >
          <div class="card diag-card">
            <div class="card-head">
              <span class="card-title">保护触发历史</span>
              <span class="card-hint">按天统计 · 0→1 跃变口径 · 可切 7/30/90 天</span>
            </div>
            <ProtectionHistoryCard :device-id="deviceId" :online="deviceOnline" @summary="onProtectionSummary" />
          </div>
        </CollapseBlock>
      </section>

      <!-- 电芯电压分布（BmsCellVoltageCard）：图表卡是固定高度的"异类"，独占一行 ——
           与内容驱动卡片同行时，矮卡下方必然留出页面底色（padding 无法吸收固定高度差）。 -->
      <BmsCellVoltageCard
        :cells="cells"
        :max-cell="maxCell"
        :min-cell="minCell"
        :diff="diff"
        :cell-voltages="cellVoltages"
        :realtime-loading="realtimeLoading"
        :realtime-error="realtimeError"
        :is-mobile-viewport="isMobileViewport"
      />

      <!-- 温度探头（BmsTemperatureCard）：内容驱动卡，与 MOS/保护等卡同属流式（auto-fill 列数） -->
      <BmsTemperatureCard
        :temps="temps"
        :realtime-loading="realtimeLoading"
        :realtime-error="realtimeError"
      />

      <!--
        页级时间窗（契约 §D，2026-11-12）：全页唯一实例，放在**图表区之前**而不是页面顶部。

        为什么在这里（2026-11-12 用户反馈「时间选择栏应该在图表上方，而不是顶部」）：
        它驱动的全是趋势图，放在页头与指标卡之间时既离图表很远、又看不出管哪几张；
        贴在图表区入口处，读者一眼就知道「下面这几张图共用这个窗口」。
        移动端仍吸顶（继续往下看 SOC / 历史趋势时选择器一直在）。

        · 通过 props 下发 :time-range / :custom-range（温度趋势 / SOC 与电流 / 历史趋势三处），
          卡内已删除各自的 TimeRangeSelector 与 useTimeRange。
        · **保护触发历史不跟随**：它的口径是 30 天 0→1 跃变次数（BmsDiagCharts.spec.ts:152-155
          锁 rangeDays=30），控件留在卡内。
        · 复用 @/components/charts/TimeRangeSelector.vue（图表卡工具条唯一档：桌面 32 / 窄屏 36）。
      -->
      <div class="page-range-bar">
        <span class="page-range-label">时间窗</span>
        <TimeRangeSelector
          v-model="pageTimeRange"
          v-model:custom-range="pageCustomRange"
          role="group"
          aria-label="趋势图时间窗（温度趋势 / SOC 与电流 / 历史趋势）"
        />
        <span class="page-range-hint" :title="pageRangeWindowText">温度 · SOC · 历史趋势共用</span>
      </div>
      <!-- 温度趋势（新增）：温度探头只给瞬时值，看不出「3 路一起升 = 环境升温 / 单路升 = 该测点异常」，
           所以要看时间维。窄屏默认折叠，摘要「当前 xx℃」由卡片 emit。 -->
      <section class="row diag-row">
        <CollapseBlock
          block-id="temp-trend"
          title="温度趋势"
          :summary="diagSummary.temp"
          :collapsible="isMobileViewport"
          :lazy="true"
          :open="collapse.isOpen('temp-trend')"
          @toggle="collapse.toggle('temp-trend')"
        >
          <div class="card diag-card">
            <div class="card-head">
              <span class="card-title">温度趋势</span>
              <span class="card-hint">3 路探头 · 45℃ 告警线</span>
            </div>
            <!-- 时间窗由父页下发（契约 §D）：卡内不再有 TimeRangeSelector -->
            <TempTrendCard
              :device-id="deviceId"
              :online="deviceOnline"
              :time-range="pageTimeRange"
              :custom-range="pageCustomRange"
              @summary="onTempSummary"
            />
          </div>
        </CollapseBlock>
      </section>

      <!-- SOC 与电流（新增）：SOC 单独看分不清「没充电」还是「充不进」，要叠加电流看电是进是出。 -->
      <section class="row diag-row">
        <CollapseBlock
          block-id="soc-current"
          title="SOC 与电流"
          :summary="diagSummary.soc"
          :collapsible="isMobileViewport"
          :lazy="true"
          :open="collapse.isOpen('soc-current')"
          @toggle="collapse.toggle('soc-current')"
        >
          <div class="card diag-card">
            <div class="card-head">
              <span class="card-title">SOC 与电流</span>
              <span class="card-hint">双线 · 量级分轴</span>
            </div>
            <!-- 同上：时间窗走页级，卡内 watch props 自行重取 -->
            <SocCurrentCard
              :device-id="deviceId"
              :online="deviceOnline"
              :time-range="pageTimeRange"
              :custom-range="pageCustomRange"
              @summary="onSocSummary"
            />
          </div>
        </CollapseBlock>
      </section>

      <!-- 图表：业务逻辑参考旧 BMS 页面（BmsCellVoltageHistoryChart / HistoryChartSection）重建，
           复用 LineChart / TimeRangeSelector / useHistoryData / useTimeRange / computeAdaptiveYAxisRange。
           容器高度固定，避免内容变化撑变页面高度而破坏 overflow-anchor:none 防跳动。 -->
      <!-- 移动端默认折叠：电芯历史 + 运行趋势两张图合计有 620px（压缩后），
           折叠态只留标题行 + 一行关键结论（压差 / 总电压），展开状态记忆在 bms-collapse:<deviceId>。
           桌面 collapsible=false → CollapseBlock 直接返回插槽（零包装节点），布局与改前逐像素一致。 -->
      <CollapseBlock
        block-id="charts"
        title="历史趋势"
        :summary="chartsCollapseSummary"
        :collapsible="isMobileViewport"
        :lazy="true"
        :open="collapse.isOpen('charts')"
        @toggle="collapse.toggle('charts')"
      >
        <!-- 时间窗与温度/SOC 共用页面级那一个：传了 props 后组件内不再渲染自己的选择器 -->
        <BmsChartsPanel
          :device-id="deviceId"
          :cell-count="16"
          :online="deviceOnline"
          :time-range="pageTimeRange"
          :custom-range="pageCustomRange"
        />
      </CollapseBlock>

      <!--
        诉求②：实时数据流不抢滚动 + 诉求③：轮询指令上限

        2026-10-22 第七轮（用户诉求③「指令频率应该抽一个公共组件出来，大家一起通用」）：
        改前本页自建了一整套轮询配置卡（.poll-row + 自己的请求/保存/容量门，约 500 行），
        与逆变器/通用设备页用的 CommandFrequencySection 是**两份实现** —— 超限点名、保存后
        确认节点已应用这些行为各只在一半里，UI 也越长越不像，窄容器下两套还各自出现
        「名称被输入框覆盖」的同类缺陷。现在三页统一用共享组件；本页只保留：
          · 页头摘要需要的启用条数（组件 @usage-change 外抛，不再重复拉同一接口）；
          · 页头「刷新数据」连带刷新指令配置（递增 reload-token，组件 watch 到就重拉）。
      -->
      <section class="row stream-poll">
        <CommandFrequencySection
          :device-id="deviceId"
          :node-id="deviceNodeId"
          :max-polling-commands="device?.max_polling_commands"
          title="轮询指令配置"
          note="每条指令可单独设间隔，0 = 禁用。"
          :reload-token="commandReloadToken"
          @usage-change="onUsageChange"
        />
      </section>

      <!-- 受控操作（目录来自后端，按用途分组） + 操作历史（BmsOpsHistorySection）——
           目录分组 / 执行状态机 / 参数表单 / 历史条目都在 useBmsOperations，本页只传展示数据与两个回调。 -->
      <BmsOpsHistorySection
        :total-ops="totalOps"
        :op-groups="opGroups"
        :open-groups="openGroups"
        :toggle-group="toggleGroup"
        :risk-count="riskCount"
        :op-state="opState"
        :op-busy="opBusy"
        :on-op-click="onOpClick"
        :catalog-error="catalogError"
        :catalog-loading="catalogLoading"
        :load-operations="loadOperations"
        :history-rows="historyRows"
        :history-collapse-summary="historyCollapseSummary"
        :collapse="collapse"
        :is-mobile-viewport="isMobileViewport"
      />
    </main>

    <!-- 近期身份验证（后端 10 分钟窗口，缺失即 403 recent_auth_required） -->
    <el-dialog v-model="reauthVisible" title="验证当前身份" width="420px" :close-on-click-modal="false" append-to-body>
      <p class="dialog-note">该操作属于高风险动作，需要重新验证当前账户密码；验证通过后会自动继续执行。</p>
      <el-input v-model="reauthPassword" type="password" show-password placeholder="请输入当前账户密码" aria-label="当前账户密码" />
      <template #footer>
        <el-button @click="cancelReauth">取消</el-button>
        <el-button type="primary" :loading="reauthBusy" :disabled="!reauthPassword" @click="doReauth">验证并继续</el-button>
      </template>
    </el-dialog>

    <!-- 参数化操作表单：字段由后端 definition.input_schema 生成，与生产页同一组件。
         参数与标定组（30/33 参数的内阻与保护参数表）靠它的双列布局 + 数字感知排序排版。 -->
    <ActionForm
      v-model:visible="opFormVisible"
      :definition="opFormTarget?.definition ?? null"
      :can-read="opFormCanRead"
      :reading="opFormReading"
      :prefill="opFormPrefill"
      @read="readOpFormValues"
      @submit="submitOpForm"
    />

    <!-- 编辑设备名（旧版由 DeviceHeader 内联对话框提供，转正后补回） -->
    <el-dialog v-model="editVisible" title="编辑设备" width="420px">
      <el-form label-width="72px" @submit.prevent>
        <el-form-item label="设备名称">
          <el-input v-model="editName" maxlength="64" show-word-limit placeholder="请输入设备名称" @keyup.enter="submitEditDevice" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="editSaving" @click="submitEditDevice">保存</el-button>
      </template>
    </el-dialog>

    <!-- 删除设备（复用共享对话框，与列表页同一交互：可选是否连带删除历史数据） -->
    <DeviceDeleteDialog
      v-model:visible="deleteVisible"
      :device="device"
      :submitting="deleting"
      @confirm="submitDeleteDevice"
    />
  </div>
</template>
<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted, onBeforeUnmount } from 'vue'
import { useRoute, useRouter } from 'vue-router'
// 页级时间窗（2026-11-12）：复用它而不是另写一套分段控件（TimeRangeSelectorUnified.spec 会红）
import TimeRangeSelector from '@/components/charts/TimeRangeSelector.vue'
import { resolveTimeRange, useTimeRange } from '@/composables/useTimeRange'
// 统一设备页头（逆变器 / BMS / 通用设备三页共用）：身份、标签、面包屑、操作组都由它渲染
import DeviceHeader from '@/views/edge-device/shared/DeviceHeader.vue'
// 参数化操作表单：按后端 input_schema 生成字段（boolean/enum/integer/string），
// 复用生产页同一组件，避免"demo 自己做一套表单"再次造成风格与校验漂移。
import ActionForm from '@/views/edge-device/shared/ActionForm.vue'
// 指令频率/轮询指令配置：全站唯一实现（BMS / 逆变器 / 通用设备三页共用）
import CommandFrequencySection from '@/views/edge-device/shared/CommandFrequencySection.vue'
import BmsChartsPanel from './BmsChartsPanel.vue'
import CollapseBlock from './CollapseBlock.vue'
import ProtectionHistoryCard from './diag/ProtectionHistoryCard.vue'
import TempTrendCard from './diag/TempTrendCard.vue'
import SocCurrentCard from './diag/SocCurrentCard.vue'
import { useCollapseState } from '@/composables/useCollapseState'
import { feedback, extractErrorMessage } from '@/utils/feedback'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { haApi } from '@/api/homeassistant'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import DeviceDeleteDialog from '@/components/device/DeviceDeleteDialog.vue'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { useBreadcrumbStore } from '@/stores/breadcrumb'
import { useControlAction } from '@/composables/useControlAction'
// ── 2026-11-12 纯搬移式拆分：数据域进 composable、自包含卡片进子组件，本页只留页面级编排 ──
// 页面级约定（§5 拆分纪律）：PageHeader / 路由参数解析 / 页级 composable 调用 / 折叠记忆 /
// 页级时间窗 / 身份重验证弹窗 / 参数化表单弹窗 仍留在本文件；DOM 与文案逐字未变。
import { useBmsRealtime } from './composables/useBmsRealtime'
import { useBmsMosControl } from './composables/useBmsMosControl'
import { useBmsOperations } from './composables/useBmsOperations'
import type { ReauthTarget } from './composables/bmsOperationModel'
import BmsMetricsRow from './components/BmsMetricsRow.vue'
import BmsStatusCards from './components/BmsStatusCards.vue'
import BmsCellVoltageCard from './components/BmsCellVoltageCard.vue'
import BmsTemperatureCard from './components/BmsTemperatureCard.vue'
import BmsOpsHistorySection from './components/BmsOpsHistorySection.vue'
import { DASH, formatStamp, num, type TagType } from './bmsFormat'

// ════════════ 路由 / 设备详情（edgeDeviceApi.getDetail） ════════════
const route = useRoute()
const router = useRouter()
const edgeDeviceStore = useEdgeDeviceStore()
// 生产路由是 edge-device/:id（params），不是 demo 的 ?device= 查询串。
const deviceId = computed(() => Number(route.params.id) || 0)
/**
 * 诊断类区块的折叠状态，按设备记忆（§3.3 bms-collapse:<deviceId>）。
 * 默认折叠的 5 组：历史趋势（电芯历史+运行趋势）、保护触发历史、温度趋势、SOC 与电流、操作历史。
 * 默认全部收起，所以这里不传任何默认展开项。
 * （2026-10-21 用户要求移除「轮询间隔分布」与「实时数据流」两块，折叠项随之减少。）
 *
 * 2026-11-12 拆分边界：折叠对象仍由**页面**持有 —— 存储键 bms-collapse:<deviceId> 是页级约定，
 * 子组件只拿到这个对象（操作历史的折叠块以 prop 透传），不各自再存一份。
 */
const collapse = useCollapseState(() => deviceId.value)

/**
 * 新增诊断卡（保护触发历史 / 温度趋势 / SOC 与电流）的折叠态摘要。
 * 这些卡自己取数，摘要由它们在数据就绪后 emit('summary') 出来 ——
 * 父页不重复请求同一份历史数据（否则一个区块两次历史请求）。
 */
const diagSummary = reactive<Record<string, string>>({ protection: '', temp: '', soc: '' })
const onProtectionSummary = (t: string) => { diagSummary.protection = t }
const onTempSummary = (t: string) => { diagSummary.temp = t }
const onSocSummary = (t: string) => { diagSummary.soc = t }

/**
 * ════════════ 页级时间窗（契约 §D，2026-11-12） ════════════
 *
 * 卡片级时间窗上移到页面级，父页持有**唯一**实例（吸顶条见模板 .page-range-bar）。
 * 只驱动三处趋势图，保护触发历史不跟随（理由见模板注释）。
 *
 * 父子 props 契约：
 *   · timeRange: string                     —— '1h' | '24h' | '7d' | 'custom'
 *   · customRange?: [string, string] | null —— date-picker 的字符串区间，仅 custom 时使用；
 *     换算统一走 resolveTimeRange()，父子不许各写一份。
 */
const {
  timeRange: pageTimeRange,
  customTimeRange: pageCustomRange,
} = useTimeRange()

/** 吸顶条的可见回执：用共享 resolveTimeRange 算出真实查询区间，作为 hint 的 title（父子互相印证）。 */
const pageRangeWindow = ref<[Date, Date] | null>(null)
function syncPageRangeWindow() {
  pageRangeWindow.value = resolveTimeRange(pageTimeRange.value, pageCustomRange.value)
}
watch([pageTimeRange, pageCustomRange], syncPageRangeWindow, { immediate: true, deep: true })
const pageRangeWindowText = computed(() => {
  const w = pageRangeWindow.value
  if (!w) return ''
  return formatStamp(w[0].toISOString()) + ' → ' + formatStamp(w[1].toISOString())
})
const device = ref<EdgeDevice | null>(null)
const detailError = ref('')
const refreshing = ref(false)

/**
 * 视口是否窄屏（≤768px）。
 *
 * 为什么用 JS 判定而不是纯 CSS 媒体查询：
 *   页头在窄屏要把「同步到HA / 编辑 / 删除」收进 ⋯ 溢出菜单（少 3 个 DOM 按钮），
 *   这是**结构变化**，CSS 只能隐藏不能减少节点。用 display:none 藏起来的按钮
 *   仍会被读屏与 Tab 顺序命中，属于可访问性缺陷。
 *   JS 判定后按结构渲染是干净的（与 DeviceHeader.vue 既有做法一致）。
 *
 * SSR/测试环境没有 window.matchMedia 时按桌面处理（宽屏），保证契约测试稳定。
 */
const MOBILE_MAX = 768
const isMobileViewport = ref(
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia('(max-width: ' + MOBILE_MAX + 'px)').matches
    : false,
)
let mobileMql: MediaQueryList | null = null
const onMobileChange = (e: MediaQueryListEvent) => { isMobileViewport.value = e.matches }

const deviceName = computed(() => device.value?.name || DASH)

/*
 * 顶部框架面包屑末段显示**设备名称**（2026-10-21 用户要求：不要「边缘设备详情」）。
 * 名称异步到达，watch 到就登记；卸载清空避免串页。
 * 用 device?.name（不是 deviceName：后者在加载期是占位符 DASH，不能拿去当面包屑）。
 * 登记点必须留在页面（ChartPlotAlignment.spec 锁的就是本页这行 watch）。
 */
const breadcrumbStore = useBreadcrumbStore()
watch(
  () => device.value?.name,
  (name) => breadcrumbStore.setLeafLabel(name || ''),
  { immediate: true },
)
onBeforeUnmount(() => breadcrumbStore.clearLeafLabel())
const deviceTypeLabel = computed(() => device.value?.device_type || DASH)
const firmwareLabel = computed(() => device.value?.node?.firmware_version || DASH)
const nodeLabel = computed(() => {
  const n = device.value?.node
  if (n?.name) return n.name
  const id = device.value?.node_id
  return id === undefined || id === null || id === '' ? DASH : String(id)
})
// ⚠ 通道名只能取 channel_hardware_id（总线名）；hardware_id 是设备从站地址，语义不同。
const channelLabel = computed(() => device.value?.channel_hardware_id || DASH)
const deviceOnline = computed(() => {
  const s = device.value?.status
  return s === 'active' || s === 'online'
})
const DEVICE_STATUS_MAP: Record<string, { type: TagType; text: string }> = {
  active: { type: 'success', text: '在线' },
  online: { type: 'success', text: '在线' },
  offline: { type: 'info', text: '离线' },
  warning: { type: 'warning', text: '告警' },
  error: { type: 'danger', text: '故障' },
  disabled: { type: 'info', text: '已停用' },
  pending: { type: 'info', text: '待激活' },
  initializing: { type: 'info', text: '初始化中' },
  unknown: { type: 'info', text: '状态未知' },
}
const statusTag = computed(() => DEVICE_STATUS_MAP[device.value?.status ?? 'unknown'] ?? DEVICE_STATUS_MAP.unknown)

async function loadDetail() {
  try {
    device.value = await edgeDeviceApi.getDetail(deviceId.value)
    detailError.value = ''
  } catch (e) {
    detailError.value = extractErrorMessage(e, '设备详情加载失败')
  }
}

// ════════════ 实时数据（useBmsRealtime：快照兜底 + 首屏四态 + 字段派生） ════════════
/**
 * 这一整段（快照兜底 /latest-values、四态 loading-error-empty-data、单体电压与温度的口径）
 * 是**一个数据域**，2026-11-12 拆进 useBmsRealtime；页面只保留「什么时候调用」
 * （onMounted / 切换设备 / 刷新 / 错误重试）。四态必须由页面统一驱动三块卡片
 * （指标卡 / MOS+保护 / 电芯+温度），所以状态仍从页面往下发。
 */
const {
  wsConnected,
  connectRealtime,
  latestData,
  snapshotErrorText,
  realtimeLoading,
  realtimeError,
  realtimeEmpty,
  loadLatestSnapshot,
  retryRealtime,
  startFirstDataGrace,
  resetRealtime,
  disposeFirstDataGrace,
  cellCountLabel,
  cells,
  totalVoltage,
  maxCell,
  minCell,
  diff,
  cellVoltages,
  soc,
  socProgress,
  remainingCapacity,
  currentDisplay,
  currentHint,
  voltageSubText,
  capacitySubText,
  temps,
} = useBmsRealtime(deviceId)

/**
 * 页头状态标签（在线/离线 … + 设备类型 · 串数）。
 * 文案与改前自建页头**逐字一致**（契约测试锁定），只是渲染交给 DeviceHeader。
 */
const headerTags = computed<Array<{ text: string; type: TagType; effect: 'plain' }>>(() => [
  { text: statusTag.value.text, type: statusTag.value.type, effect: 'plain' },
  { text: deviceTypeLabel.value + ' · ' + cellCountLabel.value, type: 'info', effect: 'plain' },
])

// ════════════ 受控操作 / MOS 直控 / 身份重验证（同一批回执，故一起装配） ════════════
const ca = useControlAction(() => deviceId.value)
const { lastError, reauthVisible, reauthPassword, reauthBusy, dispatch, retryAfterReauth, cancelReauth: cancelReauthAction, clearError } = ca

/** 重验证目标：MOS 直控与受控操作共用同一个「验证当前身份」弹窗（状态不在两个 composable 各存一份） */
const reauthTarget = ref<ReauthTarget>(null)

// 两个数据域各需对方一处：MOS 需要「页面发起命令后登记跟踪」，操作域需要把 set_mos_policy
// 回执**转交**给 MOS（谁的状态谁维护）。用晚绑定表达，避免两个 composable 互相 import 成环：
// 先建操作域（onMosReceipt 只在回执到达时才被调用），再建 MOS 域并回填 mosApi。
let mosApi: ReturnType<typeof useBmsMosControl> | null = null
const ops = useBmsOperations({
  deviceId,
  isMobileViewport,
  dispatch,
  clearError,
  lastError,
  reauthVisible,
  reauthTarget,
  onMosReceipt: (op) => mosApi?.handleReceipt(op),
})
const {
  opGroups, totalOps, openGroups, toggleGroup, riskCount, opState, opBusy,
  trackCommand, historyRows, historyCollapseSummary,
  opFormVisible, opFormTarget, opFormCanRead, opFormReading, opFormPrefill,
  onOpClick, submitOpForm, readOpFormValues,
  catalogError, catalogLoading, loadOperations, resetOperations,
  markSent: markOpSent, markFailed: markOpFailed, markCancelled: markOpCancelled,
} = ops
const mos = useBmsMosControl({
  deviceId,
  deviceOnline,
  latestData,
  dispatch,
  clearError,
  lastError,
  reauthVisible,
  reauthTarget,
  trackCommand,
})
mosApi = mos
const {
  mosPhase, mosError, hasFetStatus, fetHex, chargeOn, dischargeOn, canControlMos,
  requestMosChange, resetMos, markSent: markMosSent, markFailed: markMosFailed,
} = mos

/** 重验证通过后自动继续执行（令牌单次且绑定 params，由 useControlAction 重新 confirm）。 */
async function doReauth() {
  const target = reauthTarget.value
  const ok = await retryAfterReauth()
  if (ok) {
    if (target?.type === 'mos') {
      markMosSent(target.which)
      trackCommand('set_mos_policy')
    } else if (target?.type === 'op') {
      markOpSent(target.id)
      trackCommand(target.id)
    }
    reauthTarget.value = null
    return
  }
  if (!reauthVisible.value) {
    if (target?.type === 'mos') markMosFailed(target.which, lastError.value || 'MOS 指令未能提交')
    else if (target?.type === 'op') markOpFailed(target.id, lastError.value || '操作未能提交')
    reauthTarget.value = null
  }
}

function cancelReauth() {
  cancelReauthAction()
  if (reauthTarget.value?.type === 'mos') {
    markMosFailed(reauthTarget.value.which, lastError.value || '已取消（未执行 MOS 操作）')
  } else if (reauthTarget.value?.type === 'op') {
    markOpCancelled(reauthTarget.value.id, '已取消（未执行该操作）')
  }
  reauthTarget.value = null
}

// ════════════ 轮询指令配置（共享组件 CommandFrequencySection） ════════════
/**
 * 2026-10-22 第七轮（用户诉求③「指令频率应该抽一个公共组件出来，大家一起通用」）。
 *
 * 本页原来自建一整套轮询配置卡：请求 GET /edge-devices/:id/commands、本地 interval 副本、
 * 保存与同步状态、容量门（节点上报上限 + 超限点名 + 逐行「禁用」按钮），约 500 行；
 * 而逆变器/通用设备页走的是 CommandFrequencySection（另有自己的保存后确认节点已应用逻辑）。
 * 同一业务两份实现 ⇒ 行为各缺一半、UI 漂移。现在全部收口到共享组件，本页只保留两件事：
 *   ① 页头摘要要「已启用条数 / 上限」→ 组件 @usage-change 外抛（唯一事实源在组件内）；
 *   ② 页头「刷新数据」要连带刷新指令配置 → 递增 reloadToken，组件 watch 到后重新拉取。
 */
const deviceNodeId = computed(() => device.value?.node_id ?? '')
/** 节点上报的同时可轮询条数上限（缺失 = 未上报，不显示容量提示） */
const pollLimit = computed(() => num(device.value?.max_polling_commands))
const pollLimitValue = computed(() => pollLimit.value ?? 0)
const hasPollLimit = computed(() => pollLimit.value !== null && pollLimit.value > 0)
/** 已启用轮询条数：由共享组件外抛（页面不另存一份状态，避免两套事实源） */
const enabledCount = ref(0)
/** 刷新令牌：页头「刷新数据」/切换设备时递增，驱动共享组件重新拉取指令配置 */
const commandReloadToken = ref(0)
function onUsageChange(payload: { enabled: number }) {
  enabledCount.value = payload.enabled
}

// ════════════ 折叠态摘要（§3.2：折叠不是隐藏信息，标题行要留一行关键结论） ════════════
/** 历史趋势：压差 + 总电压，两个最常用的判读量 */
const chartsCollapseSummary = computed(() => {
  const parts: string[] = []
  if (cells.value.length) parts.push('压差 ' + Math.round(diff.value * 1000) + 'mV')
  if (totalVoltage.value !== null) parts.push('总电压 ' + totalVoltage.value.toFixed(2) + 'V')
  return parts.join(' · ')
})

// ════════════ 刷新 / 生命周期 ════════════
function resetDeviceScopedState() {
  // 三个数据域各自复位（快照错误/首帧宽限、MOS 在途态与权威回读、操作执行态与跟踪目标）
  resetRealtime()
  resetMos()
  resetOperations()
  reauthTarget.value = null
}

async function refreshAll() {
  if (refreshing.value) return
  refreshing.value = true
  try {
    await loadDetail()
    // 指令配置由共享组件自己拉取：递增令牌让它重拉，不在页面里重复一份请求
    commandReloadToken.value++
    await Promise.all([loadLatestSnapshot(), loadOperations()])
    feedback.success('已刷新设备数据')
  } catch (e) {
    feedback.handleError(e, '设备数据刷新失败')
  } finally {
    refreshing.value = false
  }
}

// ════════════ 设备级操作（同步到 HA / 编辑 / 删除 / 返回）════════════
// 2026-10-17 由 demo 转正时补回：demo 是视觉稿没有这些入口，但生产页必须有。
// 2026-11-12 拆分边界：这些入口是页头的**页面级**操作（DeviceHeader 事件直连），留在页面。

/** 返回列表。用 push 而不是 back()：详情页可能是直接打开/刷新进入的，
 *  此时 history 里没有上一页，back() 会退出站点。 */
function goBackToList() {
  router.push('/edge-device')
}

const syncingHA = ref(false)
/**
 * 同步到 Home Assistant。
 * 走与 useDeviceData.handleSyncToHA **同一个 API 与同一套会话代际校验**；
 * 本页没有引入该 composable（它自带一份 device/loading 状态，与本页既有状态重复），
 * 故在此复刻同样的三步：syncDevice → 校验代际 → 成功/失败反馈。
 */
async function onSyncToHA() {
  if (syncingHA.value || !deviceId.value) return
  const id = deviceId.value
  const generation = getSessionGeneration()
  syncingHA.value = true
  try {
    // syncDevice 返回 void，失败以异常表达（后端未配置 HA 时报错）——
    // 因此不猜测返回值结构，成功即成功、失败走 catch。
    await haApi.syncDevice(id)
    assertSessionGeneration(generation)
    if (deviceId.value !== id) return
    feedback.success('设备已同步到HomeAssistant')
  } catch (e) {
    if (deviceId.value !== id) return
    feedback.handleError(e, '同步到HomeAssistant失败')
  } finally {
    if (deviceId.value === id) syncingHA.value = false
  }
}

const editVisible = ref(false)
const editSaving = ref(false)
const editName = ref('')
function onEditDevice() {
  editName.value = deviceName.value
  editVisible.value = true
}
async function submitEditDevice() {
  if (editSaving.value || !deviceId.value) return
  const name = editName.value.trim()
  if (!name) {
    feedback.warning('设备名称不能为空')
    return
  }
  const id = deviceId.value
  editSaving.value = true
  try {
    await edgeDeviceApi.update(id, { name })
    // 身份变了 ⇒ 列表与详情缓存都要失效，否则返回列表仍是旧名字
    edgeDeviceStore.invalidateLists()
    edgeDeviceStore.invalidateDetail(id)
    feedback.success('设备信息已保存')
    editVisible.value = false
    await loadDetail()
  } catch (e) {
    feedback.handleError(e, '保存失败')
  } finally {
    editSaving.value = false
  }
}

const deleteVisible = ref(false)
const deleting = ref(false)
function onDeleteDevice() {
  deleteVisible.value = true
}
async function submitDeleteDevice(deleteData: boolean) {
  if (deleting.value || !deviceId.value) return
  const id = deviceId.value
  deleting.value = true
  try {
    await edgeDeviceStore.deleteDevice(id, { delete_data: deleteData })
    feedback.success('设备已删除')
    deleteVisible.value = false
    goBackToList()
  } catch (e) {
    feedback.handleError(e, '删除失败')
  } finally {
    deleting.value = false
  }
}

watch(deviceId, () => {
  resetDeviceScopedState()
  void loadDetail()
  void loadLatestSnapshot()
  void loadOperations()
})

onMounted(() => {
  // 监听视口变化：横竖屏切换、桌面窗口缩放时页头要在「4 按钮」与「刷新+⋯」间切换
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    mobileMql = window.matchMedia('(max-width: ' + MOBILE_MAX + 'px)')
    mobileMql.addEventListener('change', onMobileChange)
    isMobileViewport.value = mobileMql.matches
  }
  // connect() 幂等：已连接/连接中会直接返回，不会打断 MainLayout 建立的连接。
  connectRealtime()
  startFirstDataGrace()
  void loadDetail()
  void loadLatestSnapshot()
  void loadOperations()
})

onUnmounted(() => {
  // 必须解绑：不解绑会在路由切换后留下指向已销毁组件的监听，属于内存泄漏
  mobileMql?.removeEventListener('change', onMobileChange)
  mobileMql = null
  disposeFirstDataGrace()
})
</script>

<style scoped>
/* 页面自有样式：页面骨架、流式行基座、页级时间窗条、窄屏通栏带（band/bleed），
   以及仍留在本页的诊断区/折叠块规则。
   卡片基座与"卡片带"在 components/bmsDetail.shared.css（本文件末尾引入，且必须是最后一个
   style 块）；各张卡自身的样式随卡片进了 components/*.vue。 */
/* ══════════ 页面骨架 ══════════ */
/**
 * ⚠ 本页是**独立 DEV 路由**（不在 MainLayout 之下），因此必须自己提供滚动容器。
 *
 * 根因（2026-10-16 用户报障"页面内容显示不全，不能向下滚动"）：
 *   src/App.vue 的全局样式是**应用外壳**模式 ——
 *     html, body { width:100%; height:100%; overflow:hidden; }
 *     #app       { width:100%; height:100%; }
 *   即整页锁死为视口高度、**由 MainLayout 内部提供滚动区**。
 *   走 MainLayout 的页面因此都正常；而本页直接渲染进 #app、没有 MainLayout，
 *   就没有任何元素承担滚动 ⇒ 内容高 2113px 却滚不动（实测滚轮后 scrollY 恒为 0）。
 *
 * 修法：本页自己当滚动容器。**不能改全局 CSS** —— 那会破坏外壳模式，
 *   影响所有依赖 MainLayout 内部滚动的页面。
 *
 * 用 height:100% + overflow-y:auto（而不是 min-height:100vh）：
 *   前者让本页恰好占满 #app 并**在自己内部**滚动，滚动条位置/行为与生产页一致。
 */
/* 生产环境本页嵌在 MainLayout 的 .el-main.main-content 里，**由那一层负责滚动**
   （main-content 有 overflow-y:auto + height:calc(100vh - 60px) + padding:20px）。
   所以这里**不能**再写 height:100%/overflow-y:auto —— 那会造出第二个滚动容器，
   页面出现双滚动条、内层滚完外层还能滚。（评审 demo 当时是独立路由、
   没有 MainLayout 外壳，才需要自己滚。）
   保留 overflow-anchor:none：滚动锚定由浏览器布局引擎执行，不经过 JS 的 scrollTop
   setter、也不发 scroll/focus 事件，实测插桩时 0 次程序化滚动、scrollTop 纹丝不动；
   当锚点落在持续刷新的列表里、行又被 MAX_STREAM_ROWS 修剪时，锚点消失 ⇒
   浏览器重选锚点并调整滚动位置 ⇒ 整页跳到列表处（用户报障过）。
   禁用锚定对**最近的可滚动祖先**生效，在 main-content 上同样有效。 */
.bms-detail-page {
  overflow-anchor: none;
  color: var(--el-text-color-primary);
  font-size: 13px;
  padding-bottom: 24px;
}
/* 页头已改由共享 DeviceHeader 渲染（2026-10-21），本页不再维护 .page-head、.crumb、.dev-*、.head-* 样式 */

/* 流式：不再用 1480px 上限把内容钉在中间，宽度自适应可用空间 */
.page-body { max-width: 100%; margin: 0 auto; padding: 16px 24px; }

/* ══════════ 流式布局（2026-10-16 用户要求） ══════════
   改前每行是**固定列数 grid**：列数与比例都写死
     .metrics    repeat(4, 1fr)
     .mos-protect 5fr / 7fr   …等
   问题：① 窄屏不重排，只把卡片越挤越窄，内容被压扁、出现横向溢出；
        ② 某张卡内容变多也只能在本列里挤，不能借用相邻空间；
        ③ 比例是"平均分配"，不反映卡片实际需要多宽。

   改成 flex + flex-wrap 的**流式**：
     · 每张卡用 flex-basis 声明"希望多宽"（按设计意图，大卡 520/600、小卡 320/380）；
     · min-width 是硬底线，保证不会被压到不可读；
     · 一行放不下就**自动换行**，窄屏自然堆叠成单列。
   这是流式与硬栅格的关键差别：宽度由内容需要决定，而不是由列数决定。

   ⚠ 关于 align-items（2026-10-20 修正，两次迭代才定稿）：

     · flex-start（原值）：矮卡不被拉高，但**同排底边参差** —— 实测 MOS 312 vs 保护 279
       差 34px、实时流 461 vs 轮询 446 差 16px，视觉上并排两块像被切断。
     · 全行 stretch：底边齐了，但「受控操作 740px」会把「操作历史 129px」拉成 740px，
       凭空多出 611px 空白 —— 比参差更糟（老注释警告的正是这个）。
     · **定稿**：按"同排两卡高度是否相近"分别处理：
         - 高度天然相近的并排行（指标卡 / MOS+保护 / 实时流+轮询）→ stretch，底边齐且
           多出的空白 ≤40px，可接受；
         - 高度悬殊的行（受控操作 740 vs 操作历史 129）→ 保持 flex-start，
           不为对齐制造 600px 空白。
       判据是**实测高度差**，不是"看起来应该并排"。 */
.row {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  margin-bottom: 16px;
  align-items: flex-start;
}

/* 高度相近的并排行：同排等高，消除底边参差（差值实测均 ≤40px） */
.row.metrics,
.row.mos-protect,
.row.stream-poll {
  align-items: stretch;
}
.row > .card,
.row > .metric-card { flex-grow: 1; flex-shrink: 1; }
/* 同排等高后：卡片自身作为纵向 flex 容器，内容顶部对齐、空白留在底部间隙，
   避免"卡片被拉高但内容仍挤在顶部"的空旷感 */
.row > .card { display: flex; flex-direction: column; }
.row > .card > .card-head { flex-shrink: 0; }

/* 各组按设计意图给大/小卡不同的基准宽度；窄屏自动堆叠 */
.diag-row > .card            { flex: 1 1 100%; }   /* 新增诊断卡同样独占一行（图表宽度优先） */
/* 2026-10-21：该行原为「实时数据流 + 轮询指令配置」并排，数据流移除后
   轮询配置独占整行（若仍沿用 380px 基准，宽屏下会被压成一条窄条）。
   2026-10-22：轮询配置改用共享组件 CommandFrequencySection（根元素 .command-frequency-card），
   它是组件根节点、带本页 scoped 属性，故此处选择器可直接命中。 */
.stream-poll > .command-frequency-card { flex: 1 1 100%; min-width: 0; margin-top: 0; }

/* ══════════ 响应式 ══════════ */
/* 流式布局下 .row 已是 flex —— 换行由 flex-basis / min-width 自动决定，
   下面这些 grid-template-columns 覆盖在 flex 容器上已**完全失效**
   （留着会让后人以为窄屏重排还靠它，属于误导性死代码），故改为收窄大卡基准宽度，
   让"大小卡并排"更早变成堆叠。 */

/*
  桌面：页头卡片必须与内容卡片同一条左右边界。
  改前 .device-header 直接坐在 .el-main 的内容盒里（1440 实测 left 220 / width 1192），
  而 .page-body 有 24px 内距（卡片 left 244 / width 1144）—— 页头卡片比所有卡片宽 48px，
  用户看到的就是「顶部内容宽度与下方不一致」。
  只在 >768px 生效：窄屏那套是通栏带，页头与内容都贴边，无此问题。
*/
/*
  页级时间窗条（桌面基线）。2026-11-12：卡片里的 3 个时间窗控件上移到页面级，
  这里给出桌面布局；窄屏在 @media(max-width:768px) 里变成吸顶条（见下方 ④）。
*/
.page-range-bar {
  display: flex;
  align-items: center;
  gap: 8px; /* 相邻控件间距 ≥6px（图表工具条例外档的前提之一） */
  min-height: 32px;
  margin: 0 0 12px;
}
.page-range-label {
  font-size: 12px;
  color: var(--el-text-color-regular);
  white-space: nowrap;
  margin-right: 2px;
}
/* 作用域说明（不是「全页共用」）：保护触发历史有自己的 7/30/90 天档，不跟随本条 */
.page-range-hint {
  margin-left: auto;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
@media (min-width: 769px) {
  .bms-detail-page :deep(.device-header) {
    margin-left: 24px;
    margin-right: 24px;
  }
}

/* ══════════ 身份重验证 ══════════ */
.dialog-note { margin: 0 0 12px; font-size: 13px; line-height: 1.7; color: var(--el-text-color-regular); }

@media (max-width: 768px) {
  /*
    页头（2026-10-21 起由共享 DeviceHeader 渲染）：本页不再维护页头视觉，
    窄屏两行布局与操作组全部在 DeviceHeader 内。这里只保留一处本页专属样式 ——
    副文本插槽的紧凑折行，与改前 .dev-sub 的窄屏规则**等价**：
    每个 span 是不可断的「词」，放不下就整词换行（序列号 F0F5BD02DD84 不会被拦腰截断），
    信息完整可读，不横滚也不省略。
  */
  .page-body { padding: 12px; }
  /*
    ══════════ 全页统一「通栏带」（2026-11-12 第二轮：一致性）══════════

    改前：只有 3 条 .diag-row 通栏，其余区块仍是 321px 圆角卡 —— 同一页并存两套宽度系统。
    改后：所有区块（含页头）贴到滚动容器可见宽，8px 间距，无圆角/左右描边，
    带内文字 12px 内距，图表画布用负 margin 抵消这 12px。

    ⚠ 两个坑（都踩过）：
      ① 页头不在 .page-body 里，只能抵消 --shell-x(20)；按 --bleed-l(32) 会右溢出 12px；
      ② CollapseBlock 在插槽外多包一层 .collapse-body ⇒ 必须用**后代**选择器，
         `> .collapse-block > .card` 匹配不到卡片（实测 diag 卡仍是 12px 内距 + 1px 描边，
         画布 385 → 359）。
  */
  .bms-detail-page {
    --shell-x: 20px;   /* MainLayout .el-main 左右内距 */
    --gutter-x: 12px;  /* 本页 .page-body 窄屏内距 */
    --bleed-l: calc(var(--shell-x) + var(--gutter-x));
    --bleed-r: calc(var(--shell-x) + var(--gutter-x));
  }
  .page-body > .row,
  .page-body > .collapse-block {
    margin-left: calc(-1 * var(--bleed-l));
    margin-right: calc(-1 * var(--bleed-r));
    margin-bottom: 8px;
    gap: 8px;
  }
  .bms-detail-page :deep(.device-header) {
    margin-left: calc(-1 * var(--shell-x));
    margin-right: calc(-1 * var(--shell-x));
    margin-bottom: 8px;
    padding: 10px var(--gutter-x) 12px;
    border-radius: 0;
    border-left: 0;
    border-right: 0;
  }
  /* 带壳：区块内卡片去圆角/左右描边/阴影，统一 10px 12px 内距。
     .page-body > .row .card 的同名规则在 bmsDetail.shared.css：子组件内的卡片只会被
     子组件自己的 scoped 副本命中，故两条各放一处；这里只保留直接挂在 .collapse-block 下的卡片。 */
  .page-body > .collapse-block .card {
    border-radius: 0;
    border-left: 0;
    border-right: 0;
    box-shadow: none;
    padding: 10px var(--gutter-x);
  }
  /* 裸折叠块（历史趋势）内部的卡片是「带内子区」：透明表面 + 一条分隔线 */
  .page-body > .collapse-block .card {
    background: transparent;
    border-top: 1px solid var(--card-border);
  }
  /* 保护触发历史不是图表（CSS 条形 + 30 天档），画布内距归零后它的文字会贴边 ⇒ 单独补 12px */
  #collapse-body-protection-history .diag-body { padding: 0 var(--gutter-x); }

  /* 图表卡：横向内距归零，画布自己贴边 —— 该规则与"带壳"规则同特异性（0,4,0），
     原本靠"后写的赢"；拆开后带壳规则进了 bmsDetail.shared.css，故这条也随它搬过去，
     保证层叠顺序与拆分前一致（详见该文件同名注释）。 */
  /* 指标区：4 个 tile 是行的直接子项（没有 .card 外壳），行自己做带并给 12px 内距 */
  .row.metrics {
    padding: 10px var(--gutter-x);
    background: var(--card-bg);
    border: 1px solid var(--card-border);
    border-left: 0;
    border-right: 0;
    border-radius: 0;
  }
  .row.metrics { gap: 8px; }
  /* 裸折叠块本身也是「带」 */
  .page-body > .collapse-block {
    background: var(--card-bg);
  }
  /* 字号档位：标题 14/600、徽标 18px 高 10px 字、提示与脚注 11px */
  .page-body :deep(.card-title) { font-size: 14px; font-weight: 600; }
  .page-body :deep(.card-hint),
  .page-body :deep(.card-note),
  .page-body :deep(.card-footnote) { font-size: 11px; line-height: 1.5; }
  .page-body :deep(.el-tag) { font-size: 10px; height: 18px; line-height: 16px; padding: 0 5px; }
  /* 空态：el-empty 插画 48px、自绘插画收起、历史空态内距 16px */
  .page-body :deep(.el-empty) { padding: 12px 0; }
  .page-body :deep(.el-empty__image) { width: 48px; height: 48px; }
  .page-body :deep(.el-empty__description) { margin-top: 8px; }
  .page-body :deep(.el-empty__description p) { margin: 0; font-size: 12px; line-height: 1.5; }
  .page-body :deep(.command-frequency-card .empty-illustration) { display: none; }
  .page-body :deep(.command-frequency-card .empty-content) { padding: 12px 0; }
  .page-body :deep(.history-empty) { padding: 16px 12px; }
  /* 页级时间窗条：吸顶 + 与其它带同表面 + 横向出血 */
  .page-range-bar {
    position: sticky;
    top: calc(-1 * var(--shell-x));
    z-index: 5;
    margin-left: calc(-1 * var(--bleed-l));
    margin-right: calc(-1 * var(--bleed-r));
    margin-bottom: 8px;
    /* 条本身已出血到视口，内距用文字档 12px（用 --bleed 会把可用宽压到 313，hint 被迫换行） */
    padding: 4px var(--gutter-x);
    min-height: 44px;
    background: var(--card-bg);
    box-shadow: 0 1px 0 var(--el-border-color-lighter);
  }
  /* 窄屏隐藏「温度 / SOC 共用」回执：361px 里 4 段选择器已占 230，再放 110px 文案会折行（实测条高 63）。
     该信息在窄屏由「只有这一个时间窗」本身表达，桌面保留。 */
  .page-range-bar .page-range-hint,
  .page-range-bar .page-range-label { display: none; }

  /* 图表工具条例外档 36px（规范 §4.4 第 5 条）：压回被 .page-body .el-button{min-height:44px} 顶上去的按钮 */
  .page-range-bar :deep(.el-button:not(.is-circle)) {
    min-height: var(--control-h-toolbar-touch, 36px);
    height: var(--control-h-toolbar-touch, 36px);
  }

  .bms-sub {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0 4px;
    font-size: 11px;
    line-height: 17px;
  }
  .bms-sub > span { white-space: nowrap; }
  /* ── 新增诊断卡（保护触发历史 / 温度趋势 / SOC 与电流 / 轮询间隔分布）──
     ① 折叠块占满一行：它是 .row 的 flex 子项，不给 basis 会按内容宽做「窄条」；
     ② 卡片头在窄屏整块隐藏：折叠头已经写了区块名，两级同名标题就是用户点名的「顶部重复内容」。
        组件内容区自带工具行（时间范围选择器 / 弹层触发器），不会因此丢控件。 */
  .diag-row > .collapse-block { flex: 1 1 100%; min-width: 0; }
  .diag-card > .card-head { display: none; }
  .page-body .el-button:not(.is-circle) { min-height: 44px; }
  /* ── 硬 min-width 在窄屏必须清零 ──
     根因（实测）：.ops-card { min-width: 400px }、.command-frequency-card（旧 .poll-card）等在
     等在 318px 的容器里**不能收缩**（min-width 是硬底线），于是卡片宽 400、
     整页 el-main scrollWidth 432 > clientWidth 382，页面可以横向拖动。
     桌面端这些 min-width 用于保证"卡片不会被压到不可读"，窄屏已经是单列堆叠，
     不需要它们 —— 清零后卡片跟随容器 100% 宽。
     （2026-11-12 拆分：卡片侧的同名规则随卡片进了 bmsDetail.shared.css 与各子组件，
       这里只留页面自己持有的 CommandFrequencySection 根节点。） */
  .stream-poll > .command-frequency-card {
    flex: 1 1 100%;
    min-width: 0;
  }
}

@media (pointer: coarse) {
  .page-body .el-button:not(.is-circle) { min-height: 44px; }
}
</style>

<style scoped src="./components/bmsDetail.shared.css"></style>
