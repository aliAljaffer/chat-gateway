package apps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

type App struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	SystemPrompt string `json:"system_prompt"`
}

type Registry struct {
	byHash map[string]App
}

type file struct {
	Apps []App `json:"apps"`
}

func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read apps file: %w", err)
	}
	var parsed file
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("apps file must be a JSON object with an apps array: %w", err)
	}
	if dec.More() {
		return nil, errors.New("apps file must be a single JSON object")
	}
	if len(parsed.Apps) == 0 {
		return nil, errors.New("apps file has no apps")
	}

	reg := &Registry{byHash: map[string]App{}}
	ids := map[string]bool{}
	for i, app := range parsed.Apps {
		app.ID = strings.TrimSpace(app.ID)
		app.Key = strings.TrimSpace(app.Key)
		app.SystemPrompt = strings.TrimSpace(app.SystemPrompt)
		if app.ID == "" {
			return nil, fmt.Errorf("app %d has no id", i+1)
		}
		if len(app.Key) < 24 || len(app.Key) > 256 {
			return nil, fmt.Errorf("app %q key must hold 24 to 256 characters", app.ID)
		}
		if utf8.RuneCountInString(app.SystemPrompt) > 8000 {
			return nil, fmt.Errorf("app %q system_prompt exceeds 8000 characters", app.ID)
		}
		if ids[app.ID] {
			return nil, fmt.Errorf("app id %q is duplicated", app.ID)
		}
		ids[app.ID] = true
		hash := hashKey(app.Key)
		if _, ok := reg.byHash[hash]; ok {
			return nil, fmt.Errorf("app %q uses a duplicate key", app.ID)
		}
		reg.byHash[hash] = App{ID: app.ID, SystemPrompt: app.SystemPrompt}
	}
	return reg, nil
}

func (r *Registry) Lookup(key string) (App, bool) {
	app, ok := r.byHash[hashKey(key)]
	return app, ok
}

func (r *Registry) Len() int { return len(r.byHash) }

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
