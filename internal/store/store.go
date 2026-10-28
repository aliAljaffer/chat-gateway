package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

type App struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	SystemPrompt string  `json:"system_prompt"`
	KeyPrefix    string  `json:"key_prefix"`
	CreatedAt    string  `json:"created_at"`
	LastUsedAt   *string `json:"last_used_at,omitempty"`
}

type Conversation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Message struct {
	ID        int64  `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS apps (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  system_prompt TEXT NOT NULL,
  key_hash TEXT NOT NULL UNIQUE,
  key_prefix TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_used_at TEXT
);
CREATE TABLE IF NOT EXISTS conversations (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, id);
`)
	return err
}

func (s *Store) CreateApp(ctx context.Context, name, systemPrompt string) (App, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return App{}, "", errors.New("name is required")
	}
	key, err := randomToken("chat_", 32)
	if err != nil {
		return App{}, "", err
	}
	now := now()
	app := App{
		ID:           newID("app_"),
		Name:         name,
		SystemPrompt: systemPrompt,
		KeyPrefix:    key[:12],
		CreatedAt:    now,
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO apps (id, name, system_prompt, key_hash, key_prefix, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		app.ID, app.Name, app.SystemPrompt, hashKey(key), app.KeyPrefix, app.CreatedAt)
	if err != nil {
		return App{}, "", err
	}
	return app, key, nil
}

func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, system_prompt, key_prefix, created_at, last_used_at FROM apps ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := []App{}
	for rows.Next() {
		var app App
		var last sql.NullString
		if err := rows.Scan(&app.ID, &app.Name, &app.SystemPrompt, &app.KeyPrefix, &app.CreatedAt, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			app.LastUsedAt = &last.String
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

func (s *Store) DeleteApp(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AppByKey(ctx context.Context, key string) (App, error) {
	app, err := s.appByHash(ctx, hashKey(key))
	if err != nil {
		return App{}, err
	}
	now := now()
	if _, err := s.db.ExecContext(ctx, `UPDATE apps SET last_used_at = ? WHERE id = ?`, now, app.ID); err != nil {
		return App{}, err
	}
	app.LastUsedAt = &now
	return app, nil
}

func (s *Store) appByHash(ctx context.Context, hash string) (App, error) {
	var app App
	var last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, system_prompt, key_prefix, created_at, last_used_at FROM apps WHERE key_hash = ?`, hash).
		Scan(&app.ID, &app.Name, &app.SystemPrompt, &app.KeyPrefix, &app.CreatedAt, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	if err != nil {
		return App{}, err
	}
	if last.Valid {
		app.LastUsedAt = &last.String
	}
	return app, nil
}

func (s *Store) CreateConversation(ctx context.Context, title string) (Conversation, error) {
	if strings.TrimSpace(title) == "" {
		title = "New chat"
	}
	now := now()
	c := Conversation{ID: newID("cnv_"), Title: title, CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		c.ID, c.Title, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return Conversation{}, err
	}
	return c, nil
}

func (s *Store) ListConversations(ctx context.Context) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, created_at, updated_at FROM conversations ORDER BY updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetConversation(ctx context.Context, id string) (Conversation, error) {
	var c Conversation
	err := s.db.QueryRowContext(ctx,
		`SELECT id, title, created_at, updated_at FROM conversations WHERE id = ?`, id).
		Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	return c, err
}

func (s *Store) DeleteConversation(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Messages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, role, content, created_at FROM (
  SELECT id, role, content, created_at FROM messages
  WHERE conversation_id = ? ORDER BY id DESC LIMIT ?
) ORDER BY id ASC`, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AppendTurn(ctx context.Context, conversationID, userContent, reply string) (Message, Message, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, Message{}, "", err
	}
	defer tx.Rollback()

	var title string
	if err := tx.QueryRowContext(ctx, `SELECT title FROM conversations WHERE id = ?`, conversationID).Scan(&title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Message{}, Message{}, "", ErrNotFound
		}
		return Message{}, Message{}, "", err
	}
	now := now()
	user := Message{Role: "user", Content: userContent, CreatedAt: now}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO messages (conversation_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		conversationID, user.Role, user.Content, user.CreatedAt)
	if err != nil {
		return Message{}, Message{}, "", err
	}
	user.ID, _ = res.LastInsertId()

	assistant := Message{Role: "assistant", Content: reply, CreatedAt: now}
	res, err = tx.ExecContext(ctx,
		`INSERT INTO messages (conversation_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		conversationID, assistant.Role, assistant.Content, assistant.CreatedAt)
	if err != nil {
		return Message{}, Message{}, "", err
	}
	assistant.ID, _ = res.LastInsertId()

	if title == "New chat" {
		title = titleFrom(userContent)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE conversations SET title = ?, updated_at = ? WHERE id = ?`, title, now, conversationID); err != nil {
		return Message{}, Message{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, Message{}, "", err
	}
	return user, assistant, title, nil
}

func titleFrom(content string) string {
	content = strings.Join(strings.Fields(content), " ")
	if content == "" {
		return "New chat"
	}
	if utf8.RuneCountInString(content) <= 60 {
		return content
	}
	runes := []rune(content)
	return strings.TrimSpace(string(runes[:57])) + "..."
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func randomToken(prefix string, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func newID(prefix string) string {
	token, err := randomToken(prefix, 12)
	if err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return token
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
