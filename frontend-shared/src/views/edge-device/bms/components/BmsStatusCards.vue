<template>
  <section class="row mos-protect">
    <!-- 诉求① 落点：MOS 直控卡（页首主位，不再埋进受控操作清单） -->
    <div class="card mos-card" :aria-busy="realtimeLoading ? 'true' : undefined">
      <div class="card-head">
        <span class="card-title">MOS 直控</span>
        <span class="card-hint">像电灯开关一样直接操作</span>
        <span class="head-right mono-badge">{{ hasFetStatus ? 'fet_status = 0x' + fetHex : 'fet_status = ' + DASH }}</span>
      </div>

      <!-- 加载中：骨架占位。**不渲染「关断/导通」行，也不写「尚无 fet_status 数据」**
           —— 那两种表达都会把「未知」说成事实（2026-10-21 用户真机反馈）。 -->
      <div v-if="realtimeLoading" class="realtime-state" role="status">
        <el-skeleton :rows="3" animated />
        <p class="realtime-state-note">正在加载 MOS 状态…（尚未收到 fet_status，未知不等于关断）</p>
      </div>
      <!-- 失败：独立错误态 + 重试（与「暂无数据」彻底分开） -->
      <div v-else-if="realtimeError" class="realtime-state" role="alert">
        <el-alert type="error" :closable="false" show-icon class="inline-alert">
          <template #title>MOS 状态加载失败（当前未知，不等于关断）</template>
          <div class="alert-detail">
            {{ snapshotErrorText }}
            <el-button size="small" text type="primary" @click="retryRealtime">重试</el-button>
          </div>
        </el-alert>
      </div>
      <template v-else>
        <p class="card-note">
          充电 / 放电回路通断的软件策略 <code>set_mos_policy</code>。开关即时下发，切换前弹
          <b>一次极简确认</b>（只有 确认 / 取消，不填理由）；后端回读 fet_status 校验，不一致即判失败并常驻原因。
        </p>

        <div v-for="m in mosRows" :key="m.key" class="mos-item">
          <div class="mos-row" :class="{ 'is-applying': !!m.phase }">
            <span class="mos-icon" :class="m.known ? (m.on ? 'is-on' : 'is-off') : 'is-unknown'">
              <el-icon :size="18"><component :is="m.known && m.on ? Open : Lock" /></el-icon>
            </span>
            <div class="mos-main">
              <div class="mos-name-line">
                <span class="mos-name">{{ m.name }}</span>
                <!-- 未知（尚未收到 fet_status）显示中性「未知」，不得伪装成「关断」 -->
                <el-tag :type="m.known ? (m.on ? 'success' : 'info') : 'info'" size="small" effect="plain">
                  {{ m.known ? (m.on ? '导通' : '关断') : '未知' }}
                </el-tag>
                <el-tag v-if="m.phase" type="primary" size="small">{{ m.phase }}…</el-tag>
                <span v-if="m.hwOff" class="hw-off-note"><el-icon :size="12"><WarningFilled /></el-icon>可能被硬件保护关断</span>
              </div>
              <div class="mos-desc">{{ m.desc }}</div>
            </div>
            <el-switch
              class="mos-switch"
              :model-value="m.known ? m.on : false"
              :loading="!!m.phase"
              :disabled="!!m.phase || !canControlMos"
              :aria-label="m.name + (m.known ? '开关' : '开关（状态未知，暂不可操作）')"
              @change="(v: string | number | boolean) => requestMosChange(m.key, !!v)"
            />
          </div>
          <el-alert v-if="m.error" type="error" :closable="false" show-icon class="inline-alert">
            <template #title>{{ m.name }}操作失败（回读校验不一致）</template>
            <div class="alert-detail">{{ m.error }}</div>
          </el-alert>
        </div>

        <p v-if="!canControlMos && !deviceOnline" class="card-footnote mos-manual-note">
          当前不可控制：设备离线（状态 {{ statusText }}），指令无法下发。
        </p>
        <p v-else-if="!hasFetStatus" class="card-footnote mos-manual-note">
          当前不可控制：尚未收到 fet_status 字段（状态未知，不等于关断）。
        </p>

        <p class="card-footnote">
          <b>关断</b>是正常工况而非故障，因此用中性色；只有「保护已触发」才显示告警色。
        </p>
      </template>
    </div>

    <!-- 诉求④ 落点：保护状态（正常态紧凑一行 / 有保护位时展开 12 项明细） -->
    <div class="card protect-card" :aria-busy="realtimeLoading ? 'true' : undefined">
      <div class="card-head">
        <span class="card-title">保护状态</span>
        <!-- 状态标签是互斥状态展示：关掉 el-tag 的切换动画，
             否则 loading→data 的 300ms 淡出/淡入会让两个状态标签同时出现（实测） -->
        <el-tag v-if="realtimeLoading" type="info" size="small" effect="plain" disable-transitions>加载中…</el-tag>
        <el-tag v-else-if="realtimeError" type="danger" size="small" effect="plain" disable-transitions>加载失败</el-tag>
        <el-tag v-else-if="!hasProtectionData" type="info" size="small" effect="plain" disable-transitions>暂无数据</el-tag>
        <el-tag v-else-if="activeProtections.length" type="danger" size="small" disable-transitions>
          {{ activeProtections.length }} 项保护已触发
        </el-tag>
        <el-tag v-else type="success" size="small" effect="plain" disable-transitions>
          <el-icon :size="12"><CircleCheckFilled /></el-icon> 全部正常
        </el-tag>
        <span class="head-right mono-badge">{{ hasProtectionData ? 'protection_status = 0x' + protHex : 'protection_status = ' + DASH }}</span>
      </div>

      <!-- 加载中：骨架占位。**绝不渲染 12 个「正常」徽标** ——
           那会把「未知」伪装成「全部正常」，是安全语义错误（用户真机反馈）。 -->
      <div v-if="realtimeLoading" class="realtime-state" role="status">
        <el-skeleton :rows="3" animated />
        <p class="realtime-state-note">正在加载保护状态…（尚未收到 protection_status，未知不等于正常）</p>
      </div>
      <!-- 失败：独立错误态 + 重试（与「暂无数据」彻底分开） -->
      <el-alert v-else-if="realtimeError" type="error" :closable="false" show-icon class="inline-alert">
        <template #title>保护状态加载失败（未知，不能当作「全部正常」）</template>
        <div class="alert-detail">
          {{ snapshotErrorText }}
          <el-button size="small" text type="primary" @click="retryRealtime">重试</el-button>
        </div>
      </el-alert>
      <template v-else>
        <!-- 无保护命中：紧凑一行。
             ⚠ 2026-10-16 用户要求：**删除收起/展开，明细默认展开**。
             原来正常态只给一行摘要 + 「展开明细」按钮，要用户多点一次才能看到
             12 位明细；而这页的主要用途就是核对保护位，默认藏起来反而妨碍阅读。
             现在 12 项明细恒定可见（见下方 .protect-grid），少一层交互。 -->
        <div
          v-if="!activeProtections.length"
          class="protect-compact"
          :class="{ 'is-all-normal': hasProtectionData }"
          role="status"
        >
          <el-icon :size="18" :color="hasProtectionData ? 'var(--el-color-success)' : 'var(--el-text-color-secondary)'"><CircleCheckFilled /></el-icon>
          <span class="protect-compact-text">{{ hasProtectionData ? '12 项保护均未触发（bit0 – bit11 全 0）' : '暂无 protection_status 数据（设备未上报，未知不等于正常）' }}</span>
        </div>

        <!-- 有保护命中：结论 + 明细 -->
        <template v-else>
          <el-alert type="error" :closable="false" show-icon class="inline-alert">
            <template #title>{{ activeProtections.map(p => p.label).join('、') }} 已触发</template>
            <div class="alert-detail">
              protection_status = 0x{{ protHex }}。硬件保护可能已覆盖对应 MOS：请先排除故障，再到左上方尝试开关。
            </div>
          </el-alert>
        </template>

        <!-- 12 位明细：**恒定展开**（用户要求删除收起/展开）。
             不再依赖 protectExpanded 状态，因此也不会有"默认收起"的信息缺口。
             未知（尚未收到 protection_status）显示中性「未知」，不得伪装成「正常」。 -->
        <div class="protect-grid">
          <div v-for="p in protectionBits" :key="p.bit" class="protect-item" :class="{ active: p.active }">
            <span class="protect-name">{{ p.label }}</span>
            <el-tag :type="p.active ? 'danger' : hasProtectionData ? 'success' : 'info'" size="small" :effect="p.active ? 'dark' : 'plain'">
              {{ p.active ? '已触发' : (hasProtectionData ? '正常' : '未知') }}
            </el-tag>
            <span class="protect-bit">bit{{ p.bit }}</span>
          </div>
        </div>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Open, Lock, CircleCheckFilled, WarningFilled } from '@element-plus/icons-vue'
import { DASH } from '../bmsFormat'
import { useBmsProtection } from '../bmsProtection'

/**
 * BMS 首屏两块强相关卡片：MOS 直控（诉求①）+ 保护状态（诉求④）。
 *
 * 为什么合成一个子组件而不是各自一个：两块共享同一份实时状态（四态 loading/error、
 * fet_status、protection_status）与同一条错误重试入口，拆成两个会各自重复一遍
 * props/emit 契约，反而更难看出"它们读的是同一帧数据"。
 *
 * 边界（哪一段留在页面）：
 *   · 四态判定（realtimeLoading / realtimeError / snapshotErrorText / retryRealtime）由页面
 *     的 useBmsRealtime 持有 —— 首屏加载语义是**页面级**契约，不属于某张卡；
 *   · MOS 的位语义与写后回读桥接在 useBmsMosControl；本组件只负责把 phase/error 渲染出来，
 *     并按「本帧没有 fet_status」的语义拼装两行展示文案（含硬件保护提示 hwOff）。
 *   · 保护位口径在 bmsProtection.ts（与 MOS 卡共用：bit0 单体过压 / bit9、bit10 会提示"可能被硬件保护关断"）。
 *
 * 回调型 props（retryRealtime / requestMosChange）刻意沿用页面里的同名函数：
 * 本组件是"展示 + 转发"，确认弹窗、下发、回读校验全部留在页面侧，行为与拆分前一致。
 */
const props = defineProps<{
  realtimeLoading: boolean
  realtimeError: boolean
  snapshotErrorText: string
  retryRealtime: () => void
  requestMosChange: (which: 'charge' | 'discharge', target: boolean) => void
  mosPhase: Record<'charge' | 'discharge', string>
  mosError: Record<'charge' | 'discharge', string>
  hasFetStatus: boolean
  fetHex: string
  chargeOn: boolean
  dischargeOn: boolean
  canControlMos: boolean
  deviceOnline: boolean
  statusText: string
  latestData: Record<string, any> | null
}>()

const { hasProtectionData, protHex, protectionActive, protectionBits, activeProtections } = useBmsProtection(() => props.latestData)

/**
 * 两行 MOS 的展示模型。desc 的三种文案是**安全语义**，不得简化：
 *   未知（本帧没有 fet_status）≠ 关断，必须单独一行说明，不能伪装成"禁止充电"。
 * hwOff：硬件保护可能已覆盖对应 MOS（充电看 bit0，放电看 bit9/bit10），提示先排障再开关。
 */
const mosRows = computed(() => [
  {
    key: 'charge' as const,
    name: '充电 MOS',
    on: props.chargeOn,
    phase: props.mosPhase.charge,
    error: props.mosError.charge,
    known: props.hasFetStatus,
    hwOff: props.hasFetStatus && !props.chargeOn && protectionActive(0),
    desc: !props.hasFetStatus
      ? '尚未收到 fet_status 字段（状态未知，不等于关断）'
      : props.chargeOn ? '允许充电 · fet_status bit0 = 1' : '禁止充电 · fet_status bit0 = 0',
  },
  {
    key: 'discharge' as const,
    name: '放电 MOS',
    on: props.dischargeOn,
    phase: props.mosPhase.discharge,
    error: props.mosError.discharge,
    known: props.hasFetStatus,
    hwOff: props.hasFetStatus && !props.dischargeOn && (protectionActive(9) || protectionActive(10)),
    desc: !props.hasFetStatus
      ? '尚未收到 fet_status 字段（状态未知，不等于关断）'
      : props.dischargeOn ? '允许放电 · fet_status bit1 = 1' : '禁止放电 · fet_status bit1 = 0',
  },
])
</script>

<style scoped>
/* ══════════ MOS 直控 ══════════ */
.mos-item { border-top: 1px solid var(--el-border-color-lighter); padding-top: 8px; margin-top: 8px; }
.mos-item:first-of-type { border-top: none; margin-top: 0; padding-top: 0; }
.mos-row { display: flex; align-items: center; gap: 12px; min-height: 48px; }
.mos-row.is-applying { opacity: 0.72; }
.mos-icon { display: flex; align-items: center; justify-content: center; width: 34px; height: 34px; border-radius: var(--radius-sm); background: var(--el-fill-color-light); flex-shrink: 0; }
.mos-icon.is-on { color: var(--el-color-success); }
.mos-icon.is-off { color: var(--el-text-color-secondary); }
/* MOS 状态未知时图标用中性色：既不是「导通」也不是「关断」 */
.mos-icon.is-unknown { color: var(--el-text-color-secondary); }
.mos-main { flex: 1; min-width: 0; }
.mos-name-line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.mos-name { font-size: 14px; font-weight: 600; }
.mos-desc { margin-top: 3px; font-size: 12px; color: var(--el-text-color-secondary); }
.mos-switch { flex-shrink: 0; }
.mos-manual-note { margin-top: 8px; }
.hw-off-note { display: inline-flex; align-items: center; gap: 4px; font-size: 11px; color: var(--el-color-warning); }

/* ══════════ 保护状态 ══════════ */
.protect-compact { display: flex; align-items: center; gap: 8px; padding: 6px 0; flex-wrap: wrap; }
.protect-compact-text { font-size: 13px; font-weight: 500; }
/* 流式：12 项明细按可用宽度自动决定列数（窄屏 2 列 / 宽屏 4 列以上），
   而不是写死 3 列 —— 写死会让窄屏每格过窄、宽屏留白过多。 */
.protect-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 8px; margin-top: 8px; }
.protect-item {
  display: flex; align-items: center; gap: 6px; padding: 8px 10px;
  background: var(--el-fill-color-lighter); border: 1px solid transparent;
  border-radius: var(--radius-sm); min-width: 0;
}
.protect-item.active { background: var(--el-color-danger-light-9); border-color: var(--el-color-danger-light-5); }
.protect-name { font-size: 12px; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.protect-bit { font-size: 11px; color: var(--el-text-color-secondary); font-family: 'SF Mono', Consolas, Monaco, monospace; }

/* 两块卡在本行里的基准宽度（窄屏由下方 768 档清成整行） */
.mos-protect > .mos-card      { flex-basis: 380px; min-width: 320px; }
.mos-protect > .protect-card  { flex-basis: 520px; min-width: 340px; }

@media (max-width: 1200px) {
  .mos-protect > .protect-card { flex-basis: 420px; }
}
/* 平板/窄桌面（769~1100）：保护项 2 列。
   原来单列的原因：2 列时 820/1024 会把「前端 IC 错误」截断 18~19px。
   但那是**旧实现只给 name 一段固定宽度 + nowrap 省略号**导致的。
   现在 name 允许折行（见下），2 列不会截断，且比单列省一半高度。 */
@media (max-width: 1100px) {
  .protect-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .protect-name { white-space: normal; overflow: visible; text-overflow: clip; }
}

@media (max-width: 768px) {
  /* ── 保护状态：3 列紧凑网格（670px → 目标 ≤250px）──────────────
     现状问题（实测 393px）：单列布局，12 项 × 44px 触控高 = 670px，
     占 0.77 屏；每项内部「名称 + 正常标签 + bitN」三段横排，
     而右侧还有大片空白（卡片 321px 但内容只用到 295px）。

     关键决策：**bitN 在移动端隐藏，名称必须保留**。
     理由：bitN 是给工程师对寄存器用的次要信息，名称才是用户判断"哪项保护"的依据；
     用户此前明确批评"短路保护被截成短路…"。两者冲突时保名称、舍 bitN。
     桌面端 bitN 保留（空间够）。

     3 列而非 2 列：实测每格 ~103px 恰好放得下 4 字名称（12px 字号），
     2 列会浪费宽度、1 列就是现状。 */
  .protect-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 4px;
    margin-top: 4px;
  }
  .protect-item {
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 1px;
    padding: 4px 3px;
    /* 名称 15 + gap 1 + 标签 16 + 上下内距 8 = 40px，仍高于 WCAG 的 24px 下限 */
    min-height: 40px;
  }
  .protect-name {
    /* 名称必须完整可读：允许折行，不用省略号 */
    white-space: normal;
    overflow: visible;
    text-overflow: clip;
    text-align: center;
    font-size: 12px;
    line-height: 15px;
    flex: none;
    width: 100%;
  }
  /* bitN 收起（移动端）；桌面端仍显示 */
  .protect-bit { display: none; }
  /* 「正常/已触发」标签收紧，不再单独占一行高度。
     ⚠ 选择器多带一级 .protect-card：页面的 .page-body :deep(.el-tag)（窄屏 18px 高 / 10px 字）
       与拆前这条规则**特异性相同**，原本靠"后写的赢"；拆分后两者在不同组件里，顺序不可依赖，
       故提升一级保证计算结果与拆分前逐像素一致（16px 高 / 11px 字 / 内距 4px）。 */
  .protect-card .protect-item :deep(.el-tag) {
    height: 16px;
    line-height: 14px;
    padding: 0 4px;
    font-size: 11px;
  }

  /*
    移动端把「12 项保护均未触发（bit0 – bit11 全 0）」这行摘要收掉 30px。

    为什么可以收：卡片头已有「全部正常」绿标签 + protection_status = 0x00 徽章，
    这句是同义重复，而它正是超出 250px 目标的那部分。

    ⚠ 只收「全正常」这一种（.is-all-normal），**「暂无 protection_status 数据」
      必须保留** —— 那是"采不到数据"的重要提示，藏掉等于让用户以为一切正常，
      属 fail-open。有保护触发时走另一分支，也不受影响。
  */
  .protect-compact.is-all-normal { display: none; }

  /*
    卡片头压缩 47 → ~28px：
    393px 下「保护状态 + 全部正常标签 + protection_status = 0x00 徽章」放不下，
    徽章被挤到第二行（monospace 长串换行占 20px）。

    ⚠ 隐藏的是**徽章**不是标签：
      · 「全部正常 / N 项保护已触发 / 暂无数据」标签保留（这是用户要看的结论）；
      · protection_status = 0x00 是给工程师对寄存器用的原始值，
        移动端属于"开发者文案"（用户明确批评过这类内容），且完整值在
        protectionBits 的 12 格里已逐位体现。桌面端保留。
  */
  .protect-card .card-head { margin-bottom: 6px; }
  .protect-card .card-head .mono-badge { display: none; }
  .protect-card .card-head { min-height: 0; padding-bottom: 0; }

  /*
    ══════════ MOS 直控：364px → 目标 ≤200px ══════════

    移动端收起三段**说明性长文本**，保留全部操作元素（2 个开关 + 状态标签）：
      ① .card-note（"充电/放电回路通断的软件策略 set_mos_policy。开关即时下发…"4 行）
         —— 解释机制，用户点一下开关就能理解，属"首次阅读才有价值"的内容；
      ② .card-footnote（"关断是正常工况而非故障…"）
         —— 解释配色决策，同理；
      ③ mono-badge（fet_status = 0x03）—— 原始寄存器值，工程师用，同保护卡处置。

    ⚠ 不收起：mos-name（充电/放电 MOS）、状态标签（导通/关断）、
      mos-desc（允许充电/允许放电）、开关本身、错误 alert、不可控提示。
      这些是操作与状态，一个都不能少。
  */
  .mos-card .card-note,
  .mos-card .card-footnote,
  .mos-card .card-head .mono-badge { display: none; }
  .mos-card .card-head { margin-bottom: 8px; }
  .mos-row { padding: 8px 0; }
  .mos-icon { width: 28px; height: 28px; }
  .mos-name { font-size: 14px; }
  .mos-desc { font-size: 11px; }
  .mos-switch { min-height: 44px; }

  /* 硬 min-width 在窄屏必须清零（见公共基座同名说明） */
  .mos-protect > .mos-card,
  .mos-protect > .protect-card {
    flex: 1 1 100%;
    min-width: 0;
  }
}
@media (pointer: coarse) {
  .mos-switch { min-height: 44px; }
}
</style>

<style scoped src="./bmsDetail.shared.css"></style>
