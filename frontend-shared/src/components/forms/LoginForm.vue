<template>
  <el-form
    ref="formRef"
    :model="form"
    :rules="rules"
    label-position="top"
    @submit.prevent="handleSubmit"
  >
    <el-form-item label="用户名" prop="username">
      <el-input
        v-model="form.username"
        placeholder="请输入用户名"
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
        <el-checkbox v-model="form.rememberMe" label="记住我" :disabled="disabled" />
        <!-- 忘记密码入口（占位，指向管理员联系提示） -->
        <el-link type="primary" :underline="false" @click="showForgotTip = true">忘记密码？</el-link>
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
          ehome-cli user reset-password --username &lt;用户名&gt;</pre>
      </div>
    </el-dialog>
  </el-form>
</template>

<script setup lang="ts">
import { ref, reactive } from 'vue'
import { User, Lock } from '@element-plus/icons-vue'
import type { FormInstance, FormRules } from 'element-plus'

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

const form = reactive({
  username: '',
  password: '',
  rememberMe: false,
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
