package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"
)

// HTTPError carries the status code so retry logic can distinguish
// retryable (429/5xx) from permanent (other 4xx) failures.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("llm http %d: %s", e.StatusCode, e.Body)
}

// OpenAI is a hand-written OpenAI-compatible chat client.
// It deliberately avoids an LLM SDK so streaming, retries, timeouts and
// truncation detection are fully transparent and controllable.
type OpenAI struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	maxRetries int
	stream     bool
}

// NewOpenAI builds a client. Model is required.
func NewOpenAI(cfg Config) (*OpenAI, error) {
	if cfg.Model == "" {
		return nil, errors.New("llm: OPENAI_MODEL is required")
	}
	return &OpenAI{
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		maxRetries: cfg.MaxRetries,
		stream:     cfg.Stream,
	}, nil
}

// Chat sends a chat completion with retry + exponential backoff + jitter. A
// per-request Model override (set by e.g. a subagent's model field) wins over
// the client's configured model; empty falls back to it.
func (o *OpenAI) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if req.Model == "" {
		req.Model = o.model
	}
	req.Stream = o.stream
	// Ask the gateway to include usage in the final SSE chunk so token
	// accounting works in streaming mode (the agent's default).
	if req.Stream && req.StreamOptions == nil {
		req.StreamOptions = &StreamOptions{IncludeUsage: true}
	}
	return o.retry(ctx, req)
}

// ChatStream is the streaming variant of Chat: every content delta reaches
// onDelta as the gateway emits it (a live terminal renders this), and the
// fully assembled response with usage is returned as usual. Streaming is
// forced on regardless of the client's config flag.
func (o *OpenAI) ChatStream(ctx context.Context, req ChatRequest, onDelta func(string)) (ChatResponse, error) {
	if req.Model == "" {
		req.Model = o.model
	}
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}
	return o.retry(ctx, req, onDelta)
}

// retry runs the request with exponential backoff + jitter. The optional
// onDelta is threaded through to the SSE parser when streaming.
func (o *OpenAI) retry(ctx context.Context, req ChatRequest, onDelta ...func(string)) (ChatResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff(attempt)):
			case <-ctx.Done():
				return ChatResponse{}, ctx.Err()
			}
		}
		resp, err := o.chatOnce(ctx, req, onDelta...)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// Permanent client errors (e.g. bad request, auth) never recover
		// from retrying — fail fast instead of burning retries.
		var he *HTTPError
		if errors.As(err, &he) && he.StatusCode < 500 && he.StatusCode != 429 {
			break
		}
	}
	return ChatResponse{}, fmt.Errorf("llm chat failed after %d attempts: %w", o.maxRetries+1, lastErr)
}

// backoff returns 250ms * 2^attempt plus jitter.
func backoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt)) * 250 * time.Millisecond
	return base + time.Duration(rand.Int64N(int64(base)+1))
}

func (o *OpenAI) chatOnce(ctx context.Context, req ChatRequest, onDelta ...func(string)) (ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	httpResp, err := o.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm http: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		msg := string(b)
		var oeb openAIErrorBody
		if json.Unmarshal(b, &oeb) == nil && oeb.Error != nil {
			msg = oeb.Error.Message
		}
		return ChatResponse{}, &HTTPError{StatusCode: httpResp.StatusCode, Body: msg}
	}

	if req.Stream {
		return parseSSE(ctx, httpResp.Body, onDelta...)
	}
	return parseNonStream(httpResp.Body)
}

type openAIErrorBody struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func parseNonStream(r io.Reader) (ChatResponse, error) {
	var raw openAIChatResponse
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return ChatResponse{}, fmt.Errorf("llm decode: %w", err)
	}
	return toChatResponse(raw)
}

func toChatResponse(raw openAIChatResponse) (ChatResponse, error) {
	if len(raw.Choices) == 0 {
		return ChatResponse{}, errors.New("llm: empty choices in response")
	}
	ch := raw.Choices[0]
	msg := Message{Role: Role(ch.Message.Role), Content: ch.Message.Content}
	for _, tc := range ch.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return ChatResponse{
		Message:      msg,
		FinishReason: ch.FinishReason,
		Truncated:    ch.FinishReason == "length",
		Usage:        raw.Usage,
	}, nil
}
