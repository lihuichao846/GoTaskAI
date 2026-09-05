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
