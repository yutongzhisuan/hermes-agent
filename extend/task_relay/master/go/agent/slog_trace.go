package agent

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type runTraceKey struct{}
type callStateKey struct{}

// runTraceState holds per-Master.Run correlation IDs and round counters.
// A pointer is stored in context so sequential Eino callbacks share mutable state.
type runTraceState struct {
	mu sync.Mutex

	runID string

	llmN     int64
	llmNSet  bool
	toolN    int64
	toolNSet bool

	llmCallID string
	curLLMN   int64
	model     string
}

// callState correlates one ChatModel/Tool invocation's start/end/error logs.
type callState struct {
	startedAt  time.Time
	runID      string
	llmCallID  string
	toolCallID string
	model      string
	llmN       int64
	toolN      int64
}

// withRunTrace ensures ctx carries a *runTraceState. If missing, creates one with a new run_id.
func withRunTrace(ctx context.Context) (context.Context, *runTraceState) {
	if st, ok := ctx.Value(runTraceKey{}).(*runTraceState); ok && st != nil {
		return ctx, st
	}
	st := &runTraceState{runID: uuid.NewString()}
	return context.WithValue(ctx, runTraceKey{}, st), st
}

func runIDFrom(ctx context.Context) string {
	if st, ok := ctx.Value(runTraceKey{}).(*runTraceState); ok && st != nil {
		return st.runID
	}
	return ""
}

func (st *runTraceState) nextLLMN() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.llmNSet {
		st.llmN = 0
		st.llmNSet = true
	} else {
		st.llmN++
	}
	st.curLLMN = st.llmN
	return st.llmN
}

func (st *runTraceState) nextToolN() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.toolNSet {
		st.toolN = 0
		st.toolNSet = true
	} else {
		st.toolN++
	}
	return st.toolN
}

func (st *runTraceState) setLLMCall(id, model string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.llmCallID = id
	if model != "" {
		st.model = model
	}
}

func (st *runTraceState) setModel(model string) {
	if model == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.model = model
}

func (st *runTraceState) parentLLM() (callID string, llmN int64, model string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.llmCallID, st.curLLMN, st.model
}

func (st *runTraceState) Model() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.model
}

// llmCalls returns how many ChatModel invocations happened in this run (0 if none).
func (st *runTraceState) llmCalls() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.llmNSet {
		return 0
	}
	return st.llmN + 1
}

// toolCalls returns how many Tool invocations happened in this run (0 if none).
func (st *runTraceState) toolCalls() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.toolNSet {
		return 0
	}
	return st.toolN + 1
}

func callStateFrom(ctx context.Context) *callState {
	cs, _ := ctx.Value(callStateKey{}).(*callState)
	return cs
}

func elapsedFrom(cs *callState) time.Duration {
	if cs == nil || cs.startedAt.IsZero() {
		return 0
	}
	return time.Since(cs.startedAt)
}

func callRunID(cs *callState, ctx context.Context) string {
	if cs != nil && cs.runID != "" {
		return cs.runID
	}
	return runIDFrom(ctx)
}

func callLLMCallID(cs *callState) string {
	if cs == nil {
		return ""
	}
	return cs.llmCallID
}

func callToolCallID(cs *callState) string {
	if cs == nil {
		return ""
	}
	return cs.toolCallID
}

func callLLMN(cs *callState) int64 {
	if cs == nil {
		return 0
	}
	return cs.llmN
}

func callToolN(cs *callState) int64 {
	if cs == nil {
		return 0
	}
	return cs.toolN
}

func callModel(cs *callState) string {
	if cs == nil {
		return ""
	}
	return cs.model
}
