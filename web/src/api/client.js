import { apiBaseUrl } from '../config/env'

export function buildApiUrl(path) {
  return `${apiBaseUrl}${path}`
}

async function parseJsonSafe(response) {
  const contentType = response.headers.get('content-type') || ''
  if (!contentType.includes('application/json')) {
    return null
  }

  try {
    return await response.json()
  } catch {
    return null
  }
}

export async function apiRequest(path, options = {}) {
  const response = await fetch(buildApiUrl(path), options)
  const data = await parseJsonSafe(response)

  if (!response.ok) {
    const message = data?.error || data?.message || `HTTP ${response.status}`
    const error = new Error(message)
    error.status = response.status
    error.data = data
    throw error
  }

  return data
}

export async function login(payload) {
  return apiRequest('/api/auth/login', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json'
    },
    body: JSON.stringify(payload)
  })
}

export async function register(payload) {
  return apiRequest('/api/auth/register', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json'
    },
    body: JSON.stringify(payload)
  })
}

export async function getTasks(token) {
  return apiRequest('/api/tasks', {
    method: 'GET',
    headers: {
      Authorization: `Bearer ${token}`
    }
  })
}

export async function getTaskStatus(token, taskId) {
  return apiRequest(`/api/tasks/${encodeURIComponent(taskId)}`, {
    method: 'GET',
    headers: {
      Authorization: `Bearer ${token}`
    }
  })
}

export async function submitTask(token, payload) {
  return apiRequest('/api/tasks/submit', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
}

export async function batchSubmitTasks(token, payload) {
  return apiRequest('/api/tasks/batch-submit', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
}

export async function deleteSession(token, sessionId) {
  return apiRequest(`/api/tasks/session/${encodeURIComponent(sessionId)}`, {
    method: 'DELETE',
    headers: {
      Authorization: `Bearer ${token}`
    }
  })
}

export function createTaskStream(token) {
  return new EventSource(buildApiUrl(`/api/tasks/stream?token=${encodeURIComponent(token)}`))
}

export async function probeBackend() {
  try {
    const response = await fetch(buildApiUrl('/api/tasks'), {
      method: 'GET'
    })

    return {
      ok: true,
      status: response.status,
      reachable: true
    }
  } catch (error) {
    return {
      ok: false,
      status: null,
      reachable: false,
      message: error instanceof Error ? error.message : String(error)
    }
  }
}

// ===== Agent 相关接口 =====

export async function listAgents(token) {
  return apiRequest('/api/agents', {
    method: 'GET',
    headers: { Authorization: `Bearer ${token}` }
  })
}

export async function createAgent(token, payload) {
  return apiRequest('/api/agents', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
}

export async function updateAgent(token, agentId, payload) {
  return apiRequest(`/api/agents/${encodeURIComponent(agentId)}`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
}

export async function deleteAgent(token, agentId) {
  return apiRequest(`/api/agents/${encodeURIComponent(agentId)}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${token}` }
  })
}

export async function runAgent(token, agentId, payload) {
  return apiRequest(`/api/agents/${encodeURIComponent(agentId)}/run`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
}

// ===== 工具管理 =====

export async function listTools(token) {
  return apiRequest('/api/tools', {
    method: 'GET',
    headers: { Authorization: `Bearer ${token}` }
  })
}

export async function createTool(token, payload) {
  return apiRequest('/api/tools', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
}

export async function deleteTool(token, toolId) {
  return apiRequest(`/api/tools/${encodeURIComponent(toolId)}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${token}` }
  })
}

export async function updateTool(token, toolId, payload) {
  return apiRequest(`/api/tools/${encodeURIComponent(toolId)}`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
}

export async function updateToolStatus(token, toolId, status) {
  return apiRequest(`/api/tools/${encodeURIComponent(toolId)}/status`, {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify({ status })
  })
}

export async function listToolAgents(token, toolId) {
  return apiRequest(`/api/tools/${encodeURIComponent(toolId)}/agents`, {
    method: 'GET',
    headers: { Authorization: `Bearer ${token}` }
  })
}

export async function searchMCPServers(token, query) {
  return apiRequest('/api/tools/search', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify({ query })
  })
}

export async function testToolConnection(token, config) {
  return apiRequest('/api/tools/test', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify({ config })
  })
}

// ===== 会话管理 =====

export async function listConversations(token) {
  return apiRequest('/api/conversations', {
    method: 'GET',
    headers: { Authorization: `Bearer ${token}` }
  })
}

export async function createConversation(token, payload) {
  return apiRequest('/api/conversations', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
}

export async function deleteConversation(token, conversationId) {
  return apiRequest(`/api/conversations/${encodeURIComponent(conversationId)}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${token}` }
  })
}

// ===== 知识库管理 =====

export async function listKnowledgeBases(token) {
  return apiRequest('/api/knowledge-bases', {
    method: 'GET',
    headers: { Authorization: `Bearer ${token}` }
  })
}

export async function createKnowledgeBase(token, payload) {
  return apiRequest('/api/knowledge-bases', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
}

export async function deleteKnowledgeBase(token, kbId) {
  return apiRequest(`/api/knowledge-bases/${encodeURIComponent(kbId)}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${token}` }
  })
}

export async function addDocument(token, kbId, payload) {
  return apiRequest(`/api/knowledge-bases/${encodeURIComponent(kbId)}/documents`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
    body: payload
  })
}

// ===== Agent 绑定关系 =====

export async function bindAgentTools(token, agentId, toolIds) {
  return apiRequest(`/api/agents/${encodeURIComponent(agentId)}/tools`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify({ tool_ids: toolIds })
  })
}

export async function listAgentTools(token, agentId) {
  return apiRequest(`/api/agents/${encodeURIComponent(agentId)}/tools`, {
    method: 'GET',
    headers: { Authorization: `Bearer ${token}` }
  })
}

export async function bindAgentKnowledgeBases(token, agentId, kbIds) {
  return apiRequest(`/api/agents/${encodeURIComponent(agentId)}/knowledge-bases`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify({ knowledge_base_ids: kbIds })
  })
}

export async function listAgentKnowledgeBases(token, agentId) {
  return apiRequest(`/api/agents/${encodeURIComponent(agentId)}/knowledge-bases`, {
    method: 'GET',
    headers: { Authorization: `Bearer ${token}` }
  })
}
