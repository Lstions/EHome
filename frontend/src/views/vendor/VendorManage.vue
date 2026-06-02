<template>
  <div class="vendor-manage">
    <div class="page-header">
      <h2>厂商管理</h2>
      <el-button type="primary" @click="showAddDialog">
        <el-icon><Plus /></el-icon>
        添加厂商
      </el-button>
    </div>

    <el-card>
      <el-table :data="vendors" v-loading="loading" stripe>
        <el-table-column prop="id" label="ID" width="80" />
        <el-table-column prop="name" label="厂商名称" />
        <el-table-column prop="code" label="厂商代码" />
        <el-table-column prop="contact" label="联系人" />
        <el-table-column prop="phone" label="联系电话" />
        <el-table-column prop="email" label="邮箱" />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '启用' : '禁用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="200">
          <template #default="{ row }">
            <el-button size="small" @click="showEditDialog(row)">编辑</el-button>
            <el-button size="small" type="danger" @click="handleDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pagination">
        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next"
          @size-change="loadVendors"
          @current-change="loadVendors"
        />
      </div>
    </el-card>

    <!-- 添加/编辑对话框 -->
    <el-dialog
      v-model="dialogVisible"
      :title="isEdit ? '编辑厂商' : '添加厂商'"
      width="500px"
    >
      <el-form :model="form" label-width="100px">
        <el-form-item label="厂商名称" required>
          <el-input v-model="form.name" placeholder="请输入厂商名称" />
        </el-form-item>
        <el-form-item label="厂商代码" required>
          <el-input v-model="form.code" placeholder="请输入厂商代码" />
        </el-form-item>
        <el-form-item label="联系人">
          <el-input v-model="form.contact" placeholder="请输入联系人" />
        </el-form-item>
        <el-form-item label="联系电话">
          <el-input v-model="form.phone" placeholder="请输入联系电话" />
        </el-form-item>
        <el-form-item label="邮箱">
          <el-input v-model="form.email" placeholder="请输入邮箱" />
        </el-form-item>
        <el-form-item label="地址">
          <el-input v-model="form.address" type="textarea" placeholder="请输入地址" />
        </el-form-item>
        <el-form-item label="状态">
          <el-switch v-model="form.enabled" active-text="启用" inactive-text="禁用" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSubmit" :loading="submitting">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import axios from 'axios'

interface Vendor {
  id: number
  name: string
  code: string
  contact: string
  phone: string
  email: string
  address: string
  enabled: boolean
}

const vendors = ref<Vendor[]>([])
const loading = ref(false)
const currentPage = ref(1)
const pageSize = ref(20)
const total = ref(0)

const dialogVisible = ref(false)
const isEdit = ref(false)
const submitting = ref(false)
const editingId = ref<number | null>(null)

const form = ref({
  name: '',
  code: '',
  contact: '',
  phone: '',
  email: '',
  address: '',
  enabled: true
})

const loadVendors = async () => {
  loading.value = true
  try {
    const res = await axios.get('/api/v1/vendors', {
      params: { page: currentPage.value, page_size: pageSize.value }
    })
    vendors.value = res.data.data || []
    total.value = res.data.total || 0
  } catch (error) {
    ElMessage.error('加载厂商列表失败')
  } finally {
    loading.value = false
  }
}

const showAddDialog = () => {
  isEdit.value = false
  editingId.value = null
  form.value = { name: '', code: '', contact: '', phone: '', email: '', address: '', enabled: true }
  dialogVisible.value = true
}

const showEditDialog = (row: Vendor) => {
  isEdit.value = true
  editingId.value = row.id
  form.value = { ...row }
  dialogVisible.value = true
}

const handleSubmit = async () => {
  submitting.value = true
  try {
    if (isEdit.value && editingId.value) {
      await axios.put(`/api/v1/vendors/${editingId.value}`, form.value)
      ElMessage.success('更新成功')
    } else {
      await axios.post('/api/v1/vendors', form.value)
      ElMessage.success('添加成功')
    }
    dialogVisible.value = false
    loadVendors()
  } catch (error) {
    ElMessage.error('操作失败')
  } finally {
    submitting.value = false
  }
}

const handleDelete = async (row: Vendor) => {
  try {
    await ElMessageBox.confirm('确定要删除该厂商吗？', '提示', { type: 'warning' })
    await axios.delete(`/api/v1/vendors/${row.id}`)
    ElMessage.success('删除成功')
    loadVendors()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error('删除失败')
    }
  }
}

onMounted(() => {
  loadVendors()
})
</script>

<style scoped>
.vendor-manage {
  padding: 20px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.pagination {
  margin-top: 20px;
  display: flex;
  justify-content: flex-end;
}
</style>
