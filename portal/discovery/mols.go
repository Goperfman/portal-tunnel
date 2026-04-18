package discovery

// MOLSRelayPolicy implements RelayPolicy using a Multi-path Orthogonal Latin
// Squares (MOLS) engine over GF(2⁶).  SelectPriority provides deterministic,
// load-balanced, and collision-resistant relay scoring without requiring a
// central coordinator.  All other policy callbacks delegate to
// DefaultRelayPolicy.
//
// # Core Design
//
// The engine uses an order-64 MOLS grid derived from Galois Field GF(64).
// For a given ingress (local) node i and candidate relay j the base score is:
//
//	L_m[i][j] = gf64Mul(m, i) XOR j          (Latin-square row for multiplier m)
//	score(i, j) = L_m1[i][j] * 64 + L_m2[i][j] + 1   (composite, range 1..4096)
//
// # Congestion Switching (Reverse-Siamese)
//
// When the mean discovery RTT across the auto pool exceeds
// molsCongestionRTTThreshold, the engine applies:
//
//	congestionScore(i, j) = (n²+1) − score(i, 63−j)
//
// This mirrors the priority ordering so underutilised paths move to the front.
//
// # Non-Linear Load (Variant Grid)
//
// When the coefficient of variation of per-relay RTTs exceeds molsCVThreshold
// (indicating bursty load), the engine switches multipliers from (3, 5) to
// (7, 11).  Non-linear detection takes precedence over congestion switching.
//
// # Health & Fallback
//
// Relays whose measured discovery RTT exceeds molsFallbackRTTThreshold are
// treated as Fallback and placed at the end of the priority queue.  The engine
// ensures at least molsMinActiveNodes non-fallback relays remain reachable; if
// fewer are available, Fallback relays are promoted to meet the minimum.

import (
	"hash/fnv"
	"math"
	"slices"
	"sort"
	"time"
)

const (
	molsOrder         = 64
	molsMagicConstant = molsOrder*molsOrder + 1 // n²+1 = 4097

	molsBaseM1    uint8 = 3
	molsBaseM2    uint8 = 5
	molsVariantM1 uint8 = 7
	molsVariantM2 uint8 = 11

	// molsCongestionRTTThreshold is the mean discovery RTT above which the
	// Reverse-Siamese complement switch is applied.
	molsCongestionRTTThreshold = 500 * time.Millisecond

	// molsCVThreshold is the coefficient-of-variation threshold above which
	// the variant MOLS grid (multipliers 7, 11) is used instead of the base
	// grid (multipliers 3, 5).  Non-linear detection takes precedence.
	molsCVThreshold = 0.5

	// molsFallbackRTTThreshold is the discovery RTT above which a relay is
	// classified as Fallback (consistently slow) and demoted to the end of
	// the priority queue.
	molsFallbackRTTThreshold = 2 * time.Second

	// molsMinActiveNodes is the minimum number of non-fallback relays the
	// engine keeps in the active pool.  Fallback relays are promoted when
	// the active pool drops below this count.
	molsMinActiveNodes = 2
)

// gf64Mul multiplies two GF(2⁶) elements modulo the primitive polynomial
// x⁶ + x + 1 (0x43).  Both inputs and the return value are in [0, 63].
func gf64Mul(a, b uint8) uint8 {
	a &= 0x3f
	b &= 0x3f
	var r uint8
	for b != 0 {
		if b&1 != 0 {
			r ^= a
		}
		if a&0x20 != 0 {
			a = ((a << 1) ^ 0x43) & 0x3f
		} else {
			a = (a << 1) & 0x3f
		}
		b >>= 1
	}
	return r
}

// molsScore returns the composite MOLS cell value for ingress i, candidate j,
// and Latin-square multipliers m1 and m2 in GF(64).  The result is in
// [1, n²] (1-indexed) so it can participate in magic-square sum identities.
func molsScore(i, j, m1, m2 uint8) int {
	l1 := gf64Mul(m1, i) ^ j
	l2 := gf64Mul(m2, i) ^ j
	return int(l1)*molsOrder + int(l2) + 1
}

// molsCongestionScore applies the Reverse-Siamese complement to molsScore:
//
//	B(i, j) = (n²+1) − A(i, n−1−j)   [0-indexed]
//
// This mirrors the column ordering so relays that were last become first.
func molsCongestionScore(i, j, m1, m2 uint8) int {
	return molsMagicConstant - molsScore(i, (molsOrder-1)-j, m1, m2)
}

// hashToGF64 deterministically maps an arbitrary string to a GF(64) element
// in [0, 63] using 32-bit FNV-1a.
func hashToGF64(s string) uint8 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return uint8(h.Sum32() & 0x3f)
}

// molsRTTStats computes the arithmetic mean and coefficient of variation (CV)
// of discovery RTTs across states.  Relays without a measured RTT are excluded
// from both calculations.  When there are fewer than two samples the CV is 0.
func molsRTTStats(states []RelayState) (mean time.Duration, cv float64) {
	var samples []float64
	for _, s := range states {
		if s.DiscoveryRTTAt.IsZero() {
			continue
		}
		samples = append(samples, float64(s.DiscoveryRTT))
	}
	if len(samples) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range samples {
		sum += v
	}
	avg := sum / float64(len(samples))
	if len(samples) == 1 {
		return time.Duration(avg), 0
	}
	var sq float64
	for _, v := range samples {
		d := v - avg
		sq += d * d
	}
	stddev := math.Sqrt(sq / float64(len(samples)))
	if avg > 0 {
		cv = stddev / avg
	}
	return time.Duration(avg), cv
}

// isRelayFallback reports whether state should be treated as a Fallback relay.
// A relay is classified Fallback when it has a measured discovery RTT that
// exceeds molsFallbackRTTThreshold, indicating sustained latency.
func isRelayFallback(state RelayState) bool {
	return !state.DiscoveryRTTAt.IsZero() && state.DiscoveryRTT > molsFallbackRTTThreshold
}

// MOLSRelayPolicy implements the MOLS-based relay selection engine.
type MOLSRelayPolicy struct{}

func (p MOLSRelayPolicy) SelectAggregate(states []RelayState) []RelayState {
	return DefaultRelayPolicy{}.SelectAggregate(states)
}

func (p MOLSRelayPolicy) SelectConfirmed(states []RelayState) []RelayState {
	return DefaultRelayPolicy{}.SelectConfirmed(states)
}

func (p MOLSRelayPolicy) OnConfirmed(state RelayState) RelayState {
	return DefaultRelayPolicy{}.OnConfirmed(state)
}

func (p MOLSRelayPolicy) OnUnconfirmed(state RelayState) RelayState {
	return DefaultRelayPolicy{}.OnUnconfirmed(state)
}

func (p MOLSRelayPolicy) OnFailure(state RelayState, err error, recoveryFailures int) (RelayState, bool, string) {
	return DefaultRelayPolicy{}.OnFailure(state, err, recoveryFailures)
}

func (p MOLSRelayPolicy) OnBanned(state RelayState) RelayState {
	return DefaultRelayPolicy{}.OnBanned(state)
}

// SelectPriority returns an ordered list of relay URLs for the client to
// connect to, ranked by MOLS-derived scores.
//
// Explicit relays (from clientState.ExplicitRelayURLs) are always prepended
// outside of the MaxActiveRelays budget, matching DefaultRelayPolicy behaviour.
// The auto pool is scored with the MOLS grid; congestion or non-linear load
// conditions switch the active grid variant.  Fallback relays are appended
// after all healthy relays, ensuring the network stays connected during mass
// degradation while keeping them deprioritised under normal conditions.
func (p MOLSRelayPolicy) SelectPriority(states []RelayState, clientState ClientState) []string {
	selected := DefaultRelayPolicy{}.SelectAggregate(states)
	if len(selected) == 0 {
		return nil
	}

	// Filter by transport requirements and split into explicit / auto pools.
	explicit := make([]string, 0)
	autoPool := make([]RelayState, 0, len(selected))
	for _, state := range selected {
		if clientState.RequireUDP && state.hasObservedDescriptor() && !state.Descriptor.SupportsUDP {
			continue
		}
		if clientState.RequireTCP && state.hasObservedDescriptor() && !state.Descriptor.SupportsTCP {
			continue
		}
		relayURL := state.Descriptor.APIHTTPSAddr
		if slices.Contains(clientState.ExplicitRelayURLs, relayURL) {
			explicit = append(explicit, relayURL)
			continue
		}
		autoPool = append(autoPool, state)
	}
	if len(explicit) == 0 && len(autoPool) == 0 {
		return nil
	}

	// Derive the ingress (local) index into the MOLS grid.
	ingressIdx := hashToGF64(clientState.LocalAddress)

	// Detect congestion and non-linear load from the auto pool's RTT samples.
	avgRTT, cv := molsRTTStats(autoPool)
	congested := avgRTT > molsCongestionRTTThreshold
	nonLinear := cv > molsCVThreshold

	// Choose grid multipliers; non-linear load takes precedence.
	m1, m2 := molsBaseM1, molsBaseM2
	if nonLinear {
		m1, m2 = molsVariantM1, molsVariantM2
	}

	// Separate relays into active (healthy) and fallback (slow / degraded).
	active := make([]RelayState, 0, len(autoPool))
	fallbacks := make([]RelayState, 0)
	for _, state := range autoPool {
		if isRelayFallback(state) {
			fallbacks = append(fallbacks, state)
		} else {
			active = append(active, state)
		}
	}

	// Promote fallback relays to maintain the minimum active-pool size.
	if len(active) < molsMinActiveNodes && len(fallbacks) > 0 {
		promote := min(molsMinActiveNodes-len(active), len(fallbacks))
		active = append(active, fallbacks[:promote]...)
		fallbacks = fallbacks[promote:]
	}

	// scoreFor returns the MOLS score for a relay state under the current grid.
	scoreFor := func(state RelayState) int {
		candidateIdx := hashToGF64(state.Descriptor.APIHTTPSAddr)
		if congested {
			return molsCongestionScore(ingressIdx, candidateIdx, m1, m2)
		}
		return molsScore(ingressIdx, candidateIdx, m1, m2)
	}

	type scoredURL struct {
		url   string
		score int
	}
	rank := func(pool []RelayState) []scoredURL {
		out := make([]scoredURL, len(pool))
		for i, state := range pool {
			out[i] = scoredURL{url: state.Descriptor.APIHTTPSAddr, score: scoreFor(state)}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].score != out[j].score {
				return out[i].score > out[j].score // descending: highest score first
			}
			return out[i].url < out[j].url // deterministic tie-break
		})
		return out
	}

	activeSorted := rank(active)
	fallbackSorted := rank(fallbacks)

	autoURLs := make([]string, 0, len(activeSorted)+len(fallbackSorted))
	for _, s := range activeSorted {
		autoURLs = append(autoURLs, s.url)
	}
	for _, s := range fallbackSorted {
		autoURLs = append(autoURLs, s.url)
	}
	if clientState.MaxActiveRelays > 0 && len(autoURLs) > clientState.MaxActiveRelays {
		autoURLs = autoURLs[:clientState.MaxActiveRelays]
	}

	out := make([]string, 0, len(explicit)+len(autoURLs))
	out = append(out, explicit...)
	out = append(out, autoURLs...)
	if len(out) == 0 {
		return nil
	}
	return out
}
