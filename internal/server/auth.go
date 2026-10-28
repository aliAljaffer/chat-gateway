package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

var (
	errUnauthorized    = errors.New("unauthorized")
	errUINotConfigured = errors.New("ui auth is not configured")
)

type accessVerifier struct {
	issuer   string
	audience string
	owner    string

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
}

func newAccessVerifier(teamDomain, audience, owner string) *accessVerifier {
	issuer := strings.TrimRight(strings.TrimSpace(teamDomain), "/")
	if issuer != "" && !strings.HasPrefix(issuer, "http://") && !strings.HasPrefix(issuer, "https://") {
		issuer = "https://" + issuer
	}
	return &accessVerifier{issuer: issuer, audience: audience, owner: strings.ToLower(strings.TrimSpace(owner))}
}

type accessClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Sub           string `json:"sub"`
}

func (a *accessVerifier) verify(ctx context.Context, raw string) (string, error) {
	if a.issuer == "" || a.audience == "" {
		return "", errUINotConfigured
	}
	verifier, err := a.getVerifier(ctx)
	if err != nil {
		return "", err
	}
	token, err := verifier.Verify(ctx, raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errUnauthorized, err)
	}
	var claims accessClaims
	if err := token.Claims(&claims); err != nil {
		return "", fmt.Errorf("%w: %v", errUnauthorized, err)
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		return "", fmt.Errorf("%w: token has no email", errUnauthorized)
	}
	if a.owner != "" && email != a.owner {
		return "", errUnauthorized
	}
	return email, nil
}

func (a *accessVerifier) getVerifier(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.verifier != nil {
		return a.verifier, nil
	}
	provider, err := oidc.NewProvider(ctx, a.issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: access provider unavailable: %v", errUINotConfigured, err)
	}
	a.verifier = provider.Verifier(&oidc.Config{ClientID: a.audience})
	return a.verifier, nil
}

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
