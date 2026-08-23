package cloud

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// pendingTTL bounds how long a started connection may sit unfinished. It's
// generous enough for a "create a folder first" detour on the provider's
// consent screen, short enough that an abandoned attempt doesn't linger.
const pendingTTL = 15 * time.Minute

// pending is one in-flight authorization: the provider being connected, the
// PKCE verifier whose challenge the browser carried to the consent screen,
// and the redirect URI — which the token exchange must repeat byte for byte.
type pending struct {
	provider    string
	verifier    string
	redirectURI string
	expiresAt   time.Time
}

// pendingStore holds in-flight authorizations, keyed by the opaque `state`
// the provider echoes back. Memory is the right home: an authorization that
// doesn't complete before a restart is one the user simply retries, and
// keeping verifiers out of the settings file keeps them off disk entirely.
type pendingStore struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]pending
}

func newPendingStore(now func() time.Time) *pendingStore {
	if now == nil {
		now = time.Now
	}
	return &pendingStore{now: now, m: map[string]pending{}}
}

// put registers an authorization and returns its state parameter.
func (s *pendingStore) put(state string, p pending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.expiresAt = s.now().Add(pendingTTL)
	// Sweep on write: the store only grows when someone starts a connection,
	// so that's the only moment it can need trimming.
	for k, v := range s.m {
		if v.expiresAt.Before(s.now()) {
			delete(s.m, k)
		}
	}
	s.m[state] = p
}

// take consumes an authorization. A state is single-use: the second callback
// carrying it — a replay, or a double-submitted browser tab — finds nothing.
func (s *pendingStore) take(state string) (pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[state]
	if !ok {
		return pending{}, false
	}
	delete(s.m, state)
	if p.expiresAt.Before(s.now()) {
		return pending{}, false
	}
	return p, true
}

// ErrUnknownState is returned when a callback's state doesn't match a
// connection this server started (expired, replayed, or forged).
var ErrUnknownState = errors.New("this sign-in link has expired — start the connection again")

// randomURLSafe returns n bytes of randomness in the unpadded base64url
// alphabet, which is what both the `state` parameter and the PKCE verifier
// need (RFC 7636 §4.1).
func randomURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// codeChallenge is the S256 transform of a PKCE verifier.
func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
