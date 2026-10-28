package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "apps.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadLooksUpKeys(t *testing.T) {
	key := strings.Repeat("k", 32)
	reg, err := Load(write(t, `{"apps":[{"id":"grooszy","key":"`+key+`","system_prompt":"You are Gersz."}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if reg.Len() != 1 {
		t.Fatalf("len = %d", reg.Len())
	}
	app, ok := reg.Lookup(key)
	if !ok || app.ID != "grooszy" || app.SystemPrompt != "You are Gersz." {
		t.Fatalf("lookup = %#v %v", app, ok)
	}
	if _, ok := reg.Lookup("wrong"); ok {
		t.Fatal("unknown key matched")
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	key := strings.Repeat("k", 32)
	cases := map[string]string{
		"no apps":       `{"apps":[]}`,
		"bad json":      `not json`,
		"missing id":    `{"apps":[{"key":"` + key + `"}]}`,
		"short key":     `{"apps":[{"id":"a","key":"short"}]}`,
		"duplicate id":  `{"apps":[{"id":"a","key":"` + key + `"},{"id":"a","key":"` + strings.Repeat("x", 32) + `"}]}`,
		"duplicate key": `{"apps":[{"id":"a","key":"` + key + `"},{"id":"b","key":"` + key + `"}]}`,
		"unknown field": `{"apps":[{"id":"a","key":"` + key + `","model":"x"}]}`,
		"trailing json": `{"apps":[{"id":"a","key":"` + key + `"}]}{}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
