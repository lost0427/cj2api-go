package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultModel   = "llama3.1-8B"
	userAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	maxBodyBytes   = 1 << 20
	maxUpstreamLen = 8 << 20
)

var (
	upstreamURL   = getEnv("UPSTREAM_URL", "https://chatjimmy.ai/api/chat")
	modelOverride = getEnv("MODEL_OVERRIDE", "")
	timeoutSec    = getEnvInt("TIMEOUT", 120)
	httpClient    = &http.Client{
		Timeout: time.Duration(timeoutSec) * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
	}
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	TopKUndersc *int          `json:"top_k"`
	TopKCamel   *int          `json:"topK"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type UpstreamStats struct {
	PrefillTokens *int `json:"prefill_tokens"`
	DecodeTokens  *int `json:"decode_tokens"`
	TotalTokens   *int `json:"total_tokens"`
}

func getEnv(k, d string) string {
	return cmp.Or(os.Getenv(k), d)
}

func getEnvInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return d
}

func allowedModel() string {
	if modelOverride != "" {
		return modelOverride
	}
	return defaultModel
}

func generateID() string {
	b := make([]byte, 24)
	for i := range b {
		b[i] = "abcdefghijklmnopqrstuvwxyz0123456789"[rand.Intn(36)]
	}
	return "chatcmpl-" + string(b)
}

func parseUpstreamResponse(raw string) (string, *UpstreamStats) {
	start := strings.LastIndex(raw, "<|stats|>")
	if start == -1 {
		return raw, nil
	}
	content := raw[:start]
	end := strings.LastIndex(raw, "<|/stats|>")
	if end == -1 {
		return content, nil
	}
	var st UpstreamStats
	if err := json.Unmarshal([]byte(raw[start+9:end]), &st); err != nil {
		return content, nil
	}
	return content, &st
}

func buildUsage(st *UpstreamStats) Usage {
	u := Usage{PromptTokens: -1, CompletionTokens: -1, TotalTokens: -1}
	if st == nil {
		return u
	}
	if st.PrefillTokens != nil {
		u.PromptTokens = *st.PrefillTokens
	}
	if st.DecodeTokens != nil {
		u.CompletionTokens = *st.DecodeTokens
	}
	if st.TotalTokens != nil {
		u.TotalTokens = *st.TotalTokens
	}
	return u
}

func setCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	setCORS(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg, typ string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg, "type": typ}})
}

func fetchUpstream(ctx context.Context, body *ChatRequest) (string, error) {
	model := body.Model
	topK := 8
	if body.TopKUndersc != nil {
		topK = *body.TopKUndersc
	} else if body.TopKCamel != nil {
		topK = *body.TopKCamel
	}
	var sysParts []string
	chatMsgs := make([]ChatMessage, 0, len(body.Messages))
	for _, m := range body.Messages {
		if m.Role == "system" {
			sysParts = append(sysParts, m.Content)
		} else {
			chatMsgs = append(chatMsgs, m)
		}
	}
	payload := map[string]any{
		"messages": chatMsgs,
		"chatOptions": map[string]any{
			"selectedModel": model,
			"systemPrompt":  strings.Join(sysParts, "\n"),
			"topK":          topK,
		},
		"attachment": nil,
	}
	bs, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(bs))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxUpstreamLen+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if len(raw) > maxUpstreamLen {
		return "", fmt.Errorf("上游响应超限")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("上游返回 %d: %s", resp.StatusCode, string(raw))
	}
	return string(raw), nil
}

func splitContent(text string) []string {
	if text == "" {
		return nil
	}
	r := []rune(text)
	var out []string
	i := 0
	for i < len(r) {
		end := i + 3 + rand.Intn(10)
		if end > len(r) {
			end = len(r)
		}
		if end < len(r) {
			for j := end; j > i+2; j-- {
				if strings.ContainsRune(" ,.，\n!？。", r[j]) {
					end = j + 1
					break
				}
			}
		}
		out = append(out, string(r[i:end]))
		i = end
	}
	return out
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "请求体 JSON 解析失败", "invalid_request_error")
		return
	}
	if len(body.Messages) == 0 {
		writeErr(w, 400, "messages 字段不能为空", "invalid_request_error")
		return
	}
	allowed := allowedModel()
	if body.Model == "" {
		body.Model = allowed
	} else if body.Model != allowed {
		writeErr(w, 400, "model 仅支持 "+allowed, "invalid_request_error")
		return
	}
	model := body.Model
	raw, err := fetchUpstream(r.Context(), &body)
	if err != nil {
		writeErr(w, 502, err.Error(), "server_error")
		return
	}
	content, stats := parseUpstreamResponse(raw)
	if !body.Stream {
		writeJSON(w, 200, map[string]any{
			"id":      generateID(),
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": buildUsage(stats),
		})
		return
	}
	setCORS(w.Header())
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	id := generateID()
	created := time.Now().Unix()
	emit := func(v any) {
		bs, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", bs)
		if fl != nil {
			fl.Flush()
		}
	}
	chunk := func(delta map[string]string, finish any) map[string]any {
		return map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}
	emit(chunk(map[string]string{"role": "assistant", "content": ""}, nil))
	for _, p := range splitContent(content) {
		emit(chunk(map[string]string{"content": p}, nil))
	}
	emit(chunk(map[string]string{}, "stop"))
	if stats != nil {
		emit(map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{}, "usage": buildUsage(stats),
		})
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

func handleModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"object": "list",
		"data": []any{map[string]any{
			"id": allowedModel(), "object": "model", "created": 1740000000,
			"owned_by": "system", "permission": []any{},
		}},
	})
}

func route(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		setCORS(w.Header())
		w.WriteHeader(200)
		return
	}
	p := r.URL.Path
	switch {
	case p == "/" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]string{"message": "cj2api-go is running", "hint": "POST /v1/chat/completions"})
	case (p == "/v1/chat/completions" || p == "/chat/completions") && r.Method == http.MethodPost:
		handleChat(w, r)
	case (p == "/v1/models" || p == "/models") && r.Method == http.MethodGet:
		handleModels(w, r)
	default:
		writeErr(w, 404, "Not Found", "invalid_request_error")
	}
}

func main() {
	port := getEnv("PORT", "8787")
	mux := http.NewServeMux()
	mux.HandleFunc("/", route)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		slog.Info("listening", "port", port, "upstream", upstreamURL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}
