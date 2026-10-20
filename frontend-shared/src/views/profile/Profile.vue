<template>
  <div class="profile-page">
    <PageHeader title="个人设置" />

    <el-row :gutter="20">
      <el-col :xs="24" :sm="12" :md="8">
        <el-card class="info-card">
          <div class="avatar-block">
            <el-avatar :size="80" :icon="UserFilled" />
            <h3 class="username">{{ userInfo?.username || '—' }}</h3>
            <el-tag type="primary" size="default">系统管理员</el-tag>
            <p v-if="userInfo?.email" class="email">{{ userInfo.email }}</p>
          </div>
        </el-card>
      </el-col>

      <el-col :xs="24" :sm="12" :md="16">
        <el-card>
          <template #header>
            <span>修改密码</span>
          </template>
          <el-form ref="formRef" :model="form" :rules="rules" label-width="100px" :label-position="isMobile ? 'top' : 'right'" style="max-width: 480px">
            <el-form-item label="当前密码" prop="old_password">
              <el-input v-model="form.old_password" type="password" show-password placeholder="请输入当前密码" />
            </el-form-item>
            <el-form-item label="新密码" prop="new_password">
              <!-- 2026-10-21 审计 C-ops P2：placeholder 改前与下方 .form-tip 文案重复（「至少 8 位字符」
               同时出现在两处）。规则说明统一留给 form-tip，placeholder 只提示"要填什么"。 -->
              <el-input v-model="form.new_password" type="password" show-password placeholder="请输入新密码" />
              <div class="form-tip">至少 8 位字符，且不能与当前密码相同</div>
            </el-form-item>
            <el-form-item label="确认新密码" prop="confirm_password">
              <el-input v-model="form.confirm_password" type="password" show-password placeholder="请再次输入新密码" />
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
import { useResponsive } from '@/composables/useResponsive'
import feedback from '@/utils/feedback'
import { loginPath } from '@/utils/basePath'

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
      window.location.href = loginPath()
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
.form-tip {
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.4;
  color: var(--text-color-secondary);
}

/* 2026-10-21 审计 C-ops P2：桌面下信息卡 235px / 改密卡 323.8px（差 89px）参差。
   el-col 作为 flex 项会被拉齐，但内部 el-card 不会自动继承高度 ⇒ 让卡片撑满列高。 */
.profile-page :deep(.el-row) {
  align-items: stretch;
}
.profile-page :deep(.el-col) {
  display: flex;
}
.profile-page :deep(.el-col > .el-card) {
  flex: 1 1 auto;
  width: 100%;
}

@media (max-width: 768px) {
  /* 2026-10-21 审计 C-ops P2：改前这里追加 padding:0 12px（叠加 main 的 20px），
     卡片 x=32 / 宽 321，与其它 5 页（x=20 / 宽 345）不一致。水平 padding 交还 main，
     只保留纵向间距。 */
  .profile-page {
    padding: 0 0 16px;
  }
  .profile-page .el-col {
    margin-bottom: 16px;
  }
  /* 移动端 label-on-top 通过 el-form :label-position 响应式绑定实现（label-width 在 top 模式下自动失效） */
}
</style>
