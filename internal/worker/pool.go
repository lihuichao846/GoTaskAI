package worker

import (
	"fmt"
	"gotaskai/internal/model"
	"gotaskai/internal/queue"
	"log"
	"time"
)

// Pool 维护一个并发的 Worker 池，用于持续消费队列中的任务
type Pool struct {
	workers int                // Worker 的并发数量
	manager *queue.TaskManager // 任务管理器实例的引用，用于更新状态和重新入队
}

// NewPool 创建一个具备指定并发数的 Worker 池
func NewPool(workers int, manager *queue.TaskManager) *Pool {
	return &Pool{
		workers: workers,
		manager: manager,
	}
}

// Start 启动所有 Worker 协程，开始监听并处理队列
func (p *Pool) Start() {
	for i := 0; i < p.workers; i++ {
		// 为每个 Worker 启动一个独立的 Goroutine
		go p.worker(i)
	}
	log.Printf("Started worker pool with %d workers\n", p.workers)
}

// worker 是单个消费者的执行逻辑，通过优先级规则从多个 Channel 获取任务
func (p *Pool) worker(id int) {
	for {
		var task *model.Task

		// 1. 先尝试非阻塞获取高优先级任务
		select {
		case task = <-p.manager.HighQueue:
		default:
			// 2. 如果高优先级为空，尝试非阻塞获取普通优先级任务
			select {
			case task = <-p.manager.NormalQueue:
			default:
				// 3. 都为空，尝试非阻塞获取低优先级任务
				select {
				case task = <-p.manager.LowQueue:
				default:
					// 4. 如果所有队列都为空，进入阻塞等待状态。
					// 这里的 select 可能会随机挑选一个准备好的 channel，但只要队列开始积压，
					// 下一轮循环的前几步非阻塞检查就会保证高优先级的绝对优先。
					select {
					case task = <-p.manager.HighQueue:
					case task = <-p.manager.NormalQueue:
					case task = <-p.manager.LowQueue:
					}
				}
			}
		}

		if task != nil {
			p.processTask(id, task)
		}
	}
}

// processTask 模拟具体的 AI 任务执行过程，包含状态流转与重试机制
func (p *Pool) processTask(workerID int, t *model.Task) {
	// 在真正开始处理前，再检查一次数据库里的最新状态（防止在队列等待期间被用户取消了）
	latestTask, ok := p.manager.GetTask(t.ID)
	if ok && latestTask.Status == model.StatusCancelled {
		log.Printf("[Worker %d] Task %s was cancelled before processing, skipping", workerID, t.ID)
		return
	}

	// 1. 将任务状态标记为"处理中"并同步到内存管理器
	t.Status = model.StatusProcessing
	p.manager.UpdateTask(t)

	log.Printf("[Worker %d] Processing task %s (%s)", workerID, t.ID, t.Type)

	// 2. 模拟 AI 任务执行的耗时操作 (例如调用外部大模型 API、网络请求等)
	// 将原本的 sleep 10 秒拆分成多次小的 sleep，以便在执行过程中也能响应取消信号
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)
		
		// 每次迭代检查一下任务是否被取消了
		checkTask, ok := p.manager.GetTask(t.ID)
		if ok && checkTask.Status == model.StatusCancelled {
			log.Printf("[Worker %d] Task %s was cancelled during processing, aborting", workerID, t.ID)
			return
		}
	}

	// 3. 模拟失败与自动重试机制
	// （测试逻辑：如果载荷内容为 "fail"，则强制触发失败逻辑）
	if t.Payload == "fail" && t.Retries < t.MaxRetry {
		t.Retries++
		t.Status = model.StatusPending // 状态回退为等待中
		t.Error = fmt.Sprintf("Simulated failure, retrying %d/%d", t.Retries, t.MaxRetry)
		p.manager.UpdateTask(t)
		log.Printf("[Worker %d] Task %s failed, re-queueing", workerID, t.ID)

		// 异步将任务重新推入相应的优先级队列
		// 使用异步并加上休眠退避(Backoff)机制，既防止队列满时阻塞当前 Worker，也给系统喘息时间
		go func(taskToRetry *model.Task) {
			time.Sleep(1 * time.Second)
			p.manager.Enqueue(taskToRetry)
		}(t)
		return

	} else if t.Payload == "fail" {
		// 重试次数已达上限，标记任务为永久失败
		t.Status = model.StatusFailed
		t.Error = "Max retries reached"
		p.manager.UpdateTask(t)
		log.Printf("[Worker %d] Task %s failed permanently", workerID, t.ID)
		return
	}

	// 4. 任务处理成功
	t.Status = model.StatusCompleted
	t.Result = fmt.Sprintf("Processed %s successfully: %s", t.Type, t.Payload)
	t.Error = ""
	p.manager.UpdateTask(t)
	log.Printf("[Worker %d] Task %s completed", workerID, t.ID)
}
