<template>
  <el-dialog
    :model-value="visible"
    :title="isEdit ? '编辑通知通道' : '新建通知通道'"
    width="680px"
    :close-on-click-modal="false"
    data-test="nc-dialog"
    @update:model-value="(v: boolean) => emit('update:visible', v)"
  >
    <el-form :model="form" label-width="132px" data-test="nc-form">
      <el-form-item label="名称" required>
        <el-input
          v-model="form.name"
          maxlength="64"
          placeholder="如：运维群"
          data-test="field-name"
        />
      </el-form-item>

      <el-form-item label="类型" required>
        <el-select v-model="form.type" data-test="field-type" @change="onTypeChange">
          <el-option v-for="opt in TYPE_OPTIONS" :key="opt.value" :label="opt.label" :value="opt.value" />
        </el-select>
        <div class="hint" data-test="type-hint">切换类型会预填 URL 与请求体模板（仍可手工修改）。</div>
      </el-form-item>

      <el-form-item label="目标地址" required>
        <el-input
          v-model="form.target_url"
          maxlength="512"
          placeholder="https://…"
          data-test="field-target-url"
        />
      </el-form-item>

      <!--
        密钥字段（设计 §8）：type="password" + 编辑态「已设置（末 4 位 xxxx）」+ 留空即不修改。
        这里**只**绑定用户本次输入，绝不把 secret_hint 回填进输入框 —— hint 是"末 4 位"，
        回填等于把 4 位假密钥当成真密钥提交（清掉用户已配置的凭据）。
      -->
      <el-form-item label="密钥">
        <el-input
          v-model="secretInput"
          type="password"
          show-password
          autocomplete="new-password"
          :placeholder="secretPlaceholder"
          data-test="field-secret"
        />
        <div class="hint" data-test="nc-secret-state">{{ secretStateText }}</div>
        <el-button
          v-if="canClearSecret"
          link
          type="danger"
          size="small"
          data-test="nc-clear-secret"
          @click="onToggleClearSecret"
        >{{ clearSecret ? '撤销清空' : '清除密钥' }}</el-button>
      </el-form-item>

      <el-form-item label="请求体模板">
        <el-input
          v-model="form.template"
          type="textarea"
          :rows="3"
          class="mono-input"
          placeholder="留空 = 按类型使用内置模板"
          data-test="field-template"
        />
        <div class="hint" data-test="template-hint">{{ templateHint }}</div>
      </el-form-item>

      <el-form-item label="最低级别">
        <el-select v-model="form.min_level" data-test="field-min-level">
          <el-option v-for="opt in LEVEL_OPTIONS" :key="opt.value" :label="opt.label" :value="opt.value" />
        </el-select>
        <div class="hint">只有级别 ≥ 该值的通知才会投递到本通道。</div>
      </el-form-item>

      <el-form-item label="超时 (秒)">
        <el-input-number
          v-model="form.timeout_sec"
          :min="0"
          :controls="false"
          placeholder="留空 = 默认 10 秒"
          data-test="field-timeout"
        />
        <div class="hint">留空 = 用默认值；填 0 是**显式零值**（与留空语义不同）。</div>
      </el-form-item>

      <el-form-item label="最大重试次数">
        <el-input-number
          v-model="form.max_retries"
          :min="0"
          :controls="false"
          placeholder="留空 = 默认 2 次"
          data-test="field-max-retries"
        />
        <div class="hint">填 0 = 不重试（显式零值，不会被后端折叠成默认的 2 次）。</div>
      </el-form-item>

      <el-form-item label="启用">
        <el-switch v-model="form.enabled" data-test="field-enabled" />
      </el-form-item>

      <el-form-item label="允许内网地址">
        <el-switch v-model="form.allow_private" data-test="field-allow-private" />
        <div class="hint warn">
          默认关闭（SSRF 防护）。自建 OneBot（如 http://192.168.x.x:5700/send_msg）才需要打开；
          开启后该通道可向内网/回环地址发起请求。
        </div>
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button data-test="nc-cancel" @click="emit('update:visible', false)">取消</el-button>
      <el-button type="primary" :loading="submitting" data-test="nc-submit" @click="onSubmit">
        {{ isEdit ? '保存' : '创建' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import feedback from '@/utils/feedback'
import type {
  NotificationChannel,
  NotificationChannelCreatePayload,
  NotificationChannelType,
  NotificationChannelUpdatePayload,
  NotificationMinLevel,
} from '@/api/notificationChannel'

/**
 * 新建 / 编辑外发通知通道的表单对话框（设计 §8：同一个对话框组件）。
 *
 * 三条不能回退的契约：
 *
 * 1. **预设模板**（设计 §2.4「预设模板」表 + 后端 notify/template.go 的
 *    DefaultWeComTemplate / DefaultOneBotTemplate）：选「企业微信」预填 URL 前缀与
 *    markdown body；选「QQ 机器人(OneBot)」预填 /send_msg 与 message body。
 *    模板字符串**逐字符照抄后端常量** —— 前端编一套"看起来差不多"的模板，会在
 *    投递时被后端 json.Valid 拒绝或渲染出用户没预期的 body。
 *
 * 2. **secret 三态**（设计 §4 冻结 + §7.6）：
 *      留空且未点「清除密钥」 → 请求体**不含 secret 键**（= 不改，连 hint 都不动）；
 *      输入了内容             → 提交该内容（覆盖）；
 *      点「清除密钥」并保存   → 提交 `secret: ''`（后端明确把空串定义为清空）。
 *    因此本组件对 secret 的取值只有这三种来源，**secret_hint 永不参与**。
 *
 * 3. **零值语义**（§7.6）：timeout_sec / max_retries 留空发 null（未配置 → 应用层默认），
 *    填 0 发 0（显式零值），二者不可折叠。
 */

const props = withDefaults(defineProps<{
  visible: boolean
  /** null = 新建；非 null = 编辑该通道 */
  channel?: NotificationChannel | null
  submitting?: boolean
}>(), {
  channel: null,
  submitting: false,
})

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  /** 提交：payload 已经是"可直接发给后端"的形状（secret 键的有无即语义） */
  (e: 'submit', payload: NotificationChannelCreatePayload | NotificationChannelUpdatePayload): void
}>()

const isEdit = computed(() => props.channel !== null && props.channel !== undefined)

/**
 * 预设模板常量（设计 §2.4 表格 + backend/internal/notify/template.go:86-90 的逐字对应）。
 *
 * 企业微信：key 在 URL 里（后端 RedactTargetURL 会脱敏回显），body 是 markdown。
 * OneBot v11：/send_msg，body 是私聊形态（群聊把 user_id 换成 group_id）。
 * 通用 webhook：结构化 JSON，便于第三方自行解析。
 */
const URL_PREFIX: Record<NotificationChannelType, string> = {
  wecom: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=',
  onebot: 'http://<OneBot 主机>:5700/send_msg',
  webhook: '',
}

const TEMPLATE_PRESET: Record<NotificationChannelType, string> = {
  wecom: '{"msgtype":"markdown","markdown":{"content":{{printf "%q" (printf "**%s**\n%s" .Title .Message)}}}}',
  onebot: '{"user_id":0,"message":{{printf "%q" (printf "%s\n%s" .Title .Message)}},"auto_escape":false}',
  webhook: '{"title":{{printf "%q" .Title}},"message":{{printf "%q" .Message}},"level":"{{.Level}}","source":"{{.Source}}","source_id":"{{.SourceID}}","notification_id":{{.NotificationID}}}',
}

/**
 * 模板语法提示。写成脚本常量而不是模板内插值：'{{' 在 Vue 模板里是插值起始符，
 * 直接写 \\{\\{.Title}} 会被编译器当成表达式解析（实测报 Unterminated string constant）。
 */
const templateHint = 'Go text/template，可用 {{.Title}} / {{.Message}} / {{.Level}} / {{.Source}} 等占位符。'

const TYPE_OPTIONS: Array<{ value: NotificationChannelType; label: string }> = [
  { value: 'webhook', label: 'Webhook (通用 JSON)' },
  { value: 'wecom', label: '企业微信 (群机器人)' },
  { value: 'onebot', label: 'QQ 机器人 (OneBot v11)' },
]

/**
 * 最低级别选项**只有三级**：后端 notify.MinLevelOf（template.go:51）对未知值一律按
 * info 处理，而它只认 critical|warning|info。若这里给一个 "error" 选项，用户以为
 * "只收错误"，后端却把它归一化成 info（最宽松）—— 那是静默降级，本仓明令禁止。
 */
const LEVEL_OPTIONS: Array<{ value: NotificationMinLevel; label: string }> = [
  { value: 'info', label: '信息 (info) — 全部通知' },
  { value: 'warning', label: '警告 (warning) — 警告及以上' },
  { value: 'critical', label: '严重 (critical) — 仅严重' },
]

const form = reactive<{
  name: string
  type: NotificationChannelType
  target_url: string
  template: string
  min_level: NotificationMinLevel
  enabled: boolean
  timeout_sec: number | null
  max_retries: number | null
  allow_private: boolean
}>({
  name: '',
  type: 'webhook',
  target_url: '',
  template: '',
  min_level: 'info',
  enabled: true,
  timeout_sec: null,
  max_retries: null,
  allow_private: false,
})

/** 用户本次输入的密钥。空串 = 用户没输入（**不是**"清空密钥"，清空有独立的显式操作）。 */
const secretInput = ref('')
/** 用户是否显式点了「清除密钥」。只有它为 true 时提交空串。 */
const clearSecret = ref(false)

/**
 * 打开时载入表单。
 *
 * 注意 target_url：编辑态拿到的是**已脱敏**的地址（企业微信 key=***）。预填脱敏值
 * 是刻意的 —— 用户能看到"这里配过什么形态的地址"，若直接保存，后端会把 key 存成 '***'
 * （用户必须重填 key）。这是"只写不读"的必然代价，故 form 区有明确提示。
 */
watch(() => [props.visible, props.channel] as const, () => {
  if (!props.visible) return
  const c = props.channel
  secretInput.value = ''
  clearSecret.value = false
  if (!c) {
    form.name = ''
    form.type = 'webhook'
    form.target_url = ''
    form.template = ''
    form.min_level = 'info'
    form.enabled = true
    form.timeout_sec = null
    form.max_retries = null
    form.allow_private = false
    return
  }
  form.name = c.name
  form.type = c.type
  form.target_url = c.target_url
  form.template = c.template
  form.min_level = c.min_level === 'error' ? 'critical' : c.min_level
  form.enabled = c.enabled
  // null 原样保留（未配置 ≠ 0），绝不折叠成 0
  form.timeout_sec = c.timeout_sec
  form.max_retries = c.max_retries
  form.allow_private = c.allow_private
}, { immediate: true })

/** 用户一旦开始输入新密钥，"清除"意图即作废（避免"又清又填"的歧义状态）。 */
watch(secretInput, (v) => {
  if (v !== '') clearSecret.value = false
})

const secretPlaceholder = computed(() => {
  if (!isEdit.value) return '可留空（例如仅用 URL 认证的企业微信）'
  const c = props.channel
  if (c?.has_secret) return '留空 = 不修改现有密钥'
  return '尚未设置密钥'
})

/** 编辑态且（已设置密钥 或 已标记清除）才显示「清除密钥」——新建态没有可清的东西。 */
const canClearSecret = computed(() => isEdit.value && Boolean(props.channel?.has_secret || clearSecret.value))

/**
 * 密钥当前状态文案。**只用 has_secret / secret_hint 描述状态**，绝不把它当值使用。
 */
const secretStateText = computed(() => {
  const c = props.channel
  if (!isEdit.value) return '新建：留空表示暂不配置密钥。'
  if (clearSecret.value) {
    return c?.has_secret
      ? `保存后将清空已设置的密钥（当前末 4 位 ${c.secret_hint || '****'}）。`
      : '保存后将清空密钥。'
  }
  if (secretInput.value !== '') return '保存后将把密钥更新为本次输入的内容。'
  if (c?.has_secret) return `已设置（末 4 位 ${c.secret_hint || '****'}）；留空 = 不修改。`
  return '未设置密钥；留空 = 保持未设置。'
})

function onToggleClearSecret() {
  clearSecret.value = !clearSecret.value
  if (clearSecret.value) secretInput.value = ''
}

/**
 * 切换类型 ⇒ 预填预设模板（设计 §8 明文要求）。
 *
 * 只在"当前 URL/模板为空，或仍是另一个类型的预设值"时覆盖 —— 用户手改过的内容
 * 不该被一次误点类型下拉清掉。这不是"聪明"，而是防止**静默丢失用户输入**。
 */
function onTypeChange(next: NotificationChannelType) {
  const prevPresets = Object.values(URL_PREFIX)
  if (form.target_url.trim() === '' || prevPresets.includes(form.target_url)) {
    form.target_url = URL_PREFIX[next] ?? ''
  }
  const prevTemplates = Object.values(TEMPLATE_PRESET)
  if (form.template.trim() === '' || prevTemplates.includes(form.template)) {
    form.template = TEMPLATE_PRESET[next] ?? ''
  }
}

/** 空串 ⇒ null（未配置）；数字（含 0）原样 —— §7.6 的零值语义就靠这一行。 */
function normalizeOptionalInt(value: number | string | null | undefined): number | null {
  if (value === null || value === undefined || value === '') return null
  const n = Number(value)
  return Number.isFinite(n) ? n : null
}

/**
 * 组装请求体。
 *
 * secret 键的**有无**就是契约本身：
 *   - 输入了内容            → payload.secret = 输入值；
 *   - 编辑态 + 显式清除      → payload.secret = ''（后端：清空）；
 *   - 其余（留空 / 新建留空）→ **完全不出现 secret 键**。
 * 这里绝不写 `secret: secretInput.value || undefined` 之类"顺手简化"——
 * 那种写法把"留空 = 不改"变成"留空 = 清空"，会静默抹掉用户密钥（设计 §7.6 ②）。
 */
function buildPayload(): NotificationChannelCreatePayload | NotificationChannelUpdatePayload {
  const base: NotificationChannelCreatePayload = {
    name: form.name.trim(),
    type: form.type,
    target_url: form.target_url.trim(),
    template: form.template,
    min_level: form.min_level,
    enabled: form.enabled,
    timeout_sec: normalizeOptionalInt(form.timeout_sec),
    max_retries: normalizeOptionalInt(form.max_retries),
    allow_private: form.allow_private,
  }
  const secret = secretInput.value
  if (secret !== '') {
    return { ...base, secret }
  }
  if (isEdit.value && clearSecret.value) {
    return { ...base, secret: '' }
  }
  // 键不出现：留空 = 不修改（编辑）/ 不配置（新建）
  return base
}

function onSubmit() {
  if (form.name.trim() === '') {
    feedback.warning('请填写通道名称')
    return
  }
  if (form.target_url.trim() === '') {
    feedback.warning('请填写目标地址')
    return
  }
  emit('submit', buildPayload())
}
</script>

<style scoped>
.hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.5;
  width: 100%;
}
.hint.warn {
  color: var(--el-color-warning);
}
.mono-input :deep(textarea) {
  font-family: monospace;
}
</style>
