
        const { createApp, ref, computed, onMounted, onUnmounted } = Vue;

        createApp({
            setup() {
                // 认证相关状态
                const isLoggedIn = ref(false);
                const authMode = ref('login'); // 'login', 'register', 'forgot'
                const isRequestingCode = ref(false);
                const codeSent = ref(false);
                const username = ref('');
                const authToken = ref('');
                const authForm = ref({ username: '', password: '' });
                const isAuthSubmitting = ref(false);

                // 任务相关状态
                const tasks = ref([]);
                const form = ref({
                    session_id: '',
                    system_prompt: '',
                    priority: 2,
                    payload: ''
                });
                const isSubmitting = ref(false);
                const isBatchMode = ref(false);
                const isOptimizing = ref(false);
                
                // 查询功能相关的状态
                const searchId = ref('');
                const isSearching = ref(false);
                const searchResult = ref(null);

                // 聊天相关状态
                const activeSessionId = ref(null);
                const chatInput = ref('');
                const isChatSubmitting = ref(false);

                // 计算属性：按 session_id 分组任务
                const groupedConversations = computed(() => {
                    const groups = {};
                    tasks.value.forEach(task => {
                        const key = task.session_id || task.id; // 如果没有 session_id，自身 ID 即为上下文起点
                        if (!groups[key]) {
                            groups[key] = {
                                id: key,
                                tasks: [],
                                updated_at: task.created_at,
                                first_payload: task.payload,
                                latest_status: task.status,
                                system_prompt: task.system_prompt || ''
                            };
                        }
                        groups[key].tasks.push(task);
                        
                        // 更新组的最新时间与状态
                        if (new Date(task.created_at) >= new Date(groups[key].updated_at)) {
                            groups[key].updated_at = task.created_at;
                            groups[key].latest_status = task.status;
                        }
                        // 寻找最早的一条作为摘要
                        if (new Date(task.created_at) < new Date(groups[key].tasks[0].created_at)) {
                            groups[key].first_payload = task.payload;
                            groups[key].system_prompt = task.system_prompt || groups[key].system_prompt;
                        }
                    });
                    return Object.values(groups).sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at));
                });

                // 当前激活的会话任务列表（时间升序排列用于聊天展示）
                const activeSessionTasks = computed(() => {
                    if (!activeSessionId.value) return [];
                    const group = groupedConversations.value.find(g => g.id === activeSessionId.value);
                    if (!group) return [];
                    return [...group.tasks].sort((a, b) => new Date(a.created_at) - new Date(b.created_at));
                });

                const scrollToBottom = () => {
                    const container = document.getElementById('chat-messages-container');
                    if (container) {
                        container.scrollTop = container.scrollHeight;
                    }
                };

                const openChat = (id) => {
                    activeSessionId.value = id;
                    // 同步到左侧表单的 session_id，以防用户习惯从左侧提交
                    form.value.session_id = id;
                    setTimeout(scrollToBottom, 100);
                };

                const closeChat = () => {
                    activeSessionId.value = null;
                    form.value.session_id = '';
                };

                const sendChatMessage = async () => {
                    if (!chatInput.value.trim() || isChatSubmitting.value) return;
                    isChatSubmitting.value = true;
                    
                    const group = groupedConversations.value.find(g => g.id === activeSessionId.value);
                    const sysPrompt = group ? group.system_prompt : '';

                    const payloadData = {
                        session_id: activeSessionId.value,
                        system_prompt: sysPrompt,
                        priority: 2,
                        payload: chatInput.value
                    };

                    try {
                        const res = await fetchWithAuth('/api/tasks/submit', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify(payloadData)
                        });
                        if (res.ok) {
                            chatInput.value = '';
                            await fetchTasks();
                            setTimeout(scrollToBottom, 200);
                        } else if (res.status === 429) {
                            const errData = await res.json();
                            alert(errData.error || '您的操作太频繁啦，请稍后再试');
                        } else {
                            const errData = await res.json();
                            alert('发送失败: ' + (errData.error || res.statusText));
                        }
                    } catch (err) {
                        alert('发送请求出错: ' + err.message);
                    } finally {
                        isChatSubmitting.value = false;
                    }
                };


                let sseEventSource = null;

                // 初始化时检查本地存储的 Token
                const checkLoginStatus = () => {
                    const token = localStorage.getItem('gotaskai_token');
                    const savedUser = localStorage.getItem('gotaskai_username');
                    if (token && savedUser) {
                        authToken.value = token;
                        username.value = savedUser;
                        isLoggedIn.value = true;
                    }
                };

                // 封装带 Token 的 fetch 请求
                const fetchWithAuth = async (url, options = {}) => {
                    const headers = {
                        ...options.headers,
                        'Authorization': `Bearer ${authToken.value}`
                    };
                    const response = await fetch(url, { ...options, headers });
                    if (response.status === 401) {
                        logout(); // Token 过期或无效时自动登出
                        alert('登录已过期，请重新登录');
                        throw new Error('Unauthorized');
                    }
                    return response;
                };

                const initSSE = () => {
                    if (sseEventSource) {
                        sseEventSource.close();
                    }
                    if (!authToken.value) return;

                    sseEventSource = new EventSource(`/api/tasks/stream?token=${authToken.value}`);

                    sseEventSource.addEventListener('task_update', (event) => {
                        const updatedTask = JSON.parse(event.data);
                        
                        // 更新任务列表中的状态
                        const index = tasks.value.findIndex(t => t.id === updatedTask.id);
                        if (index !== -1) {
                            // 替换为最新状态，Vue 会自动触发响应式更新
                            tasks.value[index] = updatedTask;
                        } else {
                            // 如果是新任务，加到列表最前面
                            tasks.value.unshift(updatedTask);
                        }

                        // 如果当前正好在搜索这个任务，也一并更新详情
                        if (searchResult.value && searchResult.value.id === updatedTask.id) {
                            searchResult.value = updatedTask;
                        }
                        
                        // 如果当前正好在这个聊天页面内，并且是这个会话的消息更新了，滚动到底部
                        if (activeSessionId.value === updatedTask.session_id || activeSessionId.value === updatedTask.id) {
                            setTimeout(scrollToBottom, 100);
                        }
                    });

                    sseEventSource.onerror = (err) => {
                        console.error('SSE Error:', err);
                        sseEventSource.close();
                        // 简单的断线重连逻辑
                        setTimeout(initSSE, 5000);
                    };
                };

                const handleAuth = async () => {
                    isAuthSubmitting.value = true;
                    const endpoint = isLoginMode.value ? '/api/auth/login' : '/api/auth/register';
                    try {
                        const res = await fetch(endpoint, {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify(authForm.value)
                        });
                        
                        // 防止后端返回非 JSON 格式的错误（如默认的 404 纯文本页面）
                        const contentType = res.headers.get("content-type");
                        let data;
                        if (contentType && contentType.includes("application/json")) {
                            data = await res.json();
                        } else {
                            const errText = await res.text();
                            throw new Error(errText || `HTTP error ${res.status}`);
                        }

                        if (res.ok) {
                            if (isLoginMode.value) {
                                // 登录成功
                                localStorage.setItem('gotaskai_token', data.token);
                                localStorage.setItem('gotaskai_username', data.username);
                                checkLoginStatus();
                                authForm.value = { username: '', password: '' };
                                fetchTasks(); // 登录后拉取任务全量列表
                                initSSE();    // 登录后建立 SSE 长连接
                            } else {
                                // 注册成功
                                alert(data.message);
                                isLoginMode.value = true; // 切换回登录模式
                            }
                        } else {
                            alert(data.error || '请求失败');
                        }
                    } catch (err) {
                        alert('网络错误: ' + err.message);
                    } finally {
                        isAuthSubmitting.value = false;
                    }
                };

                const logout = () => {
                    localStorage.removeItem('gotaskai_token');
                    localStorage.removeItem('gotaskai_username');
                    isLoggedIn.value = false;
                    authToken.value = '';
                    username.value = '';
                    tasks.value = [];
                    if (sseEventSource) {
                        sseEventSource.close();
                        sseEventSource = null;
                    }
                };

                const sortedTasks = computed(() => {
                    return [...tasks.value].sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
                });

                const fetchTasks = async () => {
                    if (!isLoggedIn.value) return;
                    try {
                        const res = await fetchWithAuth('/api/tasks');
                        if (res.ok) {
                            const data = await res.json();
                            tasks.value = data || [];
                        }
                    } catch (err) {
                        console.error('Failed to fetch tasks', err);
                    }
                };

                const submitTask = async () => {
                    if (!form.value.payload.trim()) return;
                    isSubmitting.value = true;
                    try {
                        const res = await fetchWithAuth('/api/tasks/submit', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify(form.value)
                        });
                        if (res.ok) {
                            form.value.payload = '';
                            form.value.priority = 2; // 重置为普通优先级
                            await fetchTasks();
                        } else if (res.status === 429) {
                            // 处理限流报错 (Too Many Requests)
                            const errData = await res.json();
                            alert(errData.error || '您的操作太频繁啦，请稍后再试');
                        } else {
                            const errData = await res.json();
                            alert('提交失败: ' + (errData.error || res.statusText));
                        }
                    } catch (err) {
                        alert('提交请求出错: ' + err.message);
                    } finally {
                        isSubmitting.value = false;
                    }
                };

                const submitBatchTasks = async () => {
                    const lines = form.value.payload.split('\n').map(l => l.trim()).filter(l => l);
                    if (lines.length === 0) return;
                    if (lines.length > 100) {
                        alert('一次最多只能批量提交 100 个任务');
                        return;
                    }

                    isSubmitting.value = true;
                    
                    const batchTasks = lines.map(line => ({
                        session_id: form.value.session_id,
                        system_prompt: form.value.system_prompt,
                        priority: form.value.priority,
                        payload: line
                    }));

                    try {
                        const res = await fetchWithAuth('/api/tasks/batch-submit', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ tasks: batchTasks })
                        });
                        
                        if (res.ok) {
                            form.value.payload = '';
                            form.value.priority = 2; // 重置为普通优先级
                            // 由于是批量提交，SSE 可能无法完全覆盖或者有时序问题，主动拉取一次最新列表
                            setTimeout(fetchTasks, 500);
                        } else if (res.status === 429) {
                            const errData = await res.json();
                            alert(errData.error || '您的操作太频繁啦，请稍后再试');
                        } else {
                            const errData = await res.json();
                            alert('批量提交失败: ' + (errData.error || res.statusText));
                        }
                    } catch (err) {
                        alert('提交请求出错: ' + err.message);
                    } finally {
                        isSubmitting.value = false;
                    }
                };

                const optimizePrompt = async () => {
                    if (!form.value.system_prompt.trim()) return;
                    
                    isOptimizing.value = true;
                    try {
                        const res = await fetchWithAuth('/api/tasks/optimize-prompt', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ prompt: form.value.system_prompt })
                        });
                        
                        const contentType = res.headers.get("content-type");
                        let data;
                        if (contentType && contentType.includes("application/json")) {
                            data = await res.json();
                        } else {
                            const errText = await res.text();
                            throw new Error(errText || `HTTP error ${res.status}`);
                        }
                        
                        if (res.ok) {
                            // 将优化后的文本直接填入输入框
                            form.value.system_prompt = data.optimized_prompt;
                        } else {
                            throw new Error(data.error || '请求失败');
                        }
                    } catch (error) {
                        alert(`优化失败: ${error.message}`);
                    } finally {
                        isOptimizing.value = false;
                    }
                };

                const handleFormSubmit = () => {
                    if (isBatchMode.value) {
                        submitBatchTasks();
                    } else {
                        submitTask();
                    }
                };

                const searchTask = async () => {
                    const id = searchId.value.trim();
                    if (!id) return;
                    
                    isSearching.value = true;
                    searchResult.value = null;
                    
                    try {
                        const res = await fetchWithAuth(`/api/tasks/${encodeURIComponent(id)}`);
                        if (res.ok) {
                            searchResult.value = await res.json();
                        } else if (res.status === 404) {
                            alert('未找到该任务，请检查 ID 是否完整正确');
                        } else {
                            const errData = await res.json();
                            alert('查询失败: ' + (errData.error || res.statusText));
                        }
                    } catch (err) {
                        alert('查询请求出错: ' + err.message);
                    } finally {
                        isSearching.value = false;
                    }
                };

                const copyToSearch = async (fullId) => {
                    searchId.value = fullId;
                    
                    // 尝试将内容复制到系统剪贴板
                    try {
                        await navigator.clipboard.writeText(fullId);
                    } catch (err) {
                        console.error('无法复制到剪贴板: ', err);
                    }

                    // 自动滚动到顶部并触发查询
                    window.scrollTo({ top: 0, behavior: 'smooth' });
                    // 直接触发查询，提升用户体验
                    searchTask(); 
                };

                const continueChat = (task) => { openChat(task.session_id || task.id); return; 
                    // 如果该任务已经有 session_id，则继续使用该 session_id
                    // 如果没有，则以该任务本身的 id 作为新会话的起点
                    form.value.session_id = task.session_id || task.id;
                    form.value.system_prompt = task.system_prompt || '';
                    
                    // 关闭批量模式以防万一
                    isBatchMode.value = false;
                    
                    // 自动滚动到顶部
                    window.scrollTo({ top: 0, behavior: 'smooth' });
                };

                const deleteTask = async (id) => {
                    if (!confirm('确定要删除这个任务吗？')) return;
                    
                    try {
                        const res = await fetchWithAuth(`/api/tasks/${id}`, {
                            method: 'DELETE'
                        });
                        
                        if (res.ok) {
                            // 从列表中移除
                            tasks.value = tasks.value.filter(t => t.id !== id);
                            if (searchResult.value && searchResult.value.id === id) {
                                searchResult.value = null;
                            }
                        } else {
                            // 防止后端返回非 JSON 格式的错误（如默认的 404 纯文本页面）
                            const contentType = res.headers.get("content-type");
                            if (contentType && contentType.includes("application/json")) {
                                const errData = await res.json();
                                alert('删除失败: ' + (errData.error || res.statusText));
                            } else {
                                const errText = await res.text();
                                alert('删除失败: ' + (errText || res.statusText));
                            }
                        }
                    } catch (err) {
                        alert('请求出错: ' + err.message);
                    }
                };

                const retryTask = async (id) => {
                    try {
                        const res = await fetchWithAuth(`/api/tasks/${id}/retry`, {
                            method: 'POST'
                        });
                        
                        if (res.ok) {
                            fetchTasks(); // 刷新列表以显示新的状态
                        } else {
                            const errData = await res.json();
                            alert('重试失败: ' + (errData.error || res.statusText));
                        }
                    } catch (err) {
                        alert('请求出错: ' + err.message);
                    }
                };

                const cancelTask = async (id) => {
                    if (!confirm('确定要取消这个任务吗？')) return;
                    
                    try {
                        const res = await fetchWithAuth(`/api/tasks/${id}/cancel`, {
                            method: 'POST'
                        });
                        
                        if (res.ok) {
                            fetchTasks();
                        } else {
                            const errData = await res.json();
                            alert('取消失败: ' + (errData.error || res.statusText));
                        }
                    } catch (err) {
                        alert('请求出错: ' + err.message);
                    }
                };

                const getStatusBadgeClass = (status) => {
                    const map = {
                        'pending': 'text-gray-600',
                        'processing': 'text-blue-600',
                        'completed': 'text-green-600',
                        'failed': 'text-red-600'
                    };
                    return map[status] || 'text-gray-600';
                };

                const getBorderColor = (status) => {
                    const map = {
                        'processing': 'border-gray-200 border-l-blue-500 shadow-[0_0_15px_rgba(59,130,246,0.15)]',
                        'completed': 'border-gray-200 border-l-green-500',
                        'failed': 'border-gray-200 border-l-red-500',
                        'pending': 'border-gray-200 border-l-gray-400'
                    };
                    return map[status] || 'border-gray-200 border-l-gray-200';
                };

                const formatDate = (dateStr) => {
                    const d = new Date(dateStr);
                    return d.toLocaleTimeString('zh-CN', { hour12: false });
                };

                onMounted(() => {
                    checkLoginStatus();
                    if (isLoggedIn.value) {
                        fetchTasks();
                        initSSE(); // 如果已经登录，刷新页面时直接建立 SSE 连接
                    }
                });

                onUnmounted(() => {
                    if (sseEventSource) sseEventSource.close();
                });

                return {
                    isLoggedIn,
                    authMode,
                    isRequestingCode,
                    codeSent,
                    requestResetCode,
                    handleResetPassword,
                    username,
                    authForm,
                    isAuthSubmitting,
                    handleAuth,
                    logout,
                    tasks,
                    form,
                    isSubmitting,
                    isBatchMode,
                    
                    isOptimizing,
                    activeSessionId,
                    chatInput,
                    isChatSubmitting,
                    groupedConversations,
                    activeSessionTasks,
                    openChat,
                    closeChat,
                    sendChatMessage,
                    searchId,
                    isSearching,
                    searchResult,
                    sortedTasks,
                    fetchTasks,
                    submitTask,
                    submitBatchTasks,
                    handleFormSubmit,
                    optimizePrompt,
                    searchTask,
                    copyToSearch,
                    deleteTask,
                    retryTask,
                    cancelTask,
                    continueChat,
                    getStatusBadgeClass,
                    getBorderColor,
                    formatDate
                };
            }
        }).mount('#app');
    