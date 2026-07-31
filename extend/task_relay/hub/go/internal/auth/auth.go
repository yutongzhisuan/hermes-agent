package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	algorithm  = "HS256"
	masterRole = "master"
)

// BootstrapEntry scopes a long-lived bootstrap credential to one worker.
type BootstrapEntry struct {
	WorkerID        string
	AllowedToolsets []string
	MaxConcurrent   int
}

// Auth verifies and issues Hub JWTs (Master scope for the Go port scaffold).
type Auth struct {
	secret          []byte
	issuer          string
	audience        string
	ttl             time.Duration
	bootstrapTokens map[string]BootstrapEntry
}

// New constructs an Auth verifier/signer.
func New(secret, issuer, audience string, ttl time.Duration, bootstrap map[string]BootstrapEntry) (*Auth, error) {
	if secret == "" {
		return nil, fmt.Errorf("jwt secret is required")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	if bootstrap == nil {
		bootstrap = map[string]BootstrapEntry{}
	}
	return &Auth{
		secret:          []byte(secret),
		issuer:          issuer,
		audience:        audience,
		ttl:             ttl,
		bootstrapTokens: bootstrap,
	}, nil
}

// SecretBytes returns the HS256 signing key (shared with wake HMAC).
func (a *Auth) SecretBytes() []byte {
	if a == nil {
		return nil
	}
	return a.secret
}

// ExchangeBootstrap validates a bootstrap token and issues a scoped worker JWT.
func (a *Auth) ExchangeBootstrap(bootstrapToken, workerID string) (string, error) {
	entry, ok := a.bootstrapTokens[bootstrapToken]
	if !ok {
		return "", fmt.Errorf("unknown bootstrap token")
	}
	if entry.WorkerID != workerID {
		return "", fmt.Errorf("bootstrap token not issued for this worker_id")
	}
	maxConcurrent := entry.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return a.IssueWorkerJWT(workerID, append([]string(nil), entry.AllowedToolsets...), maxConcurrent, a.ttl)
}

// IssueMasterJWT returns a short-lived Master token.
func (a *Auth) IssueMasterJWT(masterID string, ttl time.Duration) (string, error) {
	if masterID == "" {
		return "", fmt.Errorf("master id is required")
	}
	if ttl <= 0 {
		ttl = a.ttl
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":  masterID,
		"aud":  a.audience,
		"iss":  a.issuer,
		"role": masterRole,
		"exp":  now.Add(ttl).Unix(),
		"iat":  now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.secret)
}

// VerifyMasterJWT validates a Master bearer token.
func (a *Auth) VerifyMasterJWT(token string) (string, error) {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return a.secret, nil
	}, jwt.WithAudience(a.audience), jwt.WithIssuer(a.issuer))
	if err != nil {
		return "", err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return "", fmt.Errorf("invalid token claims")
	}
	if claims["role"] != masterRole {
		return "", fmt.Errorf("not a master token")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", fmt.Errorf("missing sub claim")
	}
	return sub, nil
}
