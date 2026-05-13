# Go 语言数据库实战指南：GORM 与 go-redis 

在现代后端开发中，**MySQL** 负责持久化存储重要数据，而 **Redis** 负责高速缓存和应对高并发。
Go 社区中最主流的两个库分别是操作 MySQL 的 **GORM** 和操作 Redis 的 **go-redis**。

这份文档将带你快速掌握它们的核心用法，并结合我们项目中的 `manager.go` 源码，解释它们是如何协同工作的。
（关于 MySQL 事务和隔离级别的理论原理解析，请参考单独的 [MySQL 原理文档](mysql_explanation_guide.md)）

---

## 1. MySQL 魔法师：GORM 框架

GORM 是 Go 语言中最受欢迎的 ORM（对象关系映射）库。它的核心思想是：**你只需要操作 Go 的结构体（Struct），GORM 会自动帮你翻译并执行底层的 SQL 语句。**

### 1.1 定义表结构 (Model)
在 GORM 中，一个 `struct` 就是一张表，结构体里的字段就是表里的列。
通过 `gorm:""` 标签，你可以定义主键、数据类型、甚至索引。

```go
type User struct {
    // 定义 ID 为主键，类型为 varchar(36)
    ID   string `gorm:"primaryKey;type:varchar(36)"` 
    // 定义 Name，并加上索引加速查询
    Name string `gorm:"index"`                       
    Age  int
}
```

### 1.2 自动建表 (AutoMigrate)
以前写后端，得先去数据库里写 `CREATE TABLE...`。有了 GORM，你只需要在启动时调用：
```go
db.AutoMigrate(&User{}) 
```
GORM 会自动检查数据库，如果没有表就建表，如果结构体新增了字段，它会自动 `ALTER TABLE` 加上新列！

### 1.3 增删改查 (CRUD) 核心方法

记住这四个最常用的动作：

**创建 (Create)**
```go
newUser := User{ID: "123", Name: "Tom", Age: 18}
db.Create(&newUser) // 相当于 INSERT INTO users ...
```

**查询 (Read)**
```go
var u User
// 根据主键或条件查询第一条记录
db.Where("id = ?", "123").First(&u) 
// 相当于 SELECT * FROM users WHERE id = '123' LIMIT 1

// 查询多条记录
var users []User
db.Where("age > ?", 10).Find(&users)
```

**更新 (Update/Save)**
```go
// 先查出来
db.Where("id = ?", "123").First(&u)
// 修改数据
u.Age = 19
// Save 会保存结构体的所有字段（相当于全量覆盖更新）
db.Save(&u) 
```

**删除 (Delete)**
```go
db.Where("id = ?", "123").Delete(&User{})
```

---

## 2. 内存闪电：go-redis 框架

Redis 是基于内存的键值对（Key-Value）数据库。在 Go 中，我们通常使用 `github.com/redis/go-redis/v9`。

### 2.1 基础概念：上下文 (Context)
在 Go 中调用 Redis 方法时，第一个参数永远是 `ctx` (Context)。它用于控制超时和取消请求。对于简单场景，你可以直接用 `context.Background()`。

### 2.2 核心方法：Set 和 Get

Redis 最常用的就是存字符串和取字符串。但我们通常要把 Go 的**结构体存进去**，这该怎么办？
答案是：**先转成 JSON 字符串，再存入 Redis！**

**存入数据 (Set)**
```go
// 1. 把 Go 结构体变成 JSON 字节流
data, _ := json.Marshal(user)

// 2. 存入 Redis，并设置过期时间为 24 小时
// 参数：上下文, 键名(Key), 值(Value), 过期时间
rdb.Set(ctx, "user:123", data, 24*time.Hour)
```

**读取数据 (Get)**
```go
// 1. 从 Redis 中读取字符串
val, err := rdb.Get(ctx, "user:123").Result()

if err == nil {
    var u User
    // 2. 把 JSON 字符串反解析回 Go 结构体
    json.Unmarshal([]byte(val), &u)
    fmt.Println(u.Name)
}
```

---

## 3. 实战演练：MySQL 与 Redis 强强联手 (Cache-Aside 模式)

在我们的 [internal/queue/manager.go](../internal/queue/manager.go) 中，完美展示了业界最经典的 **Cache-Aside（旁路缓存）** 模式。

### 场景一：查询任务 `GetTask(id)`
想象一下，如果每一秒钟有 1 万个人在查询同一个任务的进度，如果全去查 MySQL，数据库瞬间就会崩溃。

**我们的做法：**
1. **先查 Redis**：找找有没有 `task:123`。如果有，直接返回！（这只需要不到 1 毫秒）
2. **Redis 没有（缓存未命中）**：再去 MySQL 里老老实实查 `db.Where("id=?", id).First(&t)`。
3. **回写缓存**：把 MySQL 查到的数据塞回 Redis，这样下一次查询就能直接命中缓存了。

### 场景二：更新任务 `UpdateTask(t)`
当 Worker 把任务处理完，状态从 `processing` 变成 `completed` 时，我们需要更新数据。

**我们的做法：**
1. **先更新 MySQL**：保证数据绝对不会丢失，落盘为安 (`db.Save(t)`)。
2. **立刻更新 Redis**：把最新的数据覆盖到 Redis 中 (`m.cacheTask(t)`)。这样前端下一次来轮询时，拿到的就是最新的状态。

### 总结
- **MySQL** 是“老实人账本”，记下来就不会丢，但翻看速度慢。
- **Redis** 是“便签纸”，看一眼就能拿到，但断电就没了。
- 把它们结合起来，用 GORM 操作账本，用 go-redis 贴便签，你就能写出支撑千万级用户的后端系统了！