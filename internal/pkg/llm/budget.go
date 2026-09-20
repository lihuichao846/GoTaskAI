package llm

import "fmt"

// Budget 描述一次任务运行的成本预算：模型调用次数上限与 token 计量。
// 由 Worker 在每次任务运行前构造并注入；单次运行的循环上限用于防止工具乒乓
// （模型反复要求调用工具导致无限循环），任务级总上限则跨 asynq 重试累计，
// 防止队列级重试把成本放大。
type Budget struct {
	// MaxCalls 为单次运行允许的最大模型调用次数；<=0 表示不限制。
	MaxCalls int
	// MaxTotalCalls 为任务跨重试累计允许的最大模型调用次数；<=0 表示不限制。
	MaxTotalCalls int

	// PriceInUSDPerMTok / PriceInUSDPerMTokCached / PriceOutUSDPerMTok 为模型
	// 输入（未命中缓存）/ 输入（命中缓存）/ 输出单价（美元 / 百万 token）。
	// 三者未配置（为 0）时 CostUSD 返回 0，表示未启用费用折算；
	// PriceInUSDPerMTokCached 为 0 而 PriceInUSDPerMTok 非 0 时，命中部分按未命中单价计（不分档）。
	PriceInUSDPerMTok       float64
	PriceInUSDPerMTokCached float64
	PriceOutUSDPerMTok      float64

	// Calls / TokensIn / TokensOut / TokensCached 由 generateWithClient 在每次模型调用后累计（本次运行）。
	// TokensCached 为 TokensIn 中命中 prompt 缓存的部分（恒 <= TokensIn）。
	Calls        int
	TokensIn     int
	TokensOut    int
	TokensCached int

	// baseCalls / baseTokensIn / baseTokensOut 为历史重试已累计的计量，用于跨重试上限判断。
	baseCalls     int
	baseTokensIn  int
	baseTokensOut int
}

// ErrBudgetExceeded 表示模型调用次数已耗尽预算，调用方应终止当前运行。
var ErrBudgetExceeded = fmt.Errorf("budget exceeded: max model calls reached")

// SetBase 设置历史重试已累计的计量（跨重试累计续算），使 BeforeCall 能感知总量。
func (b *Budget) SetBase(calls, tokensIn, tokensOut int) {
	if b == nil {
		return
	}
	b.baseCalls = calls
	b.baseTokensIn = tokensIn
	b.baseTokensOut = tokensOut
}

// BeforeCall 在每次模型调用前检查预算；返回 nil 表示允许继续。
func (b *Budget) BeforeCall() error {
	if b == nil {
		return nil
	}
	if b.MaxCalls > 0 && b.Calls >= b.MaxCalls {
		return ErrBudgetExceeded
	}
	if b.MaxTotalCalls > 0 && b.baseCalls+b.Calls >= b.MaxTotalCalls {
		return ErrBudgetExceeded
	}
	return nil
}

// Record 记录一次模型调用的 token 消耗，并累加本次运行调用次数。
func (b *Budget) Record(tokensIn, tokensOut int) {
	if b == nil {
		return
	}
	b.Calls++
	b.TokensIn += tokensIn
	b.TokensOut += tokensOut
}

// RecordWithCache 在 Record 的基础上额外记录本次输入中命中 prompt 缓存的部分。
// cached <= 0 时等价于 Record；cached 会被夹紧到 tokensIn 以内。
func (b *Budget) RecordWithCache(tokensIn, tokensOut, cached int) {
	if b == nil {
		return
	}
	b.Record(tokensIn, tokensOut)
	if cached <= 0 {
		return
	}
	if cached > tokensIn {
		cached = tokensIn
	}
	b.TokensCached += cached
}

// AddUsage 仅累加 token 计量（**不增加调用次数**），用于把"旁路调用"的成本并入主计量。
//
// 典型场景：上下文压缩使用独立的调用预算（不占用主任务的 MaxCalls 配额），
// 但它的 token 消耗是真实成本，必须计入任务账本，否则 CostUSD 会系统性低估。
func (b *Budget) AddUsage(tokensIn, tokensOut, cached int) {
	if b == nil {
		return
	}
	b.TokensIn += tokensIn
	b.TokensOut += tokensOut
	if cached <= 0 {
		return
	}
	if cached > tokensIn {
		cached = tokensIn
	}
	b.TokensCached += cached
}

// TotalCalls 返回任务累计调用次数（历史 + 本次）。
func (b *Budget) TotalCalls() int {
	if b == nil {
		return 0
	}
	return b.baseCalls + b.Calls
}

// TotalTokensIn 返回任务累计输入 token（历史 + 本次）。
func (b *Budget) TotalTokensIn() int {
	if b == nil {
		return 0
	}
	return b.baseTokensIn + b.TokensIn
}

// TotalTokensOut 返回任务累计输出 token（历史 + 本次）。
func (b *Budget) TotalTokensOut() int {
	if b == nil {
		return 0
	}
	return b.baseTokensOut + b.TokensOut
}

// TotalTokensCached 返回本次运行中输入命中 prompt 缓存的 token 数。
func (b *Budget) TotalTokensCached() int {
	if b == nil {
		return 0
	}
	return b.TokensCached
}

// CostUSD 按本次运行的 token 数与配置单价折算美元成本；单价未配置时返回 0。
// 当配置了缓存命中单价（PriceInUSDPerMTokCached > 0）时，命中部分按该单价计，其余按未命中单价计。
func (b *Budget) CostUSD() float64 {
	if b == nil {
		return 0
	}
	cachedRate := b.PriceInUSDPerMTokCached
	if cachedRate <= 0 {
		cachedRate = b.PriceInUSDPerMTok // 未配置缓存单价：不分档
	}
	cached := b.TokensCached
	if cached > b.TokensIn {
		cached = b.TokensIn
	}
	fresh := b.TokensIn - cached
	return float64(fresh)*b.PriceInUSDPerMTok/1e6 +
		float64(cached)*cachedRate/1e6 +
		float64(b.TokensOut)*b.PriceOutUSDPerMTok/1e6
}
