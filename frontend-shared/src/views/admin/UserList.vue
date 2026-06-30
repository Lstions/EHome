<template>
  <div class="user-list-page">
    <PageHeader title="用户管理">
      <template #extra>
        <el-button type="primary" :icon="Plus" v-permission="'admin'" @click="openCreateDialog">新建用户</el-button>
      </template>
    </PageHeader>

    <!-- 筛选 -->
    <el-card class="filter-card">
      <el-form :inline="true" :model="filters" @submit.prevent="fetchList">
        <el-form-item label="关键词">
          <el-input v-model="filters.keyword" placeholder="用户名/邮箱" clearable @keyup.enter="fetchList" />
        </el-form-item>
        <el-form-item label="角色">
          <el-select v-model="filters.role" placeholder="全部" clearable style="width: 140px">
            <el-option label="管理员" value="admin" />
            <el-option label="操作员" value="operator" />
            <el-option label="观察者" value="viewer" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :icon="Search" @click="fetchList">查询</el-button>
          <el-button :icon="Refresh" @click="resetFilters">重置</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 列表 -->
    <el-card class="table-card">
      <el-table v-loading="loading" :data="list" stripe>
        <el-table-column label="ID" prop="id" width="80" />
        <el-table-column label="用户名" prop="username" min-width="120" />
        <el-table-column label="邮箱" prop="email" min-width="180" show-overflow-tooltip />
        <el-table-column label="角色" width="100">
          <template #default="{ row }">
            <el-tag :type="roleTagType(row.role)" size="small">{{ roleLabel(row.role) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
              {{ row.enabled ? '启用' : '禁用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" prop="created_at" width="180" />
        <el-table-column label="最后登录" prop="last_login_at" width="180">
          <template #default="{ row }">
            <span v-if="row.last_login_at">{{ formatTime(row.last_login_at) }}</span>
            <span v-else class="text-muted">从未</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right" v-permission="'admin'">
          <template #default="{ row }">
            <el-button text type="primary" size="small" @click="openEditDialog(row)">编辑</el-button>
            <el-button text type="primary" size="small" @click="openResetPasswordDialog(row)">重置密码</el-button>
            <el-button text type="danger" size="small" :disabled="row.id === currentUserId" @click="handleDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div v-if="total > pageSize" class="pagination-wrapper">
        <el-pagination
          v-model:current-page="filters.page"
          v-model:page-size="filters.page_size"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next, jumper"
          background
          @current-change="fetchList"
          @size-change="fetchList"
        />
      </div>
    </el-card>

    <!-- 创建/编辑对话框 -->
    <el-dialog
      v-model="dialogVisible"
      :title="editingUser ? '编辑用户' : '新建用户'"
      width="500px"
      @closed="resetFormState"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="80px">
        <el-form-item label="用户名" prop="username" v-if="!editingUser">
          <el-input v-model="form.username" placeholder="3-32 个字符" />
        </el-form-item>
        <el-form-item label="密码" prop="password" v-if="!editingUser">
          <el-input v-model="form.password" type="password" show-password placeholder="至少 6 位" />
        </el-form-item>
        <el-form-item label="邮箱" prop="email">
          <el-input v-model="form.email" placeholder="可选" />
        </el-form-item>
        <el-form-item label="角色" prop="role">
          <el-select v-model="form.role" style="width: 100%">
            <el-option label="管理员" value="admin">
              <div class="role-option">
                <span>管理员</span>
                <span class="role-desc">全部权限</span>
              </div>
            </el-option>
            <el-option label="操作员" value="operator">
              <div class="role-option">
                <span>操作员</span>
                <span class="role-desc">固件/配置/通道, 不可见系统监控</span>
              </div>
            </el-option>
            <el-option label="观察者" value="viewer">
              <div class="role-option">
                <span>观察者</span>
                <span class="role-desc">只读</span>
              </div>
            </el-option>
          </el-select>
        </el-form-item>
        <el-form-item label="启用" v-if="editingUser">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="handleSubmit">确定</el-button>
      </template>
    </el-dialog>

    <!-- 重置密码对话框 -->
    <el-dialog v-model="resetDialogVisible" title="重置密码" width="400px">
      <el-form ref="resetFormRef" :model="resetForm" :rules="resetRules" label-width="100px">
        <el-form-item label="目标用户">
          <el-tag>{{ resetTarget?.username }}</el-tag>
        </el-form-item>
        <el-form-item label="新密码" prop="new_password">
          <el-input v-model="resetForm.new_password" type="password" show-password placeholder="至少 6 位" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="resetDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="resetting" @click="handleResetPassword">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import { Plus, Search, Refresh } from '@element-plus/icons-vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import PageHeader from '@/components/common/PageHeader.vue'
import { userApi, type UserListItem, type CreateUserParams, type UpdateUserParams } from '@/api/user'
import { useUserStore } from '@/stores/user'
import { formatTime } from '@/utils/format'
import feedback from '@/utils/feedback'

const userStore = useUserStore()
const loading = ref(false)
const submitting = ref(false)
const resetting = ref(false)
const list = ref<UserListItem[]>([])
const total = ref(0)
const pageSize = computed(() => filters.page_size)

const currentUserId = computed(() => userStore.userInfo?.id)

const filters = reactive({
  page: 1,
  page_size: 20,
  keyword: '',
  role: undefined as '' | 'admin' | 'operator' | 'viewer' | undefined,
})

const dialogVisible = ref(false)
const editingUser = ref<UserListItem | null>(null)
const formRef = ref<FormInstance>()
const form = reactive<CreateUserParams & { enabled: boolean }>({
  username: '',
  password: '',
  email: '',
  role: 'viewer',
  enabled: true,
})

const rules: FormRules = {
  username: [
    { required: true, message: '请输入用户名', trigger: 'blur' },
    { min: 3, max: 32, message: '长度 3-32 字符', trigger: 'blur' },
  ],
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 6, message: '至少 6 位', trigger: 'blur' },
  ],
  email: [
    { type: 'email', message: '邮箱格式不正确', trigger: 'blur' },
  ],
  role: [
    { required: true, message: '请选择角色', trigger: 'change' },
  ],
}

const resetDialogVisible = ref(false)
const resetTarget = ref<UserListItem | null>(null)
const resetFormRef = ref<FormInstance>()
const resetForm = reactive({ new_password: '' })
const resetRules: FormRules = {
  new_password: [
    { required: true, message: '请输入新密码', trigger: 'blur' },
    { min: 6, message: '至少 6 位', trigger: 'blur' },
  ],
}

const roleLabel = (role: string) => {
  const map: Record<string, string> = { admin: '管理员', operator: '操作员', viewer: '观察者' }
  return map[role] || role
}
const roleTagType = (role: string): 'danger' | 'warning' | 'info' => {
  if (role === 'admin') return 'danger'
  if (role === 'operator') return 'warning'
  return 'info'
}

const fetchList = async () => {
  loading.value = true
  try {
    const res = await userApi.list({
      page: filters.page,
      page_size: filters.page_size,
      keyword: filters.keyword || undefined,
      role: filters.role || undefined,
    })
    // 兼容 {code, data} 信封与裸对象
    const data = (res as any).data?.list ? (res as any).data : (res as any)
    list.value = data.list || data.items || []
    total.value = data.total || 0
  } catch (err) {
    feedback.handleError(err, '获取用户列表失败')
  } finally {
    loading.value = false
  }
}

const resetFilters = () => {
  filters.keyword = ''
  filters.role = undefined
  filters.page = 1
  fetchList()
}

const openCreateDialog = () => {
  editingUser.value = null
  form.username = ''
  form.password = ''
  form.email = ''
  form.role = 'viewer'
  form.enabled = true
  dialogVisible.value = true
}

const openEditDialog = (row: UserListItem) => {
  editingUser.value = row
  form.email = row.email
  form.role = row.role
  form.enabled = row.enabled
  dialogVisible.value = true
}

const handleSubmit = async () => {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  submitting.value = true
  try {
    if (editingUser.value) {
      const data: UpdateUserParams = {
        email: form.email,
        role: form.role,
        enabled: form.enabled,
      }
      await userApi.update(editingUser.value.id, data)
      feedback.success('已更新')
    } else {
      await userApi.create({
        username: form.username,
        password: form.password,
        email: form.email,
        role: form.role,
      })
      feedback.success('已创建')
    }
    dialogVisible.value = false
    fetchList()
  } catch (err) {
    feedback.handleError(err)
  } finally {
    submitting.value = false
  }
}

const resetFormState = () => {
  formRef.value?.resetFields()
  editingUser.value = null
}

const openResetPasswordDialog = (row: UserListItem) => {
  resetTarget.value = row
  resetForm.new_password = ''
  resetDialogVisible.value = true
}

const handleResetPassword = async () => {
  if (!resetFormRef.value || !resetTarget.value) return
  const valid = await resetFormRef.value.validate().catch(() => false)
  if (!valid) return
  resetting.value = true
  try {
    await userApi.resetPassword(resetTarget.value.id, resetForm.new_password)
    feedback.success(`已重置 ${resetTarget.value.username} 的密码`)
    resetDialogVisible.value = false
  } catch (err) {
    feedback.handleError(err)
  } finally {
    resetting.value = false
  }
}

const handleDelete = async (row: UserListItem) => {
  const ok = await feedback.confirmDanger(
    `确认删除用户 "${row.username}"？该操作不可撤销。`,
    { title: '删除用户', confirmText: '删除' },
  )
  if (!ok) return
  try {
    await userApi.delete(row.id)
    feedback.success('已删除')
    fetchList()
  } catch (err) {
    feedback.handleError(err)
  }
}

onMounted(() => {
  fetchList()
})
</script>

<style scoped>
.user-list-page {
  padding: 0 24px 24px;
}
.filter-card,
.table-card {
  margin-bottom: 16px;
}
.pagination-wrapper {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}
.role-option {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.role-desc {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.text-muted {
  color: var(--el-text-color-placeholder);
  font-size: 12px;
}
</style>
