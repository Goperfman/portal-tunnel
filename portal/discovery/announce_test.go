package discovery

import (
	"testing"
	"time"

	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

func mustSignedDescriptor(t *testing.T, signing types.Identity, relayName, relayURL string, issuedAt time.Time) types.RelayDescriptor {
	t.Helper()
	desc, err := utils.NormalizeDescriptor(types.RelayDescriptor{
		Identity: types.Identity{
			Name:    relayName,
			Address: signing.Address,
		},
		RelayID:      relayURL,
		Version:      1,
		IssuedAt:     issuedAt,
		ExpiresAt:    issuedAt.Add(DiscoveryDescriptorTTL),
		APIHTTPSAddr: relayURL,
		Discovery:    true,
	})
	if err != nil {
		t.Fatalf("NormalizeDescriptor() error = %v", err)
	}
	signed, err := SignDescriptor(desc, signing.PrivateKey)
	if err != nil {
		t.Fatalf("SignDescriptor() error = %v", err)
	}
	return signed
}

func TestInsertAnnouncedAcceptsValidDescriptor(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	desc := mustSignedDescriptor(t, signing, "relay-ann", "https://relay-ann.example", now)
	accepted, changed, err := set.InsertAnnounced(desc, now)
	if err != nil {
		t.Fatalf("InsertAnnounced() error = %v", err)
	}
	if !accepted || !changed {
		t.Fatalf("expected accept+change, got accepted=%v changed=%v", accepted, changed)
	}
	if got := set.AggregateRelays(); len(got) != 1 {
		t.Fatalf("len(AggregateRelays()) = %d, want 1", len(got))
	}
}

func TestInsertAnnouncedRejectsUnsigned(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	desc := mustNormalizedDescriptor(t, signing, "relay-unsigned", "https://relay-unsigned.example")
	if accepted, _, err := set.InsertAnnounced(desc, now); accepted || err == nil {
		t.Fatalf("expected unsigned reject, got accepted=%v err=%v", accepted, err)
	}
}

func TestInsertAnnouncedRejectsExpired(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	stale := now.Add(-2 * DiscoveryDescriptorTTL)
	desc := mustSignedDescriptor(t, signing, "relay-stale", "https://relay-stale.example", stale)
	if accepted, _, err := set.InsertAnnounced(desc, now); accepted || err == nil {
		t.Fatalf("expected expired reject, got accepted=%v err=%v", accepted, err)
	}
}

func TestInsertAnnouncedRejectsFutureClockSkew(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	future := now.Add(2 * AnnounceClockSkewTolerance)
	desc := mustSignedDescriptor(t, signing, "relay-future", "https://relay-future.example", future)
	if accepted, _, err := set.InsertAnnounced(desc, now); accepted || err == nil {
		t.Fatalf("expected future-skew reject, got accepted=%v err=%v", accepted, err)
	}
}

func TestInsertAnnouncedRejectsRollback(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	relayURL := "https://relay-roll.example"
	newer := mustSignedDescriptor(t, signing, "relay-roll", relayURL, now)
	if _, _, err := set.InsertAnnounced(newer, now); err != nil {
		t.Fatalf("seed insert error = %v", err)
	}
	older := mustSignedDescriptor(t, signing, "relay-roll", relayURL, now.Add(-time.Minute))
	if accepted, _, err := set.InsertAnnounced(older, now); accepted || err == nil {
		t.Fatalf("expected rollback reject, got accepted=%v err=%v", accepted, err)
	}
}

func TestInsertAnnouncedBlocksCrossIdentityTakeover(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	owner := mustSigningIdentity(t)
	attacker := mustSigningIdentity(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	relayURL := "https://relay-takeover.example"

	ownerDesc := mustSignedDescriptor(t, owner, "relay-takeover", relayURL, now)
	if _, _, err := set.InsertAnnounced(ownerDesc, now); err != nil {
		t.Fatalf("owner insert error = %v", err)
	}

	attackerDesc := mustSignedDescriptor(t, attacker, "relay-takeover", relayURL, now.Add(time.Second))
	if accepted, _, err := set.InsertAnnounced(attackerDesc, now); accepted || err == nil {
		t.Fatalf("expected takeover reject, got accepted=%v err=%v", accepted, err)
	}

	states := set.AggregateRelays()
	if len(states) != 1 {
		t.Fatalf("len(AggregateRelays()) = %d, want 1", len(states))
	}
	if got := states[0].Descriptor.Address; got != owner.Address {
		t.Fatalf("retained address = %q, want %q", got, owner.Address)
	}
}

func TestEnforceCapLockedEvictsOldestNonPinned(t *testing.T) {
	set, err := NewRelaySet([]string{"https://bootstrap.example"})
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	// Inject MaxAnnouncedRelays + extra candidates so eviction must run.
	// LastSeenAt is a strict ramp so we can assert exactly which slots
	// the oldest-first policy removed.
	const extra = 5
	set.mu.Lock()
	for i := range MaxAnnouncedRelays + extra {
		url := "https://stub-" + sprintInt(i) + ".example"
		set.relays[url] = RelayState{
			Descriptor: types.RelayDescriptor{
				Identity:     types.Identity{Address: signing.Address},
				APIHTTPSAddr: url,
				IssuedAt:     base.Add(time.Duration(i) * time.Second),
				ExpiresAt:    base.Add(time.Hour),
			},
			LastSeenAt: base.Add(time.Duration(i) * time.Second),
		}
	}
	// Pin one of the candidates as Confirmed at index 1 so we can assert
	// that pinned entries are NOT evicted even when they are old.
	pinnedURL := "https://stub-1.example"
	pinned := set.relays[pinnedURL]
	pinned.Confirmed = true
	set.relays[pinnedURL] = pinned

	preCount := len(set.relays)
	set.enforceCapLocked()
	postCount := len(set.relays)

	_, bootstrapPresent := set.relays["https://bootstrap.example"]
	_, pinnedPresent := set.relays[pinnedURL]
	// The oldest non-pinned candidates (index 0, then 2, 3, 4 — index 1 is
	// pinned) should have been evicted first. extra+1 entries are removed
	// because the bootstrap slot pushes total over the cap by one.
	_, oldest0Present := set.relays["https://stub-0.example"]
	_, oldest2Present := set.relays["https://stub-2.example"]
	_, newestPresent := set.relays["https://stub-"+sprintInt(MaxAnnouncedRelays+extra-1)+".example"]
	set.mu.Unlock()

	if preCount <= MaxAnnouncedRelays {
		t.Fatalf("test setup invalid: preCount=%d cap=%d", preCount, MaxAnnouncedRelays)
	}
	if postCount > MaxAnnouncedRelays {
		t.Fatalf("postCount=%d exceeds cap=%d", postCount, MaxAnnouncedRelays)
	}
	if !bootstrapPresent {
		t.Fatal("bootstrap entry must survive LRU eviction")
	}
	if !pinnedPresent {
		t.Fatal("Confirmed entry must survive LRU eviction even if old")
	}
	if oldest0Present {
		t.Fatal("oldest non-pinned entry (stub-0) must be evicted first")
	}
	if oldest2Present {
		t.Fatal("second-oldest non-pinned entry (stub-2) must also be evicted")
	}
	if !newestPresent {
		t.Fatal("newest entry must survive LRU eviction")
	}
}

func sprintInt(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 6)
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestAnnounceLimiterAllowsBurstThenThrottles(t *testing.T) {
	limiter := NewAnnounceLimiter(60, 5) // 1/sec sustained, burst 5
	for i := range 5 {
		if !limiter.Allow("10.0.0.1") {
			t.Fatalf("burst[%d] should be allowed", i)
		}
	}
	if limiter.Allow("10.0.0.1") {
		t.Fatal("burst budget should be exhausted")
	}
	if !limiter.Allow("10.0.0.2") {
		t.Fatal("different IP should have its own bucket")
	}
}

func TestAnnounceLimiterRefillsOverTime(t *testing.T) {
	limiter := NewAnnounceLimiter(60, 1) // 1/sec sustained, burst 1
	clock := time.Now()
	limiter.clock = func() time.Time { return clock }
	if !limiter.Allow("10.0.0.1") {
		t.Fatal("first request should be allowed")
	}
	if limiter.Allow("10.0.0.1") {
		t.Fatal("second immediate request should be throttled")
	}
	clock = clock.Add(2 * time.Second)
	if !limiter.Allow("10.0.0.1") {
		t.Fatal("after refill the request should be allowed again")
	}
}
