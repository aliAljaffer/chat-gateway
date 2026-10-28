package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aliAljaffer/chat-gateway/internal/apps"
	"github.com/aliAljaffer/chat-gateway/internal/config"
	"github.com/aliAljaffer/chat-gateway/internal/opencode"
)

const testKey = "chat_test_key_012345678901234567890123456789"

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

func testConfig(openCodeURL, appsFile string) config.Config {
	return config.Config{
		Addr:            ":0",
		OpenCodeURL:     openCodeURL,
		OpenCodeAgent:   "assistant",
		AppsFile:        appsFile,
		ChatTimeout:     5 * time.Second,
		RatePerMinute:   0,
		MaxBodyBytes:    64 << 10,
		MaxMessages:     8,
		MaxMessageChars: 2000,
		MaxContextChars: 16000,
	}
}

func writeApps(t *testing.T, prompt string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "apps.json")
	data, err := json.Marshal(map[string]any{
		"apps": []map[string]string{{
			"id":            "test",
			"key":           testKey,
			"system_prompt": prompt,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	reg, err := apps.Load(cfg.AppsFile)
	if err != nil {
		t.Fatal(err)
	}
	oc := opencode.New(cfg.OpenCodeURL, cfg.OpenCodeAgent, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return New(cfg, reg, oc, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestAPIChatUsesTheAppPromptAndContext(t *testing.T) {
	fake := newFakeOpenCode(t)
	srv := newTestServer(t, testConfig(fake.server.URL, writeApps(t, "You are Gersz.")))

	body := `{"messages":[{"role":"user","content":"How much did I spend?"}],"context":"{\"may\":\"3,500.00 SAR\"}"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testKey)
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
	srv := newTestServer(t, testConfig(fake.server.URL, writeApps(t, "test")))

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
			req.Header.Set("Authorization", "Bearer "+testKey)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}

	for name, header := range map[string]string{
		"no key":     "",
		"wrong key":  "Bearer " + testKey + "x",
		"bad scheme": "Basic " + testKey,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
}

func TestAPIChatStreamsDeltas(t *testing.T) {
	fake := newFakeOpenCode(t)
	srv := newTestServer(t, testConfig(fake.server.URL, writeApps(t, "test")))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/stream",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
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
	cfg := testConfig(fake.server.URL, writeApps(t, "test"))
	cfg.RatePerMinute = 1
	srv := newTestServer(t, cfg)
	body := `{"messages":[{"role":"user","content":"hi"}]}`

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testKey)
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

func TestReadyChecksOpenCode(t *testing.T) {
	fake := newFakeOpenCode(t)
	srv := newTestServer(t, testConfig(fake.server.URL, writeApps(t, "test")))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d, body = %s", rec.Code, rec.Body.String())
	}

	cfg := testConfig("http://127.0.0.1:1", writeApps(t, "test"))
	srv = newTestServer(t, cfg)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready status = %d", rec.Code)
	}
}
