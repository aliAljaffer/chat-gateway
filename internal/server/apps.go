package server

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

type rateLimiter struct {
	limit  int
	mu     sync.Mutex
	bucket map[string]*rateBucket
}

type rateBucket struct {
	start time.Time
	count int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{limit: limit, bucket: map[string]*rateBucket{}}
}

func (l *rateLimiter) allow(key string) bool {
	if l.limit <= 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.bucket[key]
	if !ok || now.Sub(b.start) >= time.Minute {
		l.bucket[key] = &rateBucket{start: now, count: 1}
		return true
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}
