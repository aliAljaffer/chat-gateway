package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aliAljaffer/chat-gateway/internal/config"
	"github.com/aliAljaffer/chat-gateway/internal/opencode"
	"github.com/aliAljaffer/chat-gateway/internal/store"
)

type capturedPrompt struct {
	Agent  string          `json:"agent"`
	System string          `json:"system"`
	Tools  map[string]bool `json:"tools"`
	Parts  []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"parts"`
}

type fakeOpenCode struct {
	mu       sync.Mutex
	prompts  []capturedPrompt
	reply    string
	sessions int
	deletes  int
	server   *httptest.Server
}

func newFakeOpenCode(t *testing.T) *fakeOpenCode {
	t.Helper()
	f := &fakeOpenCode{reply: "Hello from the model."}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"healthy":true,"version":"test"}`)
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.sessions++
		f.mu.Unlock()
		io.WriteString(w, `{"id":"ses_test"}`)
	})
	mux.HandleFunc("POST /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		var prompt capturedPrompt
		if err := json.NewDecoder(r.Body).Decode(&prompt); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.prompts = append(f.prompts, prompt)
		reply := f.reply
		f.mu.Unlock()
		time.Sleep(60 * time.Millisecond)
		body, _ := json.Marshal(map[string]any{
			"info":  map[string]any{"id": "msg_test", "role": "assistant"},
			"parts": []map[string]any{{"type": "text", "text": reply}},
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
	mux.HandleFunc("DELETE /session/{id}", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.deletes++
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		updated, _ := json.Marshal(map[string]any{
			"type": "message.updated",
			"properties": map[string]any{
				"sessionID": "ses_test",
				"info":      map[string]any{"id": "msg_test", "role": "assistant"},
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", updated)
		flusher.Flush()
		send := func(delta string) {
			payload, _ := json.Marshal(map[string]any{
				"type": "message.part.delta",
				"properties": map[string]any{
					"sessionID": "ses_test",
					"messageID": "msg_test",
					"partID":    "prt_test",
					"field":     "text",
					"delta":     delta,
				},
			})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
		send("Hello ")
		time.Sleep(20 * time.Millisecond)
		send("from the model.")
		<-r.Context().Done()
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOpenCode) lastPrompt(t *testing.T) capturedPrompt {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.prompts) == 0 {
		t.Fatal("no prompt was sent")
	}
	return f.prompts[len(f.prompts)-1]
}

func testConfig(openCodeURL string) config.Config {
	return config.Config{
		Addr:            ":0",
		DBPath:          "",
		OpenCodeURL:     openCodeURL,
		OpenCodeAgent:   "assistant",
		ChatTimeout:     5 * time.Second,
		RatePerMinute:   0,
		MaxBodyBytes:    64 << 10,
		MaxMessages:     8,
		MaxMessageChars: 2000,
		MaxContextChars: 16000,
		HistoryMessages: 20,
		DevUI:           true,
		Model:           "test-model",
	}
}

func newTestServer(t *testing.T, cfg config.Config) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	oc := opencode.New(cfg.OpenCodeURL, cfg.OpenCodeAgent, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv, err := New(cfg, st, oc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return srv, st
}

func createApp(t *testing.T, st *store.Store, prompt string) (store.App, string) {
	t.Helper()
	app, key, err := st.CreateApp(context.Background(), "test", prompt)
	if err != nil {
		t.Fatal(err)
	}
	return app, key
}

func TestAPIChatUsesTheAppPromptAndContext(t *testing.T) {
	fake := newFakeOpenCode(t)
	srv, st := newTestServer(t, testConfig(fake.server.URL))
	_, key := createApp(t, st, "You are Gersz.")

	body := `{"messages":[{"role":"user","content":"How much did I spend?"}],"context":"{\"may\":\"3,500.00 SAR\"}"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["reply"] != "Hello from the model." {
		t.Fatalf("reply = %q", out["reply"])
	}
	prompt := fake.lastPrompt(t)
	if !strings.Contains(prompt.System, "You are Gersz.") || !strings.Contains(prompt.System, "Never call a tool") {
		t.Fatalf("system prompt = %q", prompt.System)
	}
	text := prompt.Parts[0].Text
	if !strings.Contains(text, "3,500.00 SAR") || !strings.Contains(text, "user: How much did I spend?") {
		t.Fatalf("user prompt = %q", text)
	}
	if prompt.Tools["bash"] || prompt.Tools["read"] {
		t.Fatalf("tools were not disabled: %#v", prompt.Tools)
	}
	if prompt.Agent != "assistant" {
		t.Fatalf("agent = %q", prompt.Agent)
	}
}

func TestAPIChatRejectsBadShapeAndAuth(t *testing.T) {
	fake := newFakeOpenCode(t)
	srv, st := newTestServer(t, testConfig(fake.server.URL))
	_, key := createApp(t, st, "test")

	cases := map[string]string{
		"missing messages": `{}`,
		"empty messages":   `{"messages":[]}`,
		"system role":      `{"messages":[{"role":"system","content":"x"},{"role":"user","content":"hi"}]}`,
		"last assistant":   `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
		"blank content":    `{"messages":[{"role":"user","content":"  "}]}`,
		"unknown field":    `{"messages":[{"role":"user","content":"hi"}],"model":"x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+key)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key status = %d", rec.Code)
	}
}

func TestAPIChatStreamsDeltas(t *testing.T) {
	fake := newFakeOpenCode(t)
	srv, st := newTestServer(t, testConfig(fake.server.URL))
	_, key := createApp(t, st, "test")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/stream",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: delta") || !strings.Contains(out, "Hello ") {
		t.Fatalf("stream = %s", out)
	}
	if !strings.Contains(out, "event: done") || !strings.Contains(out, "Hello from the model.") {
		t.Fatalf("stream = %s", out)
	}
}

func TestRateLimit(t *testing.T) {
	fake := newFakeOpenCode(t)
	cfg := testConfig(fake.server.URL)
	cfg.RatePerMinute = 1
	srv, st := newTestServer(t, cfg)
	_, key := createApp(t, st, "test")
	body := `{"messages":[{"role":"user","content":"hi"}]}`

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if i == 0 && rec.Code != http.StatusOK {
			t.Fatalf("first status = %d", rec.Code)
		}
		if i == 1 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second status = %d", rec.Code)
		}
	}
}

func TestUIRequiresAccessAndRefusesCrossSite(t *testing.T) {
	fake := newFakeOpenCode(t)
	cfg := testConfig(fake.server.URL)
	cfg.DevUI = false
	srv, _ := newTestServer(t, cfg)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	cfg.DevUI = true
	srv, _ = newTestServer(t, cfg)
	req := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader("{}"))
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross site status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("X-Chat-Dev-Email", "owner@example.com")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "owner@example.com") {
		t.Fatalf("me status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestUIAppsAndConversationFlow(t *testing.T) {
	fake := newFakeOpenCode(t)
	srv, _ := newTestServer(t, testConfig(fake.server.URL))
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/apps",
		strings.NewReader(`{"name":"grooszy","system_prompt":"You are Gersz."}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created struct {
		App store.App `json:"app"`
		Key string    `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Key, "chat_") {
		t.Fatalf("key = %q", created.Key)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "grooszy") {
		t.Fatalf("list apps status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader("{}")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create conversation status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var conv struct {
		Conversation store.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/conversations/"+conv.Conversation.ID+"/messages",
		strings.NewReader(`{"content":"Hello"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("send status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("send body = %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/conversations/"+conv.Conversation.ID+"/messages", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("messages status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Hello from the model.") {
		t.Fatalf("messages body = %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/apps/"+created.App.ID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete app status = %d", rec.Code)
	}
}

func TestReadyChecksOpenCode(t *testing.T) {
	fake := newFakeOpenCode(t)
	srv, _ := newTestServer(t, testConfig(fake.server.URL))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d, body = %s", rec.Code, rec.Body.String())
	}

	cfg := testConfig(fake.server.URL)
	cfg.OpenCodeURL = "http://127.0.0.1:1"
	srv, _ = newTestServer(t, cfg)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready status = %d", rec.Code)
	}
}
