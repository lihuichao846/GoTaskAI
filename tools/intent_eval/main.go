// intent_eval 是意图路由（检索开关）的评测入口：对标注数据集运行路由，
// 统计「检索决策准确率」「漏检率（安全指标）」「误检索率（成本指标）」与决策来源分布，
// 并可作为 CI 回归闸门：准确率低于 -threshold 或漏检率高于 -max-miss 时以非零退出码结束。
//
// 用法：
//
//	go run ./tools/intent_eval -mode rule
//	go run ./tools/intent_eval -mode semantic -threshold 0.90 -max-miss 0.02
//	go run ./tools/intent_eval -mode hybrid
//
// 说明：数据集为「种子集」，需结合真实流量分布持续扩充与复核；指标全部为实测值，不做预估。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/intent"
	"gotaskai/internal/pkg/llm"
)

// evalCase 是一条标注用例：expected_retrieval 表示「是否需要执行本地检索（RAG/KAG）」。
type evalCase struct {
	Question          string `json:"question"`
	ExpectedRetrieval bool   `json:"expected_retrieval"`
	Note              string `json:"note,omitempty"`
}

// caseResult 是单条用例的多次运行结果与多数票结论。
type caseResult struct {
	Question   string   `json:"question"`
	Note       string   `json:"note,omitempty"`
	Expected   bool     `json:"expected_retrieval"`
	Actual     []bool   `json:"actual_retrieval"`
	Sources    []string `json:"sources"`
	Majority   bool     `json:"majority_retrieval"`
	Consistent bool     `json:"consistent"`
}

// report 是一次评测的完整报告。
type report struct {
	Mode         string         `json:"mode"`
	Iterations   int            `json:"iterations"`
	Total        int            `json:"total"`
	Accuracy     float64        `json:"accuracy"`
	MissRate     float64        `json:"retrieval_miss_rate"`
	FalseRate    float64        `json:"false_retrieval_rate"`
	Inconsistent int            `json:"inconsistent_cases"`
	SourceDist   map[string]int `json:"source_distribution"`
	Cases        []caseResult   `json:"cases"`
	CreatedAt    time.Time      `json:"created_at"`
}

func main() {
	casesPath := flag.String("cases", "tools/intent_eval/cases.json", "评测数据集路径")
	outPath := flag.String("out", "", "报告输出路径（JSON，UTF-8）；为空则仅打印到 stdout")
	mode := flag.String("mode", "", "覆盖配置中的路由模式：rule | semantic | hybrid（为空则用配置文件值）")
	iterations := flag.Int("iterations", 3, "每条用例运行次数")
	threshold := flag.Float64("threshold", 0.90, "检索决策准确率下限，低于则评测失败")
	maxMiss := flag.Float64("max-miss", 0.02, "检索漏检率上限（安全指标），高于则评测失败")
	flag.Parse()

	config.InitConfig("config/config.yaml")

	// 评测必须真正启用路由，否则所有决策都会是 disabled。
	config.AppConfig.Intent.Enabled = true
	if *mode != "" {
		config.AppConfig.Intent.Mode = *mode
	}
	modeCfg := config.AppConfig.Intent

	cases, err := loadCases(*casesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load cases failed: %v\n", err)
		os.Exit(2)
	}

	llmClient := llm.NewClient()

	// 语义层需要原型向量；评测时同步预热，确保命中率统计有效。
	var semantic *intent.SemanticClassifier
	if modeCfg.Mode == intent.ModeSemantic || modeCfg.Mode == intent.ModeHybrid {
		semantic = intent.NewSemanticClassifier(llmClient, modeCfg.SemanticHigh, modeCfg.SemanticMargin, modeCfg.SemanticAllowClose)
		warmCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		if err := semantic.Warmup(warmCtx); err != nil {
			cancel()
			fmt.Fprintf(os.Stderr, "semantic warmup failed: %v\n", err)
			os.Exit(2)
		}
		cancel()
	}

	router := intent.New(intent.Config{
		Enabled:            true,
		Mode:               modeCfg.Mode,
		SemanticHigh:       modeCfg.SemanticHigh,
		SemanticMargin:     modeCfg.SemanticMargin,
		SemanticAllowClose: modeCfg.SemanticAllowClose,
		LLMMinConfidence:   modeCfg.LLMMinConfidence,
		EnableToolRouting:  modeCfg.EnableToolRouting,
	}, llmClient, semantic)

	rep := run(router, cases, *iterations, modeCfg.Mode)

	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal report failed: %v\n", err)
		os.Exit(2)
	}
	fmt.Println(string(out))

	// 可选：把同一份报告写入文件（UTF-8），作为评测留档证据。
	if *outPath != "" {
		if werr := os.WriteFile(*outPath, out, 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "write report failed: %v\n", werr)
			os.Exit(2)
		}
	}

	// 回归护栏：安全指标优先，其次整体准确率。
	if rep.MissRate > *maxMiss {
		fmt.Fprintf(os.Stderr, "eval FAILED: retrieval_miss_rate=%.3f above max %.3f\n", rep.MissRate, *maxMiss)
		os.Exit(1)
	}
	if rep.Accuracy < *threshold {
		fmt.Fprintf(os.Stderr, "eval FAILED: accuracy=%.3f below threshold %.3f\n", rep.Accuracy, *threshold)
		os.Exit(1)
	}
}

// run 对每条用例独立运行 iterations 次，按多数票汇总指标。
func run(router intent.Router, cases []evalCase, iterations int, mode string) *report {
	if iterations <= 0 {
		iterations = 3
	}

	rep := &report{
		Mode:       mode,
		Iterations: iterations,
		Total:      len(cases),
		SourceDist: map[string]int{},
		CreatedAt:  time.Now(),
	}

	hits, misses, falseHits := 0, 0, 0
	expectedPositives, expectedNegatives := 0, 0

	for _, c := range cases {
		cr := caseResult{Question: c.Question, Note: c.Note, Expected: c.ExpectedRetrieval, Consistent: true}
		trueCount := 0

		for i := 0; i < iterations; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			// HasKB/HasKAG 置 true：评测关注「路由判断」本身，避免能力短路掩盖判断结果。
			d := router.Route(ctx, intent.Request{
				Question: c.Question,
				HasKB:    true,
				HasKAG:   true,
			}, nil)
			cancel()

			got := d.NeedRAG || d.NeedKAG
			cr.Actual = append(cr.Actual, got)
			cr.Sources = append(cr.Sources, string(d.Source))
			rep.SourceDist[string(d.Source)]++
			if got {
				trueCount++
			}
		}

		cr.Majority = trueCount*2 > iterations
		for _, got := range cr.Actual {
			if got != cr.Actual[0] {
				cr.Consistent = false
			}
		}
		if !cr.Consistent {
			rep.Inconsistent++
		}

		if cr.Majority == c.ExpectedRetrieval {
			hits++
		}
		if c.ExpectedRetrieval {
			expectedPositives++
			if !cr.Majority {
				misses++
			}
		} else {
			expectedNegatives++
			if cr.Majority {
				falseHits++
			}
		}

		rep.Cases = append(rep.Cases, cr)
	}

	rep.Accuracy = ratio(hits, len(cases))
	rep.MissRate = ratio(misses, expectedPositives)
	rep.FalseRate = ratio(falseHits, expectedNegatives)
	return rep
}

// ratio 返回 a/b，b<=0 时返回 0，避免除零。
func ratio(a, b int) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// loadCases 读取标注数据集。
func loadCases(path string) ([]evalCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []evalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("empty case set: %s", path)
	}
	return cases, nil
}
