<template>
  <el-form
    ref="formRef"
    :model="form"
    :rules="rules"
    label-width="80px"
    @submit.prevent="handleSubmit"
  >
    <el-form-item label="用户名" prop="username">
      <el-input
        v-model="form.username"
        placeholder="请输入用户名"
        clearable
        style="height: 40px;"
        @keyup.enter="handleSubmit"
      />
    </el-form-item>

    <el-form-item label="密码" prop="password">
      <el-input
        v-model="form.password"
        type="password"
        placeholder="请输入密码"
        show-password
        clearable
        style="height: 40px;"
        @keyup.enter="handleSubmit"
      />
    </el-form-item>

    <el-form-item>
      <el-checkbox v-model="form.rememberMe" label="记住我" />
    </el-form-item>

    <el-form-item>
      <el-button
        type="primary"
        :loading="loading"
        style="width: 100%; height: 40px;"
        @click="handleSubmit"
      >
        {{ loading ? '登录中...' : '登录' }}
      </el-button>
    </el-form-item>
  </el-form>
</template>

<script setup lang="ts">
import { ref, reactive } from 'vue'
import type { FormInstance, FormRules } from 'element-plus'

const emit = defineEmits<{
  (e: 'success', username: string, password: string, rememberMe: boolean): void
  (e: 'error', error: any): void
}>()

const formRef = ref<FormInstance>()
const loading = ref(false)

const form = reactive({
  username: '',
  password: '',
  rememberMe: false
})

const rules: FormRules = {
  username: [
    { required: true, message: '请输入用户名', trigger: 'blur' }
  ],
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 6, message: '密码长度至少6位', trigger: 'blur' }
  ]
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
  }
})
</script>
