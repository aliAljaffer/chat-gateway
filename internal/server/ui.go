package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aliAljaffer/chat-gateway/internal/store"
)

const maxUIContextRunes = 24000

func (s *Server) me(w http.ResponseWriter, _ *http.Request, email string) {
	writeJSON(w, http.StatusOK, map[string]string{"email": email, "model": s.cfg.Model})
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request, _ string) {
	apps, err := s.store.ListApps(r.Context())
	if err != nil {
		s.log.Error("list apps failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read apps")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request, _ string) {
	data, err := readBody(r, 32<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "body could not be read")
		return
	}
	var body struct {
		Name         string `json:"name"`
		SystemPrompt string `json:"system_prompt"`
	}
	if err := decodeJSON(data, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body must be a JSON object with name and system_prompt")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || utf8.RuneCountInString(body.Name) > 80 {
		writeError(w, http.StatusUnprocessableEntity, "name must hold 1 to 80 characters")
		return
	}
	if utf8.RuneCountInString(body.SystemPrompt) > 8000 {
		writeError(w, http.StatusUnprocessableEntity, "system_prompt exceeds 8000 characters")
		return
	}
	app, key, err := s.store.CreateApp(r.Context(), body.Name, body.SystemPrompt)
	if err != nil {
		s.log.Error("create app failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create the app")
		return
	}
	s.log.Info("app created", "app", app.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"app": app, "key": key})
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request, _ string) {
	if err := s.store.DeleteApp(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "app not found")
			return
		}
		s.log.Error("delete app failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete the app")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request, _ string) {
	conversations, err := s.store.ListConversations(r.Context())
	if err != nil {
		s.log.Error("list conversations failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read conversations")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": conversations})
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request, _ string) {
	c, err := s.store.CreateConversation(r.Context(), "New chat")
	if err != nil {
		s.log.Error("create conversation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create the conversation")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"conversation": c})
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request, _ string) {
	if err := s.store.DeleteConversation(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		s.log.Error("delete conversation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete the conversation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request, _ string) {
	id := r.PathValue("id")
	c, err := s.store.GetConversation(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		s.log.Error("get conversation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the conversation")
		return
	}
	messages, err := s.store.Messages(r.Context(), id, 1000)
	if err != nil {
		s.log.Error("list messages failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read messages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": c, "messages": messages})
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, _ string) {
	data, err := readBody(r, s.cfg.MaxBodyBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "body could not be read")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(data, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body must be a JSON object with content")
		return
	}
	content := strings.TrimSpace(body.Content)
	if content == "" {
		writeError(w, http.StatusUnprocessableEntity, "content is required")
		return
	}
	if utf8.RuneCountInString(content) > s.cfg.MaxMessageChars {
		writeError(w, http.StatusUnprocessableEntity, "content is too long")
		return
	}

	id := r.PathValue("id")
	if _, err := s.store.GetConversation(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		s.log.Error("get conversation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the conversation")
		return
	}
	history, err := s.store.Messages(r.Context(), id, s.cfg.HistoryMessages)
	if err != nil {
		s.log.Error("list messages failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read messages")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ChatTimeout)
	defer cancel()

	sessionID, err := s.oc.CreateSession(ctx, "chat")
	if err != nil {
		s.modelError(w, store.App{}, err)
		return
	}
	defer s.deleteSession(sessionID)

	started := time.Now()
	startSSE(w)
	reply, err := s.oc.PromptStream(ctx, sessionID, uiSystemPrompt, buildUIPrompt(history, content), func(delta string) {
		writeSSE(w, "delta", map[string]string{"text": delta})
		flush(w)
	})
	if err != nil {
		s.log.Warn("ui chat failed", "error", err)
		writeSSE(w, "error", map[string]string{"error": publicModelError(err)})
		flush(w)
		return
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer saveCancel()
	user, assistant, title, err := s.store.AppendTurn(saveCtx, id, content, reply)
	if err != nil {
		s.log.Error("save turn failed", "error", err)
		writeSSE(w, "error", map[string]string{"error": "could not save the conversation"})
		flush(w)
		return
	}
	s.log.Info("ui chat answered", "conversation", id, "duration_ms", time.Since(started).Milliseconds())
	writeSSE(w, "done", map[string]any{
		"reply":     reply,
		"user":      user,
		"assistant": assistant,
		"title":     title,
	})
	flush(w)
}

func buildUIPrompt(history []store.Message, current string) string {
	turns := make([]string, 0, len(history)+1)
	for _, m := range history {
		turns = append(turns, m.Role+": "+strings.TrimSpace(m.Content))
	}
	turns = append(turns, "user: "+strings.TrimSpace(current))

	total := 0
	start := len(turns) - 1
	for i := len(turns) - 1; i >= 0; i-- {
		add := utf8.RuneCountInString(turns[i]) + 1
		if i != len(turns)-1 && total+add > maxUIContextRunes {
			break
		}
		total += add
		start = i
	}
	return "Conversation:\n" + strings.Join(turns[start:], "\n")
}
