<template>
  <el-form
    ref="formRef"
    class="initialize-form"
    :model="form"
    :rules="rules"
    label-position="top"
    @submit.prevent="handleSubmit"
  >
    <div class="setup-intro">
      <h2>首次运行设置</h2>
      <p>请创建系统管理员账号。完成后即可使用新账号登录。</p>
    </div>

    <el-form-item label="初始化凭据" prop="credential">
      <el-input
        v-model="form.credential"
        placeholder="粘贴 make up 输出的 selector.secret"
        :prefix-icon="Key"
        autocomplete="off"
        clearable
        size="large"
        :disabled="disabled"
      />
      <div class="field-help">凭据有效期为 10 分钟，且只能使用一次。</div>
    </el-form-item>

    <el-form-item label="管理员用户名" prop="username">
      <el-input
        v-model="form.username"
        placeholder="请输入管理员用户名"
        :prefix-icon="User"
        autocomplete="username"
        clearable
        size="large"
        :disabled="disabled"
      />
    </el-form-item>

    <el-form-item label="管理员密码" prop="password">
      <el-input
        v-model="form.password"
        type="password"
        placeholder="至少 8 位"
        :prefix-icon="Lock"
        autocomplete="new-password"
        show-password
        clearable
        size="large"
        :disabled="disabled"
      />
    </el-form-item>

    <el-form-item label="确认密码" prop="confirmPassword">
      <el-input
        v-model="form.confirmPassword"
        type="password"
        placeholder="再次输入管理员密码"
        :prefix-icon="Lock"
        autocomplete="new-password"
        show-password
        clearable
        size="large"
        :disabled="disabled"
        @keyup.enter="handleSubmit"
      />
    </el-form-item>

    <el-form-item label="邮箱（可选）" prop="email">
      <el-input
        v-model="form.email"
        placeholder="用于账号联系"
        :prefix-icon="Message"
        autocomplete="email"
        clearable
        size="large"
        :disabled="disabled"
      />
    </el-form-item>

    <el-form-item>
      <el-button
        type="primary"
        :loading="loading"
        :disabled="disabled"
        size="large"
        class="submit-button"
        @click="handleSubmit"
      >
        {{ loading ? '创建中...' : '创建管理员并继续' }}
      </el-button>
    </el-form-item>
  </el-form>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { Key, Lock, Message, User } from '@element-plus/icons-vue'
import type { FormInstance, FormRules } from 'element-plus'
import type { InitializeRequest } from '@/api/auth'

defineProps<{
  disabled?: boolean
}>()

const emit = defineEmits<{
  (event: 'submit', data: InitializeRequest): void
}>()

const formRef = ref<FormInstance>()
const loading = ref(false)
const form = reactive({
  credential: '',
  username: '',
  password: '',
  confirmPassword: '',
  email: '',
})

const validateConfirmPassword = (_rule: unknown, value: string, callback: (error?: Error) => void) => {
  if (value !== form.password) {
    callback(new Error('两次输入的密码不一致'))
    return
  }
  callback()
}

const rules: FormRules = {
  credential: [{ required: true, message: '请输入初始化凭据', trigger: 'blur' }],
  username: [{ required: true, message: '请输入管理员用户名', trigger: 'blur' }],
  password: [
    { required: true, message: '请输入管理员密码', trigger: 'blur' },
    { min: 8, message: '管理员密码至少 8 位', trigger: 'blur' },
  ],
  confirmPassword: [
    { required: true, message: '请确认管理员密码', trigger: 'blur' },
    { validator: validateConfirmPassword, trigger: 'blur' },
  ],
  email: [{ type: 'email', message: '请输入有效的邮箱地址', trigger: 'blur' }],
}

const handleSubmit = async () => {
  if (!formRef.value) return

  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  loading.value = true
  emit('submit', {
    credential: form.credential.trim(),
    username: form.username.trim(),
    password: form.password,
    email: form.email.trim() || undefined,
  })
}

const resetForm = () => {
  formRef.value?.resetFields()
}

defineExpose({
  resetForm,
  setLoading: (value: boolean) => {
    loading.value = value
  },
})
</script>

<style scoped>
.initialize-form {
  width: 100%;
}

.setup-intro {
  margin-bottom: 22px;
}

.setup-intro h2 {
  margin: 0;
  color: var(--text-color-primary);
  font-size: 22px;
}

.setup-intro p {
  margin: 8px 0 0;
  color: var(--el-text-color-secondary);
  font-size: 13px;
  line-height: 1.6;
}

.field-help {
  margin-top: 5px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
}

.submit-button {
  width: 100%;
}
</style>
