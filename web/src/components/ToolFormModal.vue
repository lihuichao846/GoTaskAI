<script setup>
import { reactive, ref, watch } from 'vue'
import { createTool, testToolConnection, updateTool } from '../api/client'

const props = defineProps({
  token: { type: String, default: '' },
  open: { type: Boolean, default: false },
  mode: { type: String, default: 'create' }, // create / edit
  initial: { type: Object, default: () => ({}) }
})

const emit = defineEmits(['close', 'saved', 'message'])

const form = reactive({ name: '', display_name: '', description: '', config: '' })
const submitting = ref(false)
const testing = ref(false)
const testResult = ref(null)

watch(
  () => [props.open, props.initial],
  ([open]) => {
    if (!open) return
    form.name = props.initial.name || ''
    form.display_name = props.initial.display_name || ''
    form.description = props.initial.description || ''
    form.config = props.initial.config || ''
    testResult.value = null
  },
  { immediate: true }
)

function handleClose() {
  emit('close')
}

async function testConnection() {
  const cfg = form.config.trim()
  if (!cfg) {
    emit('message', '请先填写连接配置', 'error')
    return
  }
  try {
    JSON.parse(cfg)
  } catch {
    emit('message', '连接配置不是合法 JSON', 'error')
    return
  }
  testing.value = true
  testResult.value = null
  try {
    const res = await testToolConnection(props.token, cfg)
    testResult.value = res
    if (res.ok) {
      emit('message', `连接成功，服务端暴露 ${(res.tools || []).length} 个工具`, 'success')
    } else {
      emit('message', `连接失败：${res.error}`, 'error')
    }
  } catch (e) {
    emit('message', `测试失败：${e.message}`, 'error')
  } finally {
    testing.value = false
  }
}

async function submit() {
  if (!form.name.trim() || !form.config.trim()) {
    emit('message', '请填写工具名称与连接配置', 'error')
    return
  }
  try {
    JSON.parse(form.config.trim())
  } catch {
    emit('message', '连接配置不是合法 JSON', 'error')
    return
  }

  const payload = {
    name: form.name.trim(),
    type: 'mcp',
    display_name: form.display_name.trim(),
    description: form.description.trim(),
    config: form.config.trim()
  }

  submitting.value = true
  try {
    if (props.mode === 'edit') {
      await updateTool(props.token, props.initial.id, payload)
      emit('message', '工具已更新', 'success')
    } else {
      await createTool(props.token, payload)
      emit('message', '工具注册成功', 'success')
    }
    emit('saved')
    emit('close')
  } catch (e) {
    emit('message', `${props.mode === 'edit' ? '工具更新失败' : '工具注册失败'}：${e.message}`, 'error')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div v-if="open" class="modal-overlay" @click.self="handleClose">
    <div class="modal-dialog">
      <div class="modal-head">
        <h3>{{ mode === 'edit' ? '编辑工具' : '注册 MCP 工具' }}</h3>
        <button class="modal-close" @click="handleClose">✕</button>
      </div>

      <div class="auth-form">
        <label>
          <span>工具名称（唯一）</span>
          <input v-model="form.name" type="text" placeholder="例如：my_mcp_tool" />
        </label>
        <label>
          <span>显示名称</span>
          <input v-model="form.display_name" type="text" placeholder="例如：联网搜索" />
        </label>
        <label>
          <span>描述</span>
          <input v-model="form.description" type="text" placeholder="说明工具用途" />
        </label>
        <label>
          <span>连接配置（JSON）</span>
          <textarea v-model="form.config" rows="4" placeholder='{"command":"npx","args":["-y","@pkg"]} 或 {"url":"http://host/mcp"}' />
        </label>
        <div class="tool-actions">
          <button class="secondary-btn" :disabled="testing" @click="testConnection">
            {{ testing ? '测试中...' : '测试连接' }}
          </button>
          <button class="primary-btn" :disabled="submitting" @click="submit">
            {{ submitting ? '保存中...' : mode === 'edit' ? '保存修改' : '注册工具' }}
          </button>
        </div>
        <div v-if="testResult" class="test-result" :class="{ 'test-ok': testResult.ok, 'test-fail': !testResult.ok }">
          <template v-if="testResult.ok">
            连接成功，服务端暴露工具：{{ (testResult.tools || []).join('、') || '（无）' }}
          </template>
          <template v-else>
            连接失败：{{ testResult.error }}
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 8vh 1rem 1rem;
  z-index: 1000;
}
.modal-dialog {
  background: #fff;
  border-radius: 12px;
  width: 100%;
  max-width: 520px;
  padding: 1.2rem;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.2);
}
.modal-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1rem;
}
.modal-head h3 {
  margin: 0;
}
.modal-close {
  border: none;
  background: transparent;
  font-size: 1rem;
  cursor: pointer;
  color: var(--muted, #8c8c8c);
  padding: 0.2rem 0.4rem;
  margin: 0;
}
.tool-actions {
  display: flex;
  gap: 0.6rem;
  align-items: center;
}
.test-result {
  margin-top: 0.6rem;
  font-size: 0.82rem;
  padding: 0.5rem 0.6rem;
  border-radius: 6px;
  word-break: break-all;
}
.test-ok {
  background: #e9f7ef;
  color: #1d7a3a;
  border: 1px solid #bfe3cc;
}
.test-fail {
  background: #fdecec;
  color: #b3261e;
  border: 1px solid #f3c3c3;
}
</style>
