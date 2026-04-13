package discovery

import (
	"testing"
	"time"

	"github.com/gosuda/portal-tunnel/v2/types"
)

// TestLRUEvictionLeaksRollbackHistoryForSameIdentity exercises a replay-via-LRU
// attack. The rollback defense in upsertDescriptorLocked is anchored in
// s.keyIndex, which records the newest IssuedAt ever accepted for a signing
// identity. deleteRelayLocked drops the keyIndex entry as soon as the last
// URL slot for that identity is evicted — so if the legitimate relay's slot
// is pushed out by LRU pressure, the rollback history is lost.
//
// An attacker who captured an older (but still unexpired) signed descriptor
// can then re-announce it and the receiving relay will accept it as if it had
// never seen any newer IssuedAt. That is strictly contrary to the
// "monotonic IssuedAt per signing identity" invariant documented at
// relayset.go on upsertDescriptorLocked.
//
// This test must FAIL on a correct implementation (i.e. InsertAnnounced must
// reject the older descriptor), and PASS (in the failing-assertion sense)
// on the current implementation — surfacing the bug.
func TestLRUEvictionLeaksRollbackHistoryForSameIdentity(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	relayURL := "https://relay-replay.example"

	// Step 1: legitimate victim announces the NEWER descriptor. This populates
	// s.keyIndex[address] = t1.
	t1 := now
	t0 := now.Add(-2 * time.Minute) // strictly older but ExpiresAt is still in the future
	newer := mustSignedDescriptor(t, signing, "relay-replay", relayURL, t1)
	if accepted, _, err := set.InsertAnnounced(newer, now); err != nil || !accepted {
		t.Fatalf("seed announce: accepted=%v err=%v", accepted, err)
	}

	// Step 2: LRU pressure evicts the victim's slot. In production this
	// happens when MaxAnnouncedRelays is exceeded and the victim is the
	// oldest non-pinned candidate. We drive it through the same code path
	// enforceCapLocked uses so the assertion is faithful.
	set.mu.Lock()
	set.deleteRelayLocked(relayURL)
	set.mu.Unlock()

	// Step 3: attacker replays a strictly OLDER captured descriptor for the
	// same signing identity. Rollback defense MUST still reject it — the
	// rollback invariant is about the signing identity, not the URL slot.
	older := mustSignedDescriptor(t, signing, "relay-replay", relayURL, t0)
	accepted, _, err := set.InsertAnnounced(older, now)
	if accepted || err == nil {
		t.Fatalf(
			"rollback history was lost after LRU eviction: "+
				"InsertAnnounced(older) accepted=%v err=%v; "+
				"keyIndex must persist rollback history across URL-slot eviction",
			accepted, err,
		)
	}
}

// TestEnforceCapLockedSilentOverflowWhenEveryEntryPinned exercises the cap
// invariant under adversarial pinning. MaxAnnouncedRelays is documented at
// relaystate.go as a "hard ceiling" on the number of relay entries. The
// eviction implementation only considers non-Bootstrap, non-Confirmed
// entries as candidates — if every entry in the set is pinned, the candidate
// list is empty, the eviction loop is a no-op, and the map silently grows
// past the documented ceiling.
//
// This can be reached in practice when an operator bootstrap list grows past
// MaxAnnouncedRelays, or when a listener confirms more relays than the cap
// over a long-running session. Either way the invariant is broken and the
// memory ceiling is not honored.
func TestEnforceCapLockedSilentOverflowWhenEveryEntryPinned(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	// Populate MaxAnnouncedRelays + overflow entries, all marked Confirmed.
	// A correct eviction policy MUST still maintain the documented hard
	// ceiling; a silently-overflowing policy violates it.
	const overflow = 3
	set.mu.Lock()
	for i := range MaxAnnouncedRelays + overflow {
		url := "https://pinned-" + sprintInt(i) + ".example"
		set.relays[url] = RelayState{
			Descriptor: types.RelayDescriptor{
				Identity:     types.Identity{Address: signing.Address},
				APIHTTPSAddr: url,
				IssuedAt:     base.Add(time.Duration(i) * time.Second),
				ExpiresAt:    base.Add(time.Hour),
			},
			Confirmed:  true,
			LastSeenAt: base.Add(time.Duration(i) * time.Second),
		}
	}
	pre := len(set.relays)
	set.enforceCapLocked()
	post := len(set.relays)
	set.mu.Unlock()

	if pre <= MaxAnnouncedRelays {
		t.Fatalf("test setup invalid: pre=%d cap=%d", pre, MaxAnnouncedRelays)
	}
	if post > MaxAnnouncedRelays {
		t.Fatalf(
			"enforceCapLocked silently overflowed the hard ceiling: "+
				"post=%d cap=%d — when every entry is pinned, the eviction "+
				"loop has zero candidates and the set is left above "+
				"MaxAnnouncedRelays, violating the documented invariant",
			post, MaxAnnouncedRelays,
		)
	}
}
