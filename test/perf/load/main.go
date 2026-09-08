// 性能压测工具：针对 GoTaskAI API 的轻量读接口测量 QPS / 吞吐量 / 并发承载能力。
//
// 用法示例：
//
//	go run ./test/perf/load -url http://127.0.0.1:8080 -token <JWT> \
//	    -conc 1,5,10,20,40,80,120,160,200,300,400,500 -dur 10s -json out.json
//
// 对每个并发量级运行 -dur 时长的饱和压测（每个 worker 尽量发送请求），
// 输出：成功率、QPS、平均/p50/p95/p99 延迟、错误分类等量化数据。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Endpoint 描述一个被测接口。
type Endpoint struct {
	Method string
	Path   string
}

type Config struct {
	BaseURL   string
	Token     string
	Concs     []int
	Duration  time.Duration
	Endpoints []Endpoint
	Warmup    time.Duration
	Output    string
	Validate  bool
}

type Result struct {
	Concurrency   int     `json:"concurrency"`
	DurationSec   float64 `json:"duration_sec"`
	TotalReq      int64   `json:"total_req"`
	HTTP2xx       int64   `json:"http_2xx"`
	HTTP4xx       int64   `json:"http_4xx"`
	HTTP5xx       int64   `json:"http_5xx"`
	TransportErr  int64   `json:"transport_err"`
	QPS           float64 `json:"qps"`
	SuccessRate   float64 `json:"success_rate"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	MinLatencyMs  float64 `json:"min_latency_ms"`
	MaxLatencyMs  float64 `json:"max_latency_ms"`
	P50Ms         float64 `json:"p50_ms"`
	P95Ms         float64 `json:"p95_ms"`
	P99Ms         float64 `json:"p99_ms"`
}

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "API base URL")
	token := flag.String("token", "", "JWT token (read from GTA_TOKEN env if empty)")
	concFlag := flag.String("conc", "1,10,50,100,200,400,800", "comma-separated concurrency levels")
	durFlag := flag.String("dur", "8s", "per-level test duration (e.g. 8s, 10s)")
	warmup := flag.String("warmup", "2s", "warmup duration before measurement")
	output := flag.String("out", "", "output JSON file path (empty = stdout only)")
	valid := flag.Bool("validate", false, "only validate auth and endpoints, then exit")
	flag.Parse()

	if *token == "" {
		*token = os.Getenv("GTA_TOKEN")
	}

	cfg := &Config{
		BaseURL:  strings.TrimRight(*url, "/"),
		Token:    *token,
		Concs:    parseConc(*concFlag),
		Duration: parseDur(*durFlag),
		Warmup:   parseDur(*warmup),
		Output:   *output,
		Validate: *valid,
		Endpoints: []Endpoint{
			{Method: http.MethodGet, Path: "/api/tasks"},
			{Method: http.MethodGet, Path: "/api/conversations"},
			{Method: http.MethodGet, Path: "/api/agents"},
			{Method: http.MethodGet, Path: "/api/tools"},
			{Method: http.MethodGet, Path: "/api/knowledge-bases"},
		},
	}

	if err := cfg.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func parseConc(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func parseDur(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 8 * time.Second
	}
	return d
}

func (c *Config) Run() error {
	if c.Token == "" {
		return fmt.Errorf("missing token (pass -token or set GTA_TOKEN)")
	}

	// 预检：校验鉴权与端点可达。
	for _, ep := range c.Endpoints {
		code, err := c.request(ep)
		if err != nil {
			return fmt.Errorf("endpoint %s %s not reachable: %w", ep.Method, c.BaseURL+ep.Path, err)
		}
		if code != http.StatusOK && code != http.StatusNoContent {
			fmt.Printf("[WARN] %s %s returned HTTP %d (auth may be wrong)\n", ep.Method, ep.Path, code)
		} else {
			fmt.Printf("[OK] %s %s -> HTTP %d\n", ep.Method, ep.Path, code)
		}
	}

	if c.Validate {
		fmt.Println("[validate] OK, exiting")
		return nil
	}

	results := make([]Result, 0, len(c.Concs))
	for _, conc := range c.Concs {
		r := c.runLevel(conc)
		results = append(results, r)
		printResult(r)
	}

	if c.Output != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(c.Output, data, 0644); err != nil {
			return err
		}
		fmt.Printf("\nresults written to %s\n", c.Output)
	}
	return nil
}

func (ep Endpoint) BaseURL() string { return "" } // unused helper

func (c *Config) request(ep Endpoint) (int, error) {
	req, err := http.NewRequest(ep.Method, c.BaseURL+ep.Path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// httpClient 创建一个连接池充足、长短连接兼顾的客户端。
func (c *Config) httpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        2000,
			MaxIdleConnsPerHost: 1000,
			MaxConnsPerHost:     0, // 不限制单主机并发连接数
			IdleConnTimeout:     90 * time.Second,
		},
		// 不设整体超时：由压测器自己度量延迟，避免超时掩盖真实瓶颈。
	}
}

// runLevel 在给定并发数下执行饱和压测。
func (c *Config) runLevel(conc int) Result {
	client := c.httpClient()
	end := make(chan struct{})
	var wg sync.WaitGroup

	var total, http2xx, http4xx, http5xx, transportErr int64
	var latMu sync.Mutex
	var latencies []float64

	// 预热：先以当前并发数跑热身，避免冷启动（DB 连接池、JIT 等）污染指标。
	warmEnd := time.Now().Add(c.Warmup)
	var warmWG sync.WaitGroup
	for i := 0; i < conc; i++ {
		warmWG.Add(1)
		go func() {
			defer warmWG.Done()
			for time.Now().Before(warmEnd) {
				c.sendOne(client, end, &total, &http2xx, &http4xx, &http5xx, &transportErr, &latMu, &latencies, false)
			}
			// 预热结束后，将预热期计数清零（不计入正式测量）。
		}()
	}
	warmWG.Wait()
	// 清零预热期累积数据（latencies 与计数全部重置）。
	atomic.StoreInt64(&total, 0)
	atomic.StoreInt64(&http2xx, 0)
	atomic.StoreInt64(&http4xx, 0)
	atomic.StoreInt64(&http5xx, 0)
	atomic.StoreInt64(&transportErr, 0)
	latMu.Lock()
	latencies = latencies[:0]
	latMu.Unlock()

	measureStart := time.Now()
	runEnd := measureStart.Add(c.Duration)
	for i := 0; i < conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(runEnd) {
				c.sendOne(client, end, &total, &http2xx, &http4xx, &http5xx, &transportErr, &latMu, &latencies, true)
			}
		}()
	}
	wg.Wait()
	close(end)

	dur := time.Since(measureStart).Seconds()

	res := Result{Concurrency: conc, DurationSec: dur}
	res.TotalReq = atomic.LoadInt64(&total)
	res.HTTP2xx = atomic.LoadInt64(&http2xx)
	res.HTTP4xx = atomic.LoadInt64(&http4xx)
	res.HTTP5xx = atomic.LoadInt64(&http5xx)
	res.TransportErr = atomic.LoadInt64(&transportErr)
	res.QPS = float64(res.TotalReq) / dur

	success := res.HTTP2xx
	res.SuccessRate = percent(success, res.TotalReq)

	latMu.Lock()
	sorted := append([]float64(nil), latencies...)
	latMu.Unlock()
	if len(sorted) > 0 {
		sort.Float64s(sorted)
		res.AvgLatencyMs = avg(sorted)
		res.MinLatencyMs = sorted[0]
		res.MaxLatencyMs = sorted[len(sorted)-1]
		res.P50Ms = pct(sorted, 0.50)
		res.P95Ms = pct(sorted, 0.95)
		res.P99Ms = pct(sorted, 0.99)
	}
	return res
}

// sendOne 发送单次请求并记录结果。end 通道当前仅用于标识（保留 API 对称性）。
func (c *Config) sendOne(client *http.Client, end chan struct{}, total, h2, h4, h5, terr *int64, latMu *sync.Mutex, lat *[]float64, measure bool) {
	ep := c.Endpoints[randIdx()]
	req, err := http.NewRequest(ep.Method, c.BaseURL+ep.Path, nil)
	if err != nil {
		atomic.AddInt64(terr, 1)
		atomic.AddInt64(total, 1)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	t0 := time.Now()
	resp, err := client.Do(req)
	dur := float64(time.Since(t0).Milliseconds())
	if err != nil {
		atomic.AddInt64(terr, 1)
		atomic.AddInt64(total, 1)
		if measure {
			latMu.Lock()
			*lat = append(*lat, dur)
			latMu.Unlock()
		}
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	atomic.AddInt64(total, 1)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		atomic.AddInt64(h2, 1)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		atomic.AddInt64(h4, 1)
	default:
		atomic.AddInt64(h5, 1)
	}
	if measure {
		latMu.Lock()
		*lat = append(*lat, dur)
		latMu.Unlock()
	}
}

// randIdx 轮询返回一个随机端点下标（简单取模即可，无需真随机）。
var reqCounter int64

func randIdx() int {
	n := atomic.AddInt64(&reqCounter, 1)
	return int(n) % 5 // 与 Endpoints 长度一致
}

func percent(v, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(v) / float64(total) * 100
}

func avg(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func printResult(r Result) {
	fmt.Printf("===== concurrency=%d =====\n", r.Concurrency)
	fmt.Printf("  total=%d  2xx=%d  4xx=%d  5xx=%d  transport_err=%d\n", r.TotalReq, r.HTTP2xx, r.HTTP4xx, r.HTTP5xx, r.TransportErr)
	fmt.Printf("  QPS=%.1f  success=%.2f%%\n", r.QPS, r.SuccessRate)
	fmt.Printf("  latency(ms): avg=%.1f min=%.1f max=%.1f p50=%.1f p95=%.1f p99=%.1f\n",
		r.AvgLatencyMs, r.MinLatencyMs, r.MaxLatencyMs, r.P50Ms, r.P95Ms, r.P99Ms)
}
