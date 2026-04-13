package discovery

import (
	"errors"
	"time"

	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

const (
	DiscoveryDescriptorTTL       = 5 * time.Minute
	DiscoveryHintRetentionTTL    = 30 * 24 * time.Hour
	defaultDirectRecoveryBackoff = 1 * time.Minute
	maxDirectRecoveryBackoff     = 5 * time.Minute

	// MaxAnnouncedRelays is the hard ceiling on the number of relay entries
	// the local set will retain. When exceeded, eviction prefers the oldest
	// non-bootstrap, non-confirmed entries by LastSeenAt. Bootstrap and
	// listener-confirmed entries are pinned and never evicted by capacity.
	MaxAnnouncedRelays = 1024

	// AnnounceClockSkewTolerance bounds how far in the future a descriptor's
	// IssuedAt may sit relative to local time. Anything beyond this is
	// rejected as clock-skewed or maliciously post-dated.
	AnnounceClockSkewTolerance = 5 * time.Minute

	// AnnounceMaxValidity bounds the maximum (ExpiresAt - IssuedAt) window
	// for an accepted announce. Honest relays sign with the discovery TTL,
	// so a 24h cap leaves ample headroom while preventing attackers from
	// minting year-long descriptors.
	AnnounceMaxValidity = 24 * time.Hour
)

type RelayState struct {
	Descriptor types.RelayDescriptor
	Bootstrap  bool
	Confirmed  bool
	Banned     bool
	LastSeenAt time.Time

	DiscoveryRTT   time.Duration
	DiscoveryRTTAt time.Time

	consecutiveFailures int
	nextDirectRefreshAt time.Time
}

type ClientState struct {
	ActiveRelayURLs   []string
	ExplicitRelayURLs []string
	MaxActiveRelays   int
	RequireUDP        bool
	RequireTCP        bool
}

func newRelayState(desc types.RelayDescriptor, seenAt time.Time) (RelayState, error) {
	state := RelayState{
		Descriptor: desc,
	}
	if seenAt.IsZero() {
		return state, nil
	}

	seenAt = seenAt.UTC()
	normalized, err := utils.NormalizeDescriptor(desc)
	if err != nil {
		return RelayState{}, err
	}
	if normalized.ExpiresAt.Before(seenAt) {
		return RelayState{}, errors.New("descriptor expired")
	}

	state.Descriptor = normalized
	state.LastSeenAt = seenAt
	return state, nil
}

func newRelayStateFromURL(relayURL string) RelayState {
	return RelayState{
		Descriptor: types.RelayDescriptor{
			Identity: types.Identity{
				Name: utils.PortalRootHost(relayURL),
			},
			RelayID:      relayURL,
			APIHTTPSAddr: relayURL,
		},
	}
}

func (state RelayState) hasDescriptor() bool {
	return !state.LastSeenAt.IsZero()
}
