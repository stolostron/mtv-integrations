package webhook

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/client-go/rest"
)

// TestImpersonatedConfig_ConcurrentRequestsDoNotRace guards against regressing to a design where
// the admission handler mutates a single shared rest.Config in place. That pattern lets one
// request's Impersonate assignment be clobbered by a concurrent request before it's used,
// causing an authorization check to run under the wrong user's identity. impersonatedConfig
// must instead return an independent copy per call, so concurrent callers can neither observe
// nor overwrite each other's impersonation identity. Run with `-race` to catch any reintroduced
// shared mutable state.
func TestImpersonatedConfig_ConcurrentRequestsDoNotRace(t *testing.T) {
	t.Parallel()
	base := rest.Config{Host: "https://hub.example.com"}

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			user := fmt.Sprintf("user-%d", i)
			group := fmt.Sprintf("group-%d", i)
			uid := fmt.Sprintf("uid-%d", i)
			extraKey := fmt.Sprintf("extra-%d", i)
			extraVal := fmt.Sprintf("val-%d", i)
			cfg := impersonatedConfig(base, authenticationv1.UserInfo{
				Username: user,
				Groups:   []string{group},
				UID:      uid,
				Extra:    map[string]authenticationv1.ExtraValue{extraKey: {extraVal}},
			})
			switch {
			case cfg.Impersonate.UserName != user:
				errs[i] = fmt.Errorf("goroutine %d: got impersonated user %q, want %q", i, cfg.Impersonate.UserName, user)
			case len(cfg.Impersonate.Groups) != 1 || cfg.Impersonate.Groups[0] != group:
				errs[i] = fmt.Errorf("goroutine %d: got groups %v, want [%q]", i, cfg.Impersonate.Groups, group)
			case cfg.Impersonate.UID != uid:
				errs[i] = fmt.Errorf("goroutine %d: got UID %q, want %q", i, cfg.Impersonate.UID, uid)
			case len(cfg.Impersonate.Extra[extraKey]) != 1 || cfg.Impersonate.Extra[extraKey][0] != extraVal:
				errs[i] = fmt.Errorf("goroutine %d: got impersonated extra %v, want {%q: [%q]}",
					i, cfg.Impersonate.Extra, extraKey, extraVal)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "request %d must retain its own impersonated identity, including Groups, UID, and Extra", i)
	}

	// The base config read by every goroutine above must remain untouched: per-request
	// impersonation must never leak back into the value every request reads from.
	assert.Equal(t, rest.ImpersonationConfig{}, base.Impersonate,
		"the shared base configuration must remain unchanged by any per-request impersonation copy")
}

func TestCopyExtra(t *testing.T) {
	t.Parallel()

	t.Run("nil input returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, copyExtra(nil), "nil Extra input must remain nil")
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, copyExtra(map[string]authenticationv1.ExtraValue{}), "empty Extra input must return nil")
	})

	t.Run("copies keys and values without sharing backing storage", func(t *testing.T) {
		t.Parallel()
		src := map[string]authenticationv1.ExtraValue{
			"scopes": {"read", "write"},
		}
		got := copyExtra(src)
		require.Equal(t, map[string][]string{"scopes": {"read", "write"}}, got,
			"copyExtra must preserve Extra keys and values")

		// Mutating the copy must not affect the source ExtraValue slice.
		got["scopes"][0] = "mutated"
		assert.Equal(t, authenticationv1.ExtraValue{"read", "write"}, src["scopes"],
			"mutating the copied values must not change the source")
	})
}
