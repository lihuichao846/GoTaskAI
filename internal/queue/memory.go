package queue

import (
	"errors"
	"log/slog"
	"time"

	"gotaskai/internal/metrics"
	"gotaskai/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 长期记忆（用户向）的数据访问层。
//
// 本文件只负责"存取"，不含任何业务判断（闸门与渲染在 worker/api 侧），
// 但【含一条不可让步的安全约束】：所有召回/统计/列表查询都必须由 memoryScopeWhere 构造作用域，
// 拿不到 user_id 时一律 fail-closed（返回空），绝不回退到"公共"集合。
// 记忆的隔离要求与 RAG 不同：RAG 的默认知识库可以共享，记忆不可以。

var (
	// ErrMemoryInvalidScope 表示调用方未提供有效的用户作用域（fail-closed）。
	ErrMemoryInvalidScope = errors.New("memory: invalid scope (missing user id)")
	// ErrMemoryQuotaExceeded 表示该用户的记忆条数已达上限。
	ErrMemoryQuotaExceeded = errors.New("memory: per-user quota exceeded")
)

// memoryScopeWhere 构造记忆查询的【作用域条件】。
// 返回 ok=false 表示作用域非法，调用方必须放弃本次查询（fail-closed）。
//
// 抽成纯函数只有一个目的：让"查询必须带 user_id"这条隐私约束可以被单测直接断言。
// 漏掉 user_id 是这类代码最危险、也最难在联调中发现的一类 bug。
func memoryScopeWhere(userID uint, agentID string) (clause string, args []any, ok bool) {
	if userID == 0 {
		return "", nil, false
	}
	clause = "user_id = ?"
	args = []any{userID}
	if agentID != "" {
		// 只召回"该 Agent 专属"与"该用户全局"两类；默认不在 Agent 之间共享记忆。
		clause += " AND agent_id IN ?"
		args = append(args, []string{agentID, ""})
	}
	return clause, args, true
}

// CreateUserMemory 写入一条长期记忆，并处理"同一 key 已有生效记录"的情形。
//
// 冲突语义（双时间轴）：若同一 (user_id, agent_id, mem_key) 已有生效记录，
// 则把旧记录【关窗】而不删除——valid_until = now、superseded_at = now、
// status = superseded、active_key = NULL——再插入新记录。
// 历史因此完整保留，既能回答"现在是什么"，也能回答"当时是什么"。
//
// perUserLimit <= 0 表示不限制；超限返回 ErrMemoryQuotaExceeded。
func (m *TaskManager) CreateUserMemory(mem *model.UserMemory, perUserLimit int) error {
	if mem == nil || mem.UserID == 0 {
		return ErrMemoryInvalidScope
	}

	now := time.Now()
	if mem.ID == "" {
		mem.ID = uuid.New().String()
	}
	if mem.ValidFrom.IsZero() {
		mem.ValidFrom = now
	}
	if mem.Status == "" {
		mem.Status = model.MemoryStatusActive
	}
	if mem.Confidence <= 0 {
		mem.Confidence = 1
	}
	if mem.CreatedAt.IsZero() {
		mem.CreatedAt = now
	}
	if mem.UpdatedAt.IsZero() {
		mem.UpdatedAt = now
	}
	// ActiveKey 只在有 MemKey 时设置：没有 key 就没有"同一件事"的判定依据，
	// 此时置 NULL 让多条记忆共存（唯一索引允许多个 NULL）。
	var activeKey *string
	if mem.MemKey != "" {
		k := mem.MemKey
		activeKey = &k
	}
	mem.ActiveKey = activeKey

	return m.db.Transaction(func(tx *gorm.DB) error {
		if perUserLimit > 0 {
			var n int64
			if err := tx.Model(&model.UserMemory{}).
				Where("user_id = ? AND status = ?", mem.UserID, model.MemoryStatusActive).
				Count(&n).Error; err != nil {
				return err
			}
			if n >= int64(perUserLimit) {
				metrics.MemoryWriteTotal.WithLabelValues("rejected").Inc()
				return ErrMemoryQuotaExceeded
			}
		}

		if activeKey != nil {
			// 关窗旧记录。GORM 的 Updates(map) 会把 nil 写成 SQL NULL，
			// 这正是"失效记录不占用唯一键"的关键。
			if err := tx.Model(&model.UserMemory{}).
				Where("user_id = ? AND agent_id = ? AND active_key = ? AND status = ?",
					mem.UserID, mem.AgentID, *activeKey, model.MemoryStatusActive).
				Updates(map[string]any{
					"valid_until":   now,
					"superseded_at": now,
					"status":        model.MemoryStatusSuperseded,
					"active_key":    nil,
					"updated_at":    now,
				}).Error; err != nil {
				return err
			}
		}

		if err := tx.Create(mem).Error; err != nil {
			return err
		}
		metrics.MemoryWriteTotal.WithLabelValues("created").Inc()
		return nil
	})
}

// GetUserMemory 按 ID 读取单条记忆（供 API 做归属校验）。
func (m *TaskManager) GetUserMemory(id string) (*model.UserMemory, bool) {
	if id == "" {
		return nil, false
	}
	var mem model.UserMemory
	if err := m.db.Where("id = ?", id).First(&mem).Error; err != nil {
		return nil, false
	}
	return &mem, true
}

// ListUserMemories 列出某用户的记忆（status 为空表示不限状态），并返回总数。
// 同样走 fail-closed 作用域：userID 为 0 时返回空。
func (m *TaskManager) ListUserMemories(userID uint, agentID, status string, limit, offset int) ([]*model.UserMemory, int64) {
	clause, args, ok := memoryScopeWhere(userID, agentID)
	if !ok {
		metrics.MemoryRecallFailClosed.Inc()
		slog.Error("[Memory][ALERT] list memories rejected: empty user_id (fail-closed)")
		return nil, 0
	}

	base := func() *gorm.DB {
		q := m.db.Model(&model.UserMemory{}).Where(clause, args...)
		if status != "" {
			q = q.Where("status = ?", status)
		}
		return q
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		slog.Error("count user memories failed", "user_id", userID, "error", err)
	}

	var list []*model.UserMemory
	q := base().Order("status asc").Order("confidence desc").Order("updated_at desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	if err := q.Find(&list).Error; err != nil {
		slog.Error("list user memories failed", "user_id", userID, "error", err)
		return nil, 0
	}
	return list, total
}

// UpdateUserMemoryContent 更新一条记忆的正文与 MemKey（仅限归属用户）。
// 返回 ok=false 表示目标不存在或不属于该用户（调用方按 404 处理）；
// 返回 err 非空表示写入失败（最可能是 MemKey 与另一条生效记忆冲突，触发唯一索引）。
func (m *TaskManager) UpdateUserMemoryContent(id string, userID uint, content, memKey string) (bool, error) {
	if id == "" || userID == 0 {
		return false, ErrMemoryInvalidScope
	}
	updates := map[string]any{
		"content":    content,
		"updated_at": time.Now(),
	}
	if memKey != "" {
		updates["mem_key"] = memKey
		// 同步生效键，保持"同一 key 只有一条生效记录"的不变式。
		updates["active_key"] = memKey
	}
	res := m.db.Model(&model.UserMemory{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(updates)
	if res.Error != nil {
		slog.Error("update user memory failed", "id", id, "error", res.Error)
		return false, res.Error
	}
	// GORM 影响 0 行不报错，必须用 RowsAffected 判定；0 行说明"不存在或不是你的"。
	return res.RowsAffected == 1, nil
}

// DeleteUserMemory 物理删除一条记忆（仅限归属用户）。
//
// 这里刻意用物理删除而不是软删除：删除权是用户对自己数据的处置权，
// "标记删除"对合规没有意义——数据仍在那里。
func (m *TaskManager) DeleteUserMemory(id string, userID uint) (int64, error) {
	if id == "" || userID == 0 {
		return 0, ErrMemoryInvalidScope
	}
	res := m.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.UserMemory{})
	return res.RowsAffected, res.Error
}

// DeleteAllUserMemories 清空某用户的全部记忆（"关闭记忆并清空"），返回删除行数。
func (m *TaskManager) DeleteAllUserMemories(userID uint) (int64, error) {
	if userID == 0 {
		return 0, ErrMemoryInvalidScope
	}
	res := m.db.Where("user_id = ?", userID).Delete(&model.UserMemory{})
	return res.RowsAffected, res.Error
}

// CountActiveUserMemories 统计某用户生效中的记忆条数。
func (m *TaskManager) CountActiveUserMemories(userID uint) int64 {
	if userID == 0 {
		return 0
	}
	var n int64
	if err := m.db.Model(&model.UserMemory{}).
		Where("user_id = ? AND status = ?", userID, model.MemoryStatusActive).
		Count(&n).Error; err != nil {
		slog.Error("count user memories failed", "user_id", userID, "error", err)
	}
	return n
}

// RecallUserMemories 召回某用户（含其 Agent 专属与全局）生效中的记忆。
//
// 条件：status = active 且 valid_until IS NULL（后者是语义真相，前者是索引友好的等价条件，
// 两者同时保留以防未来有路径只改其中之一）；episodicSince 非零时，早于该时刻的
// episodic 记忆不再召回（经历会过期，事实与偏好不会）。
//
// 隔离要求：userID 为 0 时【放弃召回并告警】，不返回任何数据。
func (m *TaskManager) RecallUserMemories(userID uint, agentID string, types []string, episodicSince time.Time, limit int) []*model.UserMemory {
	clause, args, ok := memoryScopeWhere(userID, agentID)
	if !ok {
		metrics.MemoryRecallFailClosed.Inc()
		slog.Error("[Memory][ALERT] recall rejected: empty user_id (fail-closed)")
		return nil
	}

	query := m.db.Model(&model.UserMemory{}).
		Where(clause, args...).
		Where("status = ?", model.MemoryStatusActive).
		Where("valid_until IS NULL")
	if len(types) > 0 {
		query = query.Where("type IN ?", types)
	}
	if !episodicSince.IsZero() {
		// 只对 episodic 施加时间窗：NOT (type='episodic' AND valid_from < cutoff)
		query = query.Where("(type <> ? OR valid_from >= ?)", model.MemoryTypeEpisodic, episodicSince)
	}

	// 排序：置信度优先，其次最近更新。刻意不用 last_hit_at 参与排序——
	// 否则"被召回的更容易再被召回"，会形成自我强化的偏置（Phase C 引入衰减时再统一处理）。
	var list []*model.UserMemory
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Order("confidence desc").Order("updated_at desc").Find(&list).Error; err != nil {
		slog.Error("recall user memories failed", "user_id", userID, "agent_id", agentID, "error", err)
		return nil
	}
	return list
}

// TouchUserMemories 累加命中次数并记录最近命中时间（尽力而为，失败只告警）。
// 该数据用于后续的衰减与淘汰策略，不参与本次召回结果。
//
// 刻意用 UpdateColumns 而不是 Updates：后者会自动写 updated_at，而召回排序依赖 updated_at，
// 于是"被召回的更容易再被召回"，形成自我强化的偏置。命中计数不应改变内容的更新时序。
func (m *TaskManager) TouchUserMemories(ids []string) {
	if len(ids) == 0 {
		return
	}
	now := time.Now()
	if err := m.db.Model(&model.UserMemory{}).
		Where("id IN ?", ids).
		UpdateColumns(map[string]any{
			"hit_count":   gorm.Expr("hit_count + 1"),
			"last_hit_at": now,
		}).Error; err != nil {
		slog.Warn("touch user memories failed", "count", len(ids), "error", err)
	}
}
