package discovery

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

// RelaySet owns the shared relay discovery view: configured bootstrap relay URLs,
// the latest validated descriptor seen for each relay, and local runtime state
// such as ban/failure tracking and observed discovery RTT.
//
// The relays map is keyed by APIHTTPSAddr (URL). The keyIndex map provides a
// reverse lookup from signing identity (the EVM address derived from the
// signing public key, lower-cased) to the most recent IssuedAt we have ever
// accepted for that identity, along with a tombstone TombstoneUntil that
// records how long the rollback anchor must be remembered. The keyIndex is
// the rollback-defense gate: any descriptor whose IssuedAt is strictly older
// than the recorded latest is rejected before reaching s.relays. Tracking by
// signing key (rather than URL) means a single relay rotating its
// APIHTTPSAddr cannot be tricked into accepting a stale rollback simply by
// submitting it under a new URL.
//
// The keyIndex lifetime is deliberately decoupled from s.relays: evicting the
// last URL slot for an identity (via LRU or explicit removal) MUST NOT forget
// the rollback anchor, otherwise a captured older-but-unexpired descriptor
// could be replayed after eviction. Tombstones expire once the replay window
// closes, i.e. once now > IssuedAt + AnnounceMaxValidity — by that time any
// descriptor whose IssuedAt is ≤ the tombstoned value is strictly expired and
// cannot pass the announce validity check regardless.
//
// Both maps must always be read and written under s.mu. Mutators come in two
// flavors: public methods that own the lock end-to-end, and *Locked helpers
// that assume the caller already holds s.mu as a write lock and never re-
// acquire it themselves. This convention prevents nested-locking deadlocks
// (notably from ApplyRelayDiscoveryResponse, which holds the write lock for
// the entire batch).
type RelaySet struct {
	mu       sync.RWMutex
	relays   map[string]RelayState
	keyIndex map[string]keyIndexEntry
	policy   RelayPolicy
}

// keyIndexEntry records the rollback anchor for a signing identity.
// IssuedAt is the newest descriptor IssuedAt the set has ever accepted
// for this identity. TombstoneUntil is the wall-clock time at which the
// rollback anchor may safely be forgotten — after that point, any
// replayable descriptor with an older IssuedAt is itself expired.
type keyIndexEntry struct {
	IssuedAt       time.Time
	TombstoneUntil time.Time
}

func NewRelaySet(bootstrapRelayURLs []string) (*RelaySet, error) {
	set := &RelaySet{
		relays:   make(map[string]RelayState),
		keyIndex: make(map[string]keyIndexEntry),
		policy:   DefaultRelayPolicy{},
	}
	if err := set.SetBootstrapRelayURLs(bootstrapRelayURLs); err != nil {
		return nil, err
	}
	return set, nil
}

// keyIndexAddress returns the lower-cased EVM address used as the keyIndex
// key for a given relay state. Empty for stub entries that carry no signed
// descriptor (e.g. bootstrap URL placeholders before the first refresh).
func keyIndexAddress(state RelayState) string {
	return strings.ToLower(strings.TrimSpace(state.Descriptor.Address))
}

// upsertDescriptorLocked applies a fully-merged RelayState to s.relays and
// updates the keyIndex. The caller MUST already hold s.mu as a write lock.
//
// The returned bool indicates whether the descriptor was accepted. The
// upsert is rejected when:
//
//  1. The signing identity has previously published a strictly newer
//     IssuedAt (rollback defense).
//  2. The URL slot is already held by a DIFFERENT signing identity whose
//     descriptor has not yet expired, and `allowCrossIdentityTakeover` is
//     false. This blocks third-party gossip/announce from hijacking a URL
//     binding established by direct authoritative contact.
//
// `allowCrossIdentityTakeover` MUST be true only when the caller has
// directly contacted the URL and verified the response is signed by the
// announced identity (i.e. authoritative refresh). Gossip propagation and
// the announce endpoint MUST pass false.
//
// Equal IssuedAt values (idempotent re-broadcast) are accepted because the
// only mutation is the merged local telemetry on the existing URL slot,
// which never contradicts the cryptographic identity of the descriptor.
func (s *RelaySet) upsertDescriptorLocked(record RelayState, now time.Time, allowCrossIdentityTakeover bool) bool {
	relayURL := record.Descriptor.APIHTTPSAddr
	if relayURL == "" {
		return false
	}
	address := keyIndexAddress(record)
	if address != "" {
		if prev, ok := s.keyIndex[address]; ok {
			// Stale tombstone: no replayable descriptor could still be
			// within its validity window, so drop the anchor and accept
			// the fresh descriptor as if first-seen.
			if !prev.TombstoneUntil.IsZero() && now.After(prev.TombstoneUntil) {
				delete(s.keyIndex, address)
			} else if record.Descriptor.IssuedAt.Before(prev.IssuedAt) {
				return false
			}
		}
	}
	if !allowCrossIdentityTakeover {
		if existing, ok := s.relays[relayURL]; ok {
			existingAddress := keyIndexAddress(existing)
			if existingAddress != "" && address != "" && existingAddress != address {
				if !existing.Descriptor.ExpiresAt.IsZero() && existing.Descriptor.ExpiresAt.After(now) {
					return false
				}
			}
		}
	}
	s.relays[relayURL] = record
	if address != "" {
		issuedAt := record.Descriptor.IssuedAt
		tombstoneUntil := issuedAt.Add(AnnounceMaxValidity)
		if prev, ok := s.keyIndex[address]; ok {
			if prev.IssuedAt.After(issuedAt) {
				issuedAt = prev.IssuedAt
			}
			if prev.TombstoneUntil.After(tombstoneUntil) {
				tombstoneUntil = prev.TombstoneUntil
			}
		}
		s.keyIndex[address] = keyIndexEntry{
			IssuedAt:       issuedAt,
			TombstoneUntil: tombstoneUntil,
		}
	}
	return true
}

// deleteRelayLocked removes a URL slot from s.relays. The keyIndex tombstone
// is intentionally NOT dropped here: the rollback anchor must outlive the
// URL slot so that LRU eviction cannot be used as a laundering step for a
// captured older-but-unexpired descriptor from the same signing identity.
// Stale tombstones are swept by pruneKeyIndexLocked, called from
// enforceCapLocked after every insert. The caller MUST already hold s.mu
// as a write lock.
func (s *RelaySet) deleteRelayLocked(relayURL string) {
	if _, ok := s.relays[relayURL]; !ok {
		return
	}
	delete(s.relays, relayURL)
}

// pruneKeyIndexLocked drops keyIndex tombstones whose replay-window has
// closed. A tombstone at `now.After(entry.TombstoneUntil)` cannot gate any
// live descriptor: the oldest replayable descriptor from the same identity
// would itself be expired (since honest announces cap validity at
// AnnounceMaxValidity). Callers MUST already hold s.mu as a write lock.
func (s *RelaySet) pruneKeyIndexLocked(now time.Time) {
	for address, entry := range s.keyIndex {
		if entry.TombstoneUntil.IsZero() {
			continue
		}
		if now.After(entry.TombstoneUntil) {
			delete(s.keyIndex, address)
		}
	}
}

func (s *RelaySet) SetRelayPolicy(policy RelayPolicy) {
	if policy == nil {
		policy = DefaultRelayPolicy{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = policy
}

func (s *RelaySet) SetBootstrapRelayURLs(inputs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	keep := make(map[string]struct{}, len(inputs))
	for _, relayURL := range inputs {
		keep[relayURL] = struct{}{}
	}

	for key, state := range s.relays {
		_, bootstrap := keep[key]
		state.Bootstrap = bootstrap
		if !state.Bootstrap && !state.hasDescriptor() && !state.Banned && state.consecutiveFailures == 0 {
			s.deleteRelayLocked(key)
			continue
		}

		s.relays[key] = state
	}

	for _, relayURL := range inputs {
		if _, ok := s.relays[relayURL]; ok {
			continue
		}

		state := newRelayStateFromURL(relayURL)
		state.Bootstrap = true
		s.relays[relayURL] = state
	}
	return nil
}

func (s *RelaySet) AggregateRelays() []RelayState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.policy.SelectAggregate(s.relayStatesLocked())
}

func (s *RelaySet) ConfirmedRelays() []RelayState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.policy.SelectConfirmed(s.relayStatesLocked())
}

func (s *RelaySet) PriorityRelays(clientState ClientState) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.policy.SelectPriority(s.relayStatesLocked(), clientState)
}

func (s *RelaySet) OverlayPeerStates() []RelayState {
	s.mu.RLock()
	states := s.relayStatesLocked()
	s.mu.RUnlock()

	now := time.Now().UTC()
	out := make([]RelayState, 0, len(states))
	for _, state := range states {
		if state.Banned || !state.hasDescriptor() || !state.Descriptor.ExpiresAt.After(now) || !state.Descriptor.SupportsOverlayPeer {
			continue
		}
		if state.Descriptor.WireGuardPublicKey == "" ||
			state.Descriptor.WireGuardEndpoint == "" ||
			state.Descriptor.OverlayIPv4 == "" {
			continue
		}
		out = append(out, state)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *RelaySet) Descriptors() []types.RelayDescriptor {
	s.mu.RLock()
	states := s.relayStatesLocked()
	s.mu.RUnlock()

	now := time.Now().UTC()
	out := make([]types.RelayDescriptor, 0, len(states))
	for _, state := range states {
		if state.Banned || !state.hasDescriptor() || !state.Descriptor.Discovery {
			continue
		}
		desc := state.Descriptor
		if !desc.ExpiresAt.After(now) {
			if state.LastSeenAt.IsZero() || !state.LastSeenAt.After(now.Add(-DiscoveryHintRetentionTTL)) {
				continue
			}

			// Keep stale relay hints flowing through discovery so the mesh converges
			// on a large shared relay set. Local listener confirmation and direct
			// refresh retry state are tracked separately.
			desc.ExpiresAt = now.Add(DiscoveryDescriptorTTL)
		}
		out = append(out, desc)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *RelaySet) ServeDiscovery(w http.ResponseWriter, r *http.Request, local ...types.RelayDescriptor) {
	if !utils.RequireMethod(w, r, http.MethodGet) {
		return
	}

	known := s.Descriptors()
	relays := make([]types.RelayDescriptor, 0, len(local)+len(known))
	seen := make(map[string]struct{}, len(local)+len(known))
	add := func(descriptor types.RelayDescriptor) {
		relayURL := descriptor.APIHTTPSAddr
		if relayURL == "" {
			return
		}
		if _, ok := seen[relayURL]; ok {
			return
		}
		seen[relayURL] = struct{}{}
		relays = append(relays, descriptor)
	}

	for _, descriptor := range local {
		add(descriptor)
	}
	for _, descriptor := range known {
		add(descriptor)
	}

	utils.WriteAPIData(w, http.StatusOK, types.DiscoveryResponse{
		ProtocolVersion: types.DiscoveryVersion,
		GeneratedAt:     time.Now().UTC(),
		Relays:          relays,
	})
}

func (s *RelaySet) relayStatesLocked() []RelayState {
	out := make([]RelayState, 0, len(s.relays))
	for _, state := range s.relays {
		out = append(out, state)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Descriptor.APIHTTPSAddr < out[j].Descriptor.APIHTTPSAddr
	})
	return out
}

func (s *RelaySet) BanRelayURL(relayURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.relays[relayURL]
	if !ok {
		state = newRelayStateFromURL(relayURL)
	}
	state = s.policy.OnBanned(state)
	s.relays[relayURL] = state
}

func (s *RelaySet) ConfirmRelayURL(relayURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.relays[relayURL]
	if !ok {
		state = newRelayStateFromURL(relayURL)
	}
	state = s.policy.OnConfirmed(state)
	s.relays[relayURL] = state
}

func (s *RelaySet) UnconfirmRelayURL(relayURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.relays[relayURL]
	if !ok {
		return
	}
	state = s.policy.OnUnconfirmed(state)
	s.relays[relayURL] = state
}

func (s *RelaySet) ApplyRelayDiscoveryResponse(targetURL string, resp types.DiscoveryResponse, now time.Time) (relaySetChanged bool, err error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	protocolMismatch := resp.ProtocolVersion != types.DiscoveryVersion
	authoritative := targetURL != ""

	s.mu.Lock()
	defer s.mu.Unlock()

	discoveredByURL := make(map[string]RelayState, len(resp.Relays))
	discoveredOrder := make([]string, 0, len(resp.Relays)+1)
	targetFound := false
	add := func(descriptor types.RelayDescriptor) {
		// Cryptographic gate: every gossiped descriptor must carry a valid
		// signature. Unsigned or invalid-signature descriptors are dropped
		// silently — they cannot poison the local relay set, and other peers
		// will reach the same verdict independently. This is the sole global
		// trust gate under unconditional propagation, so it is mandatory.
		if _, verifyErr := VerifyDescriptor(descriptor); verifyErr != nil {
			return
		}
		relayState, err := newRelayState(descriptor, now)
		if err != nil {
			return
		}
		relayURL := relayState.Descriptor.APIHTTPSAddr
		if relayURL == "" {
			return
		}
		if authoritative && relayURL == targetURL {
			targetFound = true
		}
		if _, ok := discoveredByURL[relayURL]; !ok {
			discoveredOrder = append(discoveredOrder, relayURL)
		}
		discoveredByURL[relayURL] = relayState
	}
	for _, descriptor := range resp.Relays {
		add(descriptor)
	}
	missingTarget := authoritative && !targetFound

	for _, relayURL := range discoveredOrder {
		record := discoveredByURL[relayURL]
		existingAtURL, hasExistingAtURL := s.relays[relayURL]
		record.Bootstrap = record.Bootstrap || existingAtURL.Bootstrap
		record.Confirmed = record.Confirmed || existingAtURL.Confirmed
		record.Banned = record.Banned || existingAtURL.Banned
		if record.consecutiveFailures < existingAtURL.consecutiveFailures {
			record.consecutiveFailures = existingAtURL.consecutiveFailures
		}
		record.nextDirectRefreshAt = existingAtURL.nextDirectRefreshAt
		if record.DiscoveryRTTAt.IsZero() || (!existingAtURL.DiscoveryRTTAt.IsZero() && existingAtURL.DiscoveryRTTAt.After(record.DiscoveryRTTAt)) {
			record.DiscoveryRTT = existingAtURL.DiscoveryRTT
			record.DiscoveryRTTAt = existingAtURL.DiscoveryRTTAt
		}

		isAuthoritativeTarget := !protocolMismatch && !missingTarget && authoritative && relayURL == targetURL
		if isAuthoritativeTarget {
			record.consecutiveFailures = 0
			record.nextDirectRefreshAt = time.Time{}
		}

		if !s.upsertDescriptorLocked(record, now, isAuthoritativeTarget) {
			// The monotonic-IssuedAt check rejected this descriptor as a
			// rollback. The cryptographic identity in s.relays is unchanged,
			// but if we successfully reached the authoritative target we
			// should still credit it as alive on its existing URL slot.
			if isAuthoritativeTarget && hasExistingAtURL {
				if existingAtURL.consecutiveFailures != 0 || !existingAtURL.nextDirectRefreshAt.IsZero() {
					existingAtURL.consecutiveFailures = 0
					existingAtURL.nextDirectRefreshAt = time.Time{}
					s.relays[relayURL] = existingAtURL
					relaySetChanged = true
				}
			}
			continue
		}

		if !hasExistingAtURL || !reflect.DeepEqual(existingAtURL, record) {
			relaySetChanged = true
		}
	}
	s.enforceCapLocked()
	if missingTarget {
		return relaySetChanged, errors.New("target relay descriptor missing from relays")
	}
	if protocolMismatch && authoritative {
		return relaySetChanged, fmt.Errorf("relay discovery protocol version mismatch: relay=%q client=%q", resp.ProtocolVersion, types.DiscoveryVersion)
	}
	return relaySetChanged, nil
}

func (s *RelaySet) RecordDiscoveryRTT(relayURL string, rtt time.Duration, measuredAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.relays[relayURL]
	if !ok {
		return
	}

	state.DiscoveryRTT = rtt
	state.DiscoveryRTTAt = measuredAt
	s.relays[relayURL] = state
}

// InsertAnnounced ingests a single descriptor submitted via the announce
// endpoint. It is the only public mutator that is intended to be reachable
// from external (untrusted) callers. The full validation pipeline runs
// inline:
//
//  1. The descriptor signature is verified against the recovered public key
//     and matched to the descriptor's Address field.
//  2. The descriptor must be currently valid (ExpiresAt strictly in the
//     future) and not significantly clock-skewed (IssuedAt no further into
//     the future than AnnounceClockSkewTolerance, validity window no longer
//     than AnnounceMaxValidity).
//  3. Local merge preserves Bootstrap, Confirmed, Banned, telemetry, and
//     direct-refresh retry state from any pre-existing entry at the same URL.
//  4. The shared upsertDescriptorLocked helper enforces the
//     monotonic-IssuedAt-per-key rollback guard and the cross-identity
//     URL-takeover guard. Announce never grants takeover authority — only
//     direct authoritative refresh can do that.
//  5. After a successful upsert, the LRU cap is enforced; bootstrap and
//     listener-confirmed entries are pinned.
//
// Returns (accepted, changed, err): accepted=true iff the descriptor was
// stored (or was an idempotent refresh). changed=true iff s.relays was
// mutated. The error categories are exported as Err* sentinels so callers
// can map to HTTP statuses.
func (s *RelaySet) InsertAnnounced(desc types.RelayDescriptor, now time.Time) (accepted bool, changed bool, err error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	if _, verifyErr := VerifyDescriptor(desc); verifyErr != nil {
		return false, false, verifyErr
	}
	normalized, err := utils.NormalizeDescriptor(desc)
	if err != nil {
		return false, false, fmt.Errorf("normalize announced descriptor: %w", err)
	}
	if normalized.IssuedAt.IsZero() {
		return false, false, errors.New("announced descriptor missing issued_at")
	}
	if normalized.ExpiresAt.IsZero() {
		return false, false, errors.New("announced descriptor missing expires_at")
	}
	if !normalized.ExpiresAt.After(now) {
		return false, false, errors.New("announced descriptor already expired")
	}
	if normalized.IssuedAt.After(now.Add(AnnounceClockSkewTolerance)) {
		return false, false, errors.New("announced descriptor is too far in the future")
	}
	if normalized.ExpiresAt.Sub(normalized.IssuedAt) > AnnounceMaxValidity {
		return false, false, errors.New("announced descriptor validity window exceeds maximum")
	}

	record, err := newRelayState(normalized, now)
	if err != nil {
		return false, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	relayURL := record.Descriptor.APIHTTPSAddr
	if existing, ok := s.relays[relayURL]; ok {
		record.Bootstrap = record.Bootstrap || existing.Bootstrap
		record.Confirmed = record.Confirmed || existing.Confirmed
		record.Banned = record.Banned || existing.Banned
		if record.consecutiveFailures < existing.consecutiveFailures {
			record.consecutiveFailures = existing.consecutiveFailures
		}
		record.nextDirectRefreshAt = existing.nextDirectRefreshAt
		if record.DiscoveryRTTAt.IsZero() || (!existing.DiscoveryRTTAt.IsZero() && existing.DiscoveryRTTAt.After(record.DiscoveryRTTAt)) {
			record.DiscoveryRTT = existing.DiscoveryRTT
			record.DiscoveryRTTAt = existing.DiscoveryRTTAt
		}
	}

	if !s.upsertDescriptorLocked(record, now, false) {
		return false, false, errors.New("announced descriptor rejected by rollback or takeover guard")
	}

	s.enforceCapLocked()
	return true, true, nil
}

// enforceCapLocked trims s.relays back to MaxAnnouncedRelays using a
// two-tier eviction strategy: non-Bootstrap non-Confirmed entries are
// evicted first (oldest by LastSeenAt), then non-Bootstrap Confirmed
// entries as a last resort. Bootstrap entries are absolutely pinned —
// an operator misconfig that lists more than MaxAnnouncedRelays bootstraps
// is surfaced by the resulting overflow rather than silently violating
// operator intent. Tombstone keyIndex entries whose replay window has
// closed are swept opportunistically. The caller MUST already hold s.mu
// as a write lock.
func (s *RelaySet) enforceCapLocked() {
	s.pruneKeyIndexLocked(time.Now().UTC())
	if len(s.relays) <= MaxAnnouncedRelays {
		return
	}
	type ageEntry struct {
		url       string
		confirmed bool
		seenAt    time.Time
	}
	candidates := make([]ageEntry, 0, len(s.relays))
	for url, state := range s.relays {
		if state.Bootstrap {
			continue
		}
		candidates = append(candidates, ageEntry{
			url:       url,
			confirmed: state.Confirmed,
			seenAt:    state.LastSeenAt,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		// Non-confirmed entries evict first — confirmed is the last-resort
		// tier. Within each tier, oldest LastSeenAt evicts first.
		if candidates[i].confirmed != candidates[j].confirmed {
			return !candidates[i].confirmed
		}
		return candidates[i].seenAt.Before(candidates[j].seenAt)
	})
	for _, c := range candidates {
		if len(s.relays) <= MaxAnnouncedRelays {
			return
		}
		s.deleteRelayLocked(c.url)
	}
}

func (s *RelaySet) RecordRelayFailure(relayURL string, err error, recoveryFailures int) (backedOff bool, backoffReason string, consecutiveFailures int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.relays[relayURL]
	if !ok {
		return false, "", 0
	}
	state, backedOff, backoffReason = s.policy.OnFailure(state, err, recoveryFailures)
	s.relays[relayURL] = state
	return backedOff, backoffReason, state.consecutiveFailures
}
