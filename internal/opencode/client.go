package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

var (
	ErrUnavailable = errors.New("assistant unavailable")
	ErrProvider    = errors.New("model did not answer")
	ErrEmpty       = errors.New("model returned no text")
)

type Client struct {
	baseURL string
	agent   string
	http    *http.Client
	log     *slog.Logger
}

type TextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Part struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	PartID    string `json:"partID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	Delta     string `json:"-"`
	Synthetic bool   `json:"synthetic"`
	Ignored   bool   `json:"ignored"`
}

type messageInfo struct {
	ID    string          `json:"id"`
	Role  string          `json:"role"`
	Error json.RawMessage `json:"error"`
}

type promptResponse struct {
	Info  messageInfo `json:"info"`
	Parts []Part      `json:"parts"`
}

var textOnlyTools = map[string]bool{
	"bash":      false,
	"edit":      false,
	"write":     false,
	"read":      false,
	"grep":      false,
	"glob":      false,
	"list":      false,
	"webfetch":  false,
	"websearch": false,
	"task":      false,
	"patch":     false,
	"todowrite": false,
	"skill":     false,
	"question":  false,
	"lsp":       false,
	"batch":     false,
}

func New(baseURL, agent string, log *slog.Logger) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		agent:   agent,
		http:    &http.Client{},
		log:     log,
	}
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/global/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: health status %d", ErrUnavailable, resp.StatusCode)
	}
	var body struct {
		Healthy bool `json:"healthy"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&body); err != nil {
		return fmt.Errorf("%w: health response unreadable", ErrUnavailable)
	}
	if !body.Healthy {
		return fmt.Errorf("%w: not healthy", ErrUnavailable)
	}
	return nil
}

func (c *Client) CreateSession(ctx context.Context, title string) (string, error) {
	body, err := json.Marshal(map[string]string{"title": title, "agent": c.agent})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("%w: create status %d", ErrUnavailable, resp.StatusCode)
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&session); err != nil {
		return "", fmt.Errorf("%w: session response unreadable", ErrUnavailable)
	}
	if !strings.HasPrefix(session.ID, "ses") {
		return "", fmt.Errorf("%w: session response has no id", ErrUnavailable)
	}
	return session.ID, nil
}

func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/session/"+sessionID, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("delete session status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) Prompt(ctx context.Context, sessionID, system, user string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"agent":  c.agent,
		"system": system,
		"tools":  textOnlyTools,
		"parts":  []TextPart{{Type: "text", Text: user}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/session/"+sessionID+"/message", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("%w: prompt status %d", ErrProvider, resp.StatusCode)
	}
	var out promptResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("%w: prompt response unreadable", ErrProvider)
	}
	if hasError(out.Info.Error) {
		return "", fmt.Errorf("%w: provider reported an error", ErrProvider)
	}
	text := joinText(out.Parts)
	if strings.TrimSpace(text) == "" {
		return "", ErrEmpty
	}
	return text, nil
}

func (c *Client) PromptStream(ctx context.Context, sessionID, system, user string, onDelta func(string)) (string, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan Part, 64)
	streamErr := make(chan error, 1)
	go c.readEvents(streamCtx, sessionID, events, streamErr)

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := c.Prompt(ctx, sessionID, system, user)
		done <- result{text: text, err: err}
	}()

	var acc strings.Builder
	for {
		select {
		case part, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if part.Delta != "" {
				acc.WriteString(part.Delta)
				onDelta(part.Delta)
				continue
			}
			if part.Type != "text" || part.Synthetic || part.Ignored {
				continue
			}
			full := part.Text
			current := acc.String()
			if strings.HasPrefix(full, current) {
				delta := full[len(current):]
				if delta != "" {
					acc.WriteString(delta)
					onDelta(delta)
				}
			} else {
				acc.Reset()
				acc.WriteString(full)
				onDelta(full)
			}
		case err := <-streamErr:
			if err != nil {
				c.log.Debug("event stream ended", "error", err)
			}
			streamErr = nil
		case res := <-done:
			if res.err != nil {
				if errors.Is(res.err, ErrEmpty) && acc.Len() > 0 {
					return acc.String(), nil
				}
				return "", res.err
			}
			if strings.TrimSpace(res.text) == "" {
				if acc.Len() > 0 {
					return acc.String(), nil
				}
				return "", ErrEmpty
			}
			if acc.Len() == 0 {
				onDelta(res.text)
			}
			return res.text, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (c *Client) readEvents(ctx context.Context, sessionID string, out chan<- Part, errs chan<- error) {
	defer close(out)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/event", nil)
	if err != nil {
		errs <- err
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		errs <- err
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errs <- fmt.Errorf("event status %d", resp.StatusCode)
		return
	}

	state := &eventState{assistant: map[string]bool{}}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				c.handleEvent(data.String(), sessionID, state, out)
				data.Reset()
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		errs <- err
	}
}

type eventState struct {
	assistant map[string]bool
}

func (c *Client) handleEvent(raw, sessionID string, state *eventState, out chan<- Part) {
	var envelope struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return
	}
	switch envelope.Type {
	case "message.updated":
		var props struct {
			SessionID string `json:"sessionID"`
			Info      struct {
				ID   string `json:"id"`
				Role string `json:"role"`
			} `json:"info"`
		}
		if err := json.Unmarshal(envelope.Properties, &props); err != nil {
			return
		}
		if props.SessionID == sessionID && props.Info.Role == "assistant" {
			state.assistant[props.Info.ID] = true
		}
	case "message.part.delta":
		var props struct {
			SessionID string `json:"sessionID"`
			MessageID string `json:"messageID"`
			PartID    string `json:"partID"`
			Field     string `json:"field"`
			Delta     string `json:"delta"`
		}
		if err := json.Unmarshal(envelope.Properties, &props); err != nil {
			return
		}
		if props.SessionID != sessionID || props.Field != "text" || !state.assistant[props.MessageID] {
			return
		}
		select {
		case out <- Part{PartID: props.PartID, MessageID: props.MessageID, Type: "text", Delta: props.Delta}:
		case <-time.After(5 * time.Second):
		}
	}
}

func joinText(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" || p.Synthetic || p.Ignored {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Text)
	}
	return strings.TrimSpace(b.String())
}

func hasError(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}
