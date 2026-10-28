package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr            string
	OpenCodeURL     string
	OpenCodeAgent   string
	AppsFile        string
	ChatTimeout     time.Duration
	RatePerMinute   int
	MaxBodyBytes    int64
	MaxMessages     int
	MaxMessageChars int
	MaxContextChars int
}

func Load() (Config, error) {
	c := Config{
		Addr:            envOr("CHAT_ADDR", ":8080"),
		OpenCodeURL:     envOr("OPENCODE_URL", "http://127.0.0.1:4096"),
		OpenCodeAgent:   envOr("OPENCODE_AGENT", "assistant"),
		AppsFile:        envOr("CHAT_APPS_FILE", "/apps/apps.json"),
		ChatTimeout:     120 * time.Second,
		RatePerMinute:   30,
		MaxBodyBytes:    64 << 10,
		MaxMessages:     8,
		MaxMessageChars: 2000,
		MaxContextChars: 16000,
	}

	if v := os.Getenv("CHAT_TIMEOUT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("CHAT_TIMEOUT must be a positive number of seconds")
		}
		c.ChatTimeout = time.Duration(n) * time.Second
	}
	if v := os.Getenv("CHAT_RATE_PER_MINUTE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("CHAT_RATE_PER_MINUTE must be zero or a positive number")
		}
		c.RatePerMinute = n
	}
	if v := os.Getenv("CHAT_MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("CHAT_MAX_BODY_BYTES must be a positive number")
		}
		c.MaxBodyBytes = n
	}
	if !strings.HasPrefix(c.OpenCodeURL, "http://") && !strings.HasPrefix(c.OpenCodeURL, "https://") {
		return Config{}, fmt.Errorf("OPENCODE_URL must be an http or https URL")
	}
	if strings.TrimSpace(c.AppsFile) == "" {
		return Config{}, fmt.Errorf("CHAT_APPS_FILE must not be empty")
	}
	return c, nil
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
