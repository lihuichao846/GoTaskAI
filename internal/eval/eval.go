// Package eval 提供 Agent 评估 harness：对一组评测用例做固定采样、多次独立运行，
// 汇总统计稳定的质量报告（均值、方差、置信区间），作为后续改动的回归护栏。
package eval

import (
	"context"
	"math"
	"time"

	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/toolregistry"
)

// Case 表示一个评测用例。
type Case struct {
	Question       string   `json:"question"`
	ExpectedTools  []string `json:"expected_tools,omitempty"`  // 期望被调用的工具名，可为空
	ExpectedAnswer string   `json:"expected_answer,omitempty"` // 期望答案（用于语义相似度评测），可为空
}

// Config 描述一次评测运行的配置。
type Config struct {
	Model      string        `json:"model"`
	Sampling   *llm.Sampling `json:"sampling"`   // 固定采样配置，降低输出随机性
	Iterations int           `json:"iterations"` // 每个用例独立运行次数 N
}

// CaseReport 表示单个用例的统计结果。
type CaseReport struct {
	Question        string    `json:"question"`
	Iterations      int       `json:"iterations"`
	Errors          int       `json:"errors"`                        // 运行失败（API 错误/超时）次数
	EmbeddingErrors int       `json:"embedding_errors"`              // embedding 计算失败次数（不计入 0 分）
	ToolHitRates    []float64 `json:"tool_hit_rates"`                // 每次运行的工具命中率
	ToolMean        float64   `json:"tool_mean"`                     // 工具命中率均值
	ToolVariance    float64   `json:"tool_variance"`                 // 工具命中率样本方差
	ToolStdDev      float64   `json:"tool_std_dev"`                  // 工具命中率样本标准差
	ToolCIHalf      float64   `json:"tool_ci_half"`                  // 工具命中率 95% CI 半宽（已裁剪至 [0,1]）
	AnswerSims      []float64 `json:"answer_similarities,omitempty"` // 每次运行的答案相似度
	AnswerMean      float64   `json:"answer_mean,omitempty"`         // 答案相似度均值
	AnswerVariance  float64   `json:"answer_variance,omitempty"`     // 答案相似度方差
	AnswerCIHalf    float64   `json:"answer_ci_half,omitempty"`      // 答案相似度 95% CI 半宽
}

// Report 表示一次评测运行的完整报告。
type Report struct {
	Config    Config       `json:"config"`
	Cases     []CaseReport `json:"cases"`
	CreatedAt time.Time    `json:"created_at"`
}

// Runner 执行评测运行。
type Runner struct {
	llmClient    *llm.Client
	tools        []toolregistry.Tool
	systemPrompt string
	apiKey       string
	baseURL      string
}

// NewRunner 创建一个评测运行器。
func NewRunner(llmClient *llm.Client, tools []toolregistry.Tool, systemPrompt, apiKey, baseURL string) *Runner {
	return &Runner{
		llmClient:    llmClient,
		tools:        tools,
		systemPrompt: systemPrompt,
		apiKey:       apiKey,
		baseURL:      baseURL,
	}
}

// Run 对每个用例独立运行 cfg.Iterations 次，计算工具命中率与答案相似度并汇总统计。
// 返回统计稳定的质量报告；同输入多次运行，指标波动应落在置信区间内。
func (r *Runner) Run(ctx context.Context, cases []Case, cfg Config) (*Report, error) {
	if cfg.Iterations <= 0 {
		cfg.Iterations = 5
	}

	report := &Report{
		Config:    cfg,
		CreatedAt: time.Now(),
	}

	for _, c := range cases {
		report.Cases = append(report.Cases, r.runCase(ctx, c, cfg))
	}
	return report, nil
}

// runCase 对单个用例运行 N 次，收集每次工具命中率与答案相似度并统计。
func (r *Runner) runCase(ctx context.Context, c Case, cfg Config) CaseReport {
	cr := CaseReport{
		Question:   c.Question,
		Iterations: cfg.Iterations,
	}

	// 预计算期望答案 embedding，避免每次运行重复计算；失败时计入 EmbeddingErrors，答案评测跳过。
	var expectedVec []float32
	if c.ExpectedAnswer != "" {
		var err error
		expectedVec, err = r.llmClient.CreateEmbedding(ctx, c.ExpectedAnswer)
		if err != nil {
			cr.EmbeddingErrors++
		}
	}

	for i := 0; i < cfg.Iterations; i++ {
		toolRate, answerSim, embFailed, err := r.runOnce(ctx, c, cfg, expectedVec)
		if err != nil {
			cr.Errors++
			continue
		}
		if embFailed {
			cr.EmbeddingErrors++
		}
		cr.ToolHitRates = append(cr.ToolHitRates, toolRate)
		// 仅当期望答案向量成功计算、且本轮 answerVec 未失败时，才计入相似度（避免 0 分污染）。
		if len(expectedVec) > 0 && !embFailed {
			cr.AnswerSims = append(cr.AnswerSims, answerSim)
		}
	}

	cr.ToolMean, cr.ToolVariance, cr.ToolStdDev, cr.ToolCIHalf = stats(cr.ToolHitRates)
	if len(expectedVec) > 0 {
		cr.AnswerMean, cr.AnswerVariance, _, cr.AnswerCIHalf = stats(cr.AnswerSims)
	}
	return cr
}

// runOnce 运行一次用例，返回工具命中率、答案相似度与 embedding 失败标记。
func (r *Runner) runOnce(ctx context.Context, c Case, cfg Config, expectedVec []float32) (toolRate float64, answerSim float64, embFailed bool, err error) {
	called := make(map[string]bool)
	observer := func(obs llm.ToolCallObservation) {
		// 仅将执行成功的工具调用计为命中；失败/参数错误不计入命中。
		if obs.Status == "success" {
			called[obs.ToolName] = true
		}
	}

	answer, err := r.llmClient.GenerateWithToolsAndObserverSampling(
		ctx,
		r.apiKey, r.baseURL, cfg.Model,
		r.systemPrompt, nil, c.Question,
		r.tools, observer, nil, cfg.Sampling,
	)
	if err != nil {
		return 0, 0, false, err
	}

	toolRate = 1.0
	if len(c.ExpectedTools) > 0 {
		hits := 0
		for _, name := range c.ExpectedTools {
			if called[name] {
				hits++
			}
		}
		toolRate = float64(hits) / float64(len(c.ExpectedTools))
	}

	answerSim = 0.0
	if len(expectedVec) > 0 {
		vec, verr := r.llmClient.CreateEmbedding(ctx, answer)
		if verr != nil {
			embFailed = true
		} else {
			answerSim = cosineSimilarity(expectedVec, vec)
		}
	}
	return toolRate, answerSim, embFailed, nil
}

// stats 计算样本均值、样本方差、样本标准差与 95% 置信区间半宽。
// 半宽会被裁剪，确保 [mean-half, mean+half] 落在 [0,1] 内（指标均为 [0,1] 比例）。
func stats(values []float64) (mean, variance, stdDev, halfWidth float64) {
	n := len(values)
	if n == 0 {
		return 0, 0, 0, 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean = sum / float64(n)
	if n <= 1 {
		return mean, 0, 0, 0
	}
	var sq float64
	for _, v := range values {
		d := v - mean
		sq += d * d
	}
	variance = sq / float64(n-1)
	stdDev = math.Sqrt(variance)
	halfWidth = 1.96 * stdDev / math.Sqrt(float64(n))
	// 裁剪 CI 半宽，避免区间越界 [0,1]。
	if halfWidth > mean {
		halfWidth = mean
	}
	if halfWidth > 1-mean {
		halfWidth = 1 - mean
	}
	return mean, variance, stdDev, halfWidth
}

// cosineSimilarity 计算两个向量的余弦相似度。
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
