<script setup>
import { onMounted, ref } from 'vue'
import { addDocument, createKnowledgeBase, deleteKnowledgeBase, listKnowledgeBases } from '../api/client'

const props = defineProps({
  token: { type: String, default: '' }
})

const emit = defineEmits(['message'])

const kbs = ref([])
const loading = ref(false)
const selectedId = ref('')
const form = ref({ name: '', description: '', type: 'rag' })
const docForm = ref({ title: '', content: '' })
const fileInput = ref(null)
const selectedFile = ref(null)

async function refresh() {
  loading.value = true
  try {
    kbs.value = await listKnowledgeBases(props.token)
    if (!selectedId.value && kbs.value.length > 0) {
      selectedId.value = kbs.value[0].id
    }
  } catch (e) {
    emit('message', `加载知识库失败：${e.message}`, 'error')
  } finally {
    loading.value = false
  }
}

async function create() {
  if (!form.value.name.trim()) {
    emit('message', '请填写知识库名称', 'error')
    return
  }
  try {
    const data = await createKnowledgeBase(props.token, {
      name: form.value.name.trim(),
      description: form.value.description.trim(),
      type: form.value.type || 'rag'
    })
    form.value = { name: '', description: '', type: 'rag' }
    selectedId.value = data?.knowledge_base?.id || ''
    emit('message', '知识库创建成功', 'success')
    await refresh()
  } catch (e) {
    emit('message', `创建失败：${e.message}`, 'error')
  }
}

function onFileChange(e) {
  selectedFile.value = e.target.files?.[0] || null
}

function formatSize(bytes) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

async function upload() {
  if (!selectedId.value) {
    emit('message', '请先选择知识库', 'error')
    return
  }
  const hasFile = !!selectedFile.value
  const hasText = !!docForm.value.content.trim()
  if (!hasFile && !hasText) {
    emit('message', '请选择文件或填写文档内容', 'error')
    return
  }
  try {
    const fd = new FormData()
    if (docForm.value.title.trim()) fd.append('title', docForm.value.title.trim())
    if (hasFile) {
      fd.append('file', selectedFile.value)
    } else {
      const file = new File(
        [docForm.value.content],
        docForm.value.title.trim() || 'clipboard.txt',
        { type: 'text/plain' }
      )
      fd.append('file', file)
    }
    await addDocument(props.token, selectedId.value, fd)
    docForm.value = { title: '', content: '' }
    selectedFile.value = null
    if (fileInput.value) fileInput.value.value = ''
    emit('message', '文档已提交，异步构建中', 'success')
    await refresh()
  } catch (e) {
    emit('message', `文档上传失败：${e.message}`, 'error')
  }
}

async function remove(id) {
  if (!window.confirm('确定删除该知识库吗？')) return
  try {
    await deleteKnowledgeBase(props.token, id)
    if (selectedId.value === id) selectedId.value = ''
    emit('message', '知识库已删除', 'success')
    await refresh()
  } catch (e) {
    emit('message', `删除失败：${e.message}`, 'error')
  }
}

onMounted(refresh)
</script>

<template>
  <section class="card">
    <div class="panel-head">
      <div class="panel-title-block">
        <h2>知识库构建</h2>
        <p>上传文档构建 RAG 向量库，供 Agent 绑定后作为领域知识检索。</p>
      </div>
    </div>

    <div class="agent-layout">
      <aside class="agent-list">
        <div v-if="loading" class="empty-state">加载中...</div>
        <div v-else-if="kbs.length === 0" class="empty-state">暂无知识库</div>
        <button
          v-for="kb in kbs"
          :key="kb.id"
          class="agent-item"
          :class="{ 'agent-item-active': kb.id === selectedId }"
          @click="selectedId = kb.id"
        >
          <strong>{{ kb.name }}</strong>
          <span>{{ kb.type }} · {{ kb.status }} · {{ kb.doc_count }} 文档</span>
          <span class="conv-delete-btn" @click.stop="remove(kb.id)">删除</span>
        </button>
      </aside>

      <div class="agent-editor">
        <h3>新建知识库</h3>
        <div class="auth-form">
          <label>
            <span>名称</span>
            <input v-model="form.name" type="text" placeholder="例如：产品文档库" />
          </label>
          <label>
            <span>描述</span>
            <input v-model="form.description" type="text" placeholder="知识库用途说明" />
          </label>
          <label>
            <span>类型</span>
            <select v-model="form.type">
              <option value="rag">RAG 向量</option>
              <option value="kag">KAG 图谱</option>
              <option value="hybrid">混合</option>
            </select>
          </label>
          <button class="primary-btn" @click="create">创建知识库</button>
        </div>

        <div v-if="selectedId" class="agent-run">
          <h3>上传文档</h3>
          <label class="prompt-field">
            <span>标题（可选，默认取文件名）</span>
            <input v-model="docForm.title" type="text" placeholder="文档标题" />
          </label>
          <div
            class="file-drop"
            :class="{ 'has-file': !!selectedFile }"
            @click="fileInput.click()"
          >
            <template v-if="selectedFile">
              <strong>{{ selectedFile.name }}</strong>
              <span>{{ formatSize(selectedFile.size) }} · 点击更换</span>
            </template>
            <template v-else>
              <span>点击选择文件，支持 txt / md / csv / json / 代码等文本类型</span>
            </template>
            <input
              ref="fileInput"
              type="file"
              class="file-input-hidden"
              accept=".txt,.md,.markdown,.csv,.json,.xml,.html,.log,.yaml,.yml,.go,.js,.ts,.jsx,.tsx,.py,.java,.sql,.sh,.rs,.c,.h,.cpp,.vue"
              @change="onFileChange"
            />
          </div>
          <details class="paste-alt">
            <summary>或直接粘贴文本</summary>
            <textarea
              v-model="docForm.content"
              rows="4"
              placeholder="粘贴文档内容，将异步切分并向量化"
            />
          </details>
          <button
            class="primary-btn"
            :disabled="!selectedFile && !docForm.content.trim()"
            @click="upload"
          >
            提交构建
          </button>
        </div>
      </div>
    </div>
  </section>
</template>
