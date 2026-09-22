package queue

import (
	"os"
	"testing"
	"time"

	"gotaskai/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 需要真实 MySQL 的记忆数据访问实测。默认跳过（避免 CI 依赖外部服务），显式开启：
//
//	$env:GOTASKAI_TEST_DSN="root:root@tcp(127.0.0.1:3306)/gotaskai?charset=utf8mb4&parseTime=True&loc=Local"
//	go test -count=1 -run TestLiveMemory -v ./internal/queue/
//
// 它验证的是本模块最容易出错、且静态检查完全覆盖不到的两处：
//  1. 唯一索引 idx_mem_active(user_id, agent_id, active_key) 是否真的允许"多条失效行 + 一条生效行"；
//  2. 取代语义是否真的关窗而不删除（历史可查）。
func TestLiveMemory(t *testing.T) {
	dsn := os.Getenv("GOTASKAI_TEST_DSN")
	if dsn == "" {
		t.Skip("需要真实 MySQL：设置 GOTASKAI_TEST_DSN 后运行")
	}

	gdb, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	if err := gdb.AutoMigrate(&model.UserMemory{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}
	m := &TaskManager{db: gdb}

	const (
		userA = uint(900001)
		userB = uint(900002)
	)
	key := "live_test::answer_style"
	cleanup := func() {
		gdb.Unscoped().Where("user_id IN ?", []uint{userA, userB}).Delete(&model.UserMemory{})
	}
	cleanup()
	defer cleanup()

	// ① 首次写入：一条生效记录，active_key 就位
	first := &model.UserMemory{UserID: userA, MemKey: key, Type: model.MemoryTypePreference, Content: "偏好简洁回答"}
	if err := m.CreateUserMemory(first, 100); err != nil {
		t.Fatalf("写入第一条记忆失败: %v", err)
	}
	if first.ActiveKey == nil || *first.ActiveKey != key {
		t.Fatalf("生效记录的 active_key 应等于 mem_key，实际 %v", first.ActiveKey)
	}

	// ② 同 key 再写一条：旧记录必须被【关窗】而不是删除
	second := &model.UserMemory{UserID: userA, MemKey: key, Type: model.MemoryTypePreference, Content: "偏好详细解释"}
	if err := m.CreateUserMemory(second, 100); err != nil {
		t.Fatalf("写入第二条记忆失败（唯一索引可能未按 NULL 语义生效）: %v", err)
	}

	var all []model.UserMemory
	if err := gdb.Where("user_id = ? AND mem_key = ?", userA, key).Order("created_at asc").Find(&all).Error; err != nil {
		t.Fatalf("查询历史记忆失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("应保留 2 条（1 条失效 + 1 条生效），实际 %d", len(all))
	}
	if all[0].Status != model.MemoryStatusSuperseded || all[0].ValidUntil == nil || all[0].ActiveKey != nil {
		t.Fatalf("旧记录应被关窗（superseded + valid_until + active_key=NULL），实际 status=%s valid_until=%v active_key=%v",
			all[0].Status, all[0].ValidUntil, all[0].ActiveKey)
	}
	if all[1].Status != model.MemoryStatusActive || all[1].ValidUntil != nil {
		t.Fatalf("新记录应为生效状态，实际 status=%s valid_until=%v", all[1].Status, all[1].ValidUntil)
	}

	// ③ 召回只返回生效记录，且内容是新事实
	got := m.RecallUserMemories(userA, "", nil, time.Time{}, 10)
	if len(got) != 1 || got[0].Content != "偏好详细解释" {
		t.Fatalf("召回应只返回最新生效记录，实际 %v", got)
	}

	// ④ 隐私隔离：另一个用户必须召回不到任何东西
	if other := m.RecallUserMemories(userB, "", nil, time.Time{}, 10); len(other) != 0 {
		t.Fatalf("跨用户召回必须为空，实际 %v", other)
	}

	// ⑤ 删除权：删除后彻底不可召回、且行数归零
	rows, err := m.DeleteAllUserMemories(userA)
	if err != nil || rows != 2 {
		t.Fatalf("清空应删除 2 行，实际 rows=%d err=%v", rows, err)
	}
	if left := m.RecallUserMemories(userA, "", nil, time.Time{}, 10); len(left) != 0 {
		t.Fatalf("删除后不应召回任何记忆，实际 %v", left)
	}
}
