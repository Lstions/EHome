<template>
  <el-form
    ref="formRef"
    :model="form"
    :rules="rules"
    label-position="top"
    @submit.prevent="handleSubmit"
  >
    <!--
      autocomplete 必须显式声明（Element Plus 默认给 el-input 下发 autocomplete="off"）：
      登录表单是浏览器密码管理器的**主战场**，"off" 会让浏览器既不保存也不回填，
      与「记住我」的语义直接冲突。这里按 HTML 规范给语义值：
        · username        → 浏览器识别为账号字段
        · current-password→ 登录场景（而非 new-password 的注册/改密场景）
      这两项与下面的应用内「记住我」互补：应用内记住的是"勾选+用户名"，
      浏览器记住的是"凭据"，任一侧生效都能减少重复输入。
    -->
    <el-form-item label="用户名" prop="username">
      <el-input
        v-model="form.username"
        placeholder="请输入用户名"
        autocomplete="username"
        name="username"
        :prefix-icon="User"
        clearable
        size="large"
        :disabled="disabled"
        @keyup.enter="handleSubmit"
      />
    </el-form-item>

    <el-form-item label="密码" prop="password">
      <el-input
        v-model="form.password"
        type="password"
        placeholder="请输入密码"
        autocomplete="current-password"
        name="password"
        :prefix-icon="Lock"
        show-password
        clearable
        size="large"
        :disabled="disabled"
        @keyup.enter="handleSubmit"
      />
    </el-form-item>

    <el-form-item>
      <div style="display: flex; justify-content: space-between; width: 100%; align-items: center;">
        <!--
          ⚠ 不要写 label="记住我"：Element Plus 2.6+ 起 label 作**显示文本**已弃用
          （它的语义变成了"选中值"），控制台会报弃用告警且未来版本会改变行为。
          显示文本走默认插槽。
        -->
        <el-checkbox v-model="form.rememberMe" name="rememberMe" :disabled="disabled">记住我</el-checkbox>
        <!-- 忘记密码入口（占位，指向管理员联系提示） -->
        <!-- Element Plus 2.14.3：underline 的布尔形态已弃用（触发 ElementPlusError 告警）。
             `:underline="false"` 与 `underline="never"` 语义等价（link.vue:34-36 的
             isBoolean 分支把 false 映射为 "never"），改为字符串字面量以消除告警且不改视觉。 -->
        <el-link type="primary" underline="never" @click="showForgotTip = true">忘记密码？</el-link>
      </div>
    </el-form-item>

    <el-form-item>
      <el-button
        type="primary"
        :loading="loading"
        :disabled="disabled"
        size="large"
        style="width: 100%;"
        @click="handleSubmit"
      >
        {{ loading ? '登录中...' : '登 录' }}
      </el-button>
    </el-form-item>

    <!-- 忘记密码提示 -->
    <el-dialog v-model="showForgotTip" title="忘记密码" width="400px" append-to-body>
      <div style="line-height: 1.8; color: var(--el-text-color-regular);">
        <p>请联系系统管理员重置密码。</p>
        <p>管理员可通过后端命令行工具执行：</p>
        <pre style="background: var(--el-fill-color-light); padding: 12px 16px; border-radius: 4px; font-family: monospace; font-size: 13px; color: var(--el-text-color-primary); margin: 12px 0 0;">
          EHOME_NEW_ADMIN_PASSWORD='&lt;新密码&gt;' ehomectl auth reset-password</pre>
      </div>
    </el-dialog>
  </el-form>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { User, Lock } from '@element-plus/icons-vue'
import type { FormInstance, FormRules } from 'element-plus'
import { isRemembered, getRememberedUsername } from '@/utils/loginPreference'

defineProps<{
  disabled?: boolean
}>()

const emit = defineEmits<{
  (e: 'success', username: string, password: string, rememberMe: boolean): void
  (e: 'error', error: any): void
}>()

const formRef = ref<FormInstance>()
const loading = ref(false)
const showForgotTip = ref(false)

/**
 * 表单初值**直接取自已持久化的偏好**（而不是先渲染空表单再 onMounted 里改）：
 * 后者会让用户先看到空输入框再"闪"出用户名，且在慢设备上会和用户输入竞争。
 * 初始化失败（存储被禁用）时函数自身已兜底返回 false/''。
 */
const form = reactive({
  username: getRememberedUsername(),
  password: '',
  rememberMe: isRemembered(),
})

// 保持与 localStorage 的最终一致性：组件挂载时再对齐一次。
// 为什么 reactive 初值之外还要这一步：form 是模块级 reactive，若同一页面里
// 组件被销毁重建（如切换 authState），初值不会再执行。正常流程两者一致，
// 这一步只兜"外部改过偏好"的边界，不覆盖用户已输入的密码。
onMounted(() => {
  if (!form.username) form.username = getRememberedUsername()
  form.rememberMe = isRemembered()
})

const rules: FormRules = {
  username: [
    { required: true, message: '请输入用户名', trigger: 'blur' },
  ],
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 6, message: '密码长度至少6位', trigger: 'blur' },
  ],
}

const handleSubmit = async () => {
  if (!formRef.value) return

  await formRef.value.validate((valid) => {
    if (valid) {
      loading.value = true
      emit('success', form.username, form.password, form.rememberMe)
    }
  })
}

const resetForm = () => {
  formRef.value?.resetFields()
}

defineExpose({
  resetForm,
  setLoading: (val: boolean) => {
    loading.value = val
  },
})
</script>
