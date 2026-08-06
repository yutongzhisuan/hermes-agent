package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// WorkerClaims holds verified worker JWT fields.
type WorkerClaims struct {
	WorkerID        string
	AllowedToolsets []string
	MaxConcurrent   int
}

// IssueWorkerJWT returns a short-lived worker token.
func (a *Auth) IssueWorkerJWT(workerID string, toolsets []string, maxConcurrent int, ttl time.Duration) (string, error) {
	if workerID == "" {
		return "", fmt.Errorf("worker id is required")
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	if ttl <= 0 {
		ttl = a.ttl
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":              workerID,
		"aud":              a.audience,
		"iss":              a.issuer,
		"allowed_toolsets": toolsets,
		"max_concurrent":   maxConcurrent,
		"exp":              now.Add(ttl).Unix(),
		"iat":              now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.secret)
}

// VerifyWorkerJWT validates a worker bearer token.
func (a *Auth) VerifyWorkerJWT(token string) (*WorkerClaims, error) {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return a.secret, nil
	}, jwt.WithAudience(a.audience), jwt.WithIssuer(a.issuer))
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	if claims["role"] == masterRole {
		return nil, fmt.Errorf("not a worker token")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, fmt.Errorf("missing sub claim")
	}
	maxConcurrent := 1
	if raw, ok := claims["max_concurrent"].(float64); ok && int(raw) > 0 {
		maxConcurrent = int(raw)
	}
	toolsets := make([]string, 0)
	if raw, ok := claims["allowed_toolsets"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				toolsets = append(toolsets, s)
			}
		}
	}
	return &WorkerClaims{
		WorkerID:        sub,
		AllowedToolsets: toolsets,
		MaxConcurrent:   maxConcurrent,
	}, nil
}

// JWTExpiresAt returns the exp claim without verifying the signature.
func JWTExpiresAt(token string) (int64, error) {
	parsed, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return 0, err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return 0, fmt.Errorf("invalid token claims")
	}
	switch exp := claims["exp"].(type) {
	case float64:
		return int64(exp), nil
	case int64:
		return exp, nil
	default:
		return 0, fmt.Errorf("missing exp claim")
	}
}
