package store

import (
	"context"
	"time"
)

// LLMCall 是一次模型调用的用量记录，同一条妙记每次生成各记一条
type LLMCall struct {
	ID               int64
	MinuteID         int64
	Model            string
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	ReasoningTokens  int64
	TotalTokens      int64
	DurationMillis   int64
	// Error 是调用出错时的原因，成功时为空
	Error     string
	CreatedAt time.Time
}

// InsertLLMCall 新增一条模型调用记录
func (s *Store) InsertLLMCall(ctx context.Context, call *LLMCall) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO llm_calls (minute_id, model, prompt_tokens, cached_tokens, completion_tokens, reasoning_tokens,
			total_tokens, duration_ms, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		call.MinuteID,
		call.Model,
		call.PromptTokens,
		call.CachedTokens,
		call.CompletionTokens,
		call.ReasoningTokens,
		call.TotalTokens,
		call.DurationMillis,
		call.Error,
		time.Now().Unix(),
	)

	return err
}

// LLMCalls 按调用先后返回一条妙记的全部模型调用记录
func (s *Store) LLMCalls(ctx context.Context, minuteID int64) ([]*LLMCall, error) {
	const query = `SELECT id, minute_id, model, prompt_tokens, cached_tokens, completion_tokens, reasoning_tokens,
		total_tokens, duration_ms, error, created_at FROM llm_calls WHERE minute_id = ? ORDER BY id`

	rows, err := s.db.QueryContext(ctx, query, minuteID)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var calls []*LLMCall

	for rows.Next() {
		var (
			call    LLMCall
			created int64
		)

		err = rows.Scan(
			&call.ID,
			&call.MinuteID,
			&call.Model,
			&call.PromptTokens,
			&call.CachedTokens,
			&call.CompletionTokens,
			&call.ReasoningTokens,
			&call.TotalTokens,
			&call.DurationMillis,
			&call.Error,
			&created,
		)
		if err != nil {
			return nil, err
		}

		call.CreatedAt = fromUnix(created)
		calls = append(calls, &call)
	}

	return calls, rows.Err()
}
