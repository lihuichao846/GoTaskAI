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

	// PriceInUSDPerMTok / PriceOutUSDPerMTok 为模型输入/输出单价（美元 / 百万 token）。
	// 两者未配置（为 0）时 CostUSD 返回 0，表示未启用费用折算。
	PriceInUSDPerMTok  float64
	PriceOutUSDPerMTok float64

	// Calls / TokensIn / TokensOut 由 generateWithClient 在每次模型调用后累计（本次运行）。
	Calls     int
	TokensIn  int
	TokensOut int

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

// CostUSD 按本次运行的 token 数与配置单价折算美元成本；单价未配置时返回 0。
func (b *Budget) CostUSD() float64 {
	if b == nil {
		return 0
	}
	return float64(b.TokensIn)*b.PriceInUSDPerMTok/1e6 +
		float64(b.TokensOut)*b.PriceOutUSDPerMTok/1e6
}
