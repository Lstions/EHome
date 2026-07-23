<template>
  <div class="profile-page">
    <PageHeader title="个人设置" />

    <el-row :gutter="20">
      <el-col :xs="24" :sm="24" :md="8">
        <el-card class="info-card">
          <div class="avatar-block">
            <el-avatar :size="80" :icon="UserFilled" />
            <h3 class="username">{{ userInfo?.username || '—' }}</h3>
            <el-tag type="danger" size="default">系统管理员</el-tag>
            <p class="email">{{ userInfo?.email || '未设置邮箱' }}</p>
          </div>
        </el-card>
      </el-col>

      <el-col :xs="24" :sm="24" :md="16">
        <el-card>
          <template #header>
            <span>修改密码</span>
          </template>
          <el-form
            ref="formRef"
            :model="form"
            :rules="rules"
            label-width="100px"
            :label-position="isMobile ? 'top' : 'right'"
            class="password-form"
          >
            <el-form-item label="当前密码" prop="old_password">
              <el-input v-model="form.old_password" type="password" show-password />
            </el-form-item>
            <el-form-item label="新密码" prop="new_password">
              <el-input v-model="form.new_password" type="password" show-password />
            </el-form-item>
            <el-form-item label="确认新密码" prop="confirm_password">
              <el-input v-model="form.confirm_password" type="password" show-password />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" :loading="submitting" @click="handleChangePassword">提交</el-button>
              <el-button @click="formRef?.resetFields()">重置</el-button>
            </el-form-item>
          </el-form>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed } from 'vue'
import { UserFilled } from '@element-plus/icons-vue'
import type { FormInstance, FormRules } from 'element-plus'
import PageHeader from '@/components/common/PageHeader.vue'
import { useUserStore } from '@/stores/user'
import { authApi } from '@/api/auth'
import feedback from '@/utils/feedback'
import { useResponsive } from '@/composables/useResponsive'

const userStore = useUserStore()
const userInfo = computed(() => userStore.userInfo)
const { isMobile } = useResponsive()


const formRef = ref<FormInstance>()
const submitting = ref(false)
const form = reactive({
  old_password: '',
  new_password: '',
  confirm_password: '',
})

const rules: FormRules = {
  old_password: [{ required: true, message: '请输入当前密码', trigger: 'blur' }],
  new_password: [
    { required: true, message: '请输入新密码', trigger: 'blur' },
    { min: 8, message: '至少 8 位', trigger: 'blur' },
  ],
  confirm_password: [
    { required: true, message: '请再次输入新密码', trigger: 'blur' },
    {
      validator: (_rule, value, callback) => {
        if (value !== form.new_password) {
          callback(new Error('两次输入的密码不一致'))
        } else {
          callback()
        }
      },
      trigger: 'blur',
    },
  ],
}

const handleChangePassword = async () => {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  submitting.value = true
  try {
    await authApi.changePassword({
      old_password: form.old_password,
      new_password: form.new_password,
    })
    feedback.success('密码修改成功，请重新登录')
    setTimeout(async () => {
      await userStore.logout()
      window.location.href = '/login'
    }, 1500)
  } catch (err) {
    feedback.handleError(err)
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.profile-page {
  padding: 0 24px 24px;
}
.info-card {
  text-align: center;
}
.avatar-block {
  padding: 24px 0;
}
.username {
  margin: 12px 0 8px;
  font-size: 18px;
  color: var(--text-color-primary);
}
.email {
  margin: 12px 0 0;
  font-size: 13px;
  color: var(--text-color-secondary);
}
</style>
