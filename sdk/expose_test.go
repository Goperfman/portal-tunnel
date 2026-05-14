package sdk

import (
	"net/url"
	"testing"

	"github.com/gosuda/portal-tunnel/v2/portal/discovery"
	"github.com/gosuda/portal-tunnel/v2/types"
)

func mustRelaySet(t *testing.T, relayURLs ...string) *discovery.RelaySet {
	t.Helper()
	return discovery.NewRelaySet(relayURLs)
}

func TestExposureConfigSnapshotsDoNotShareMutableState(t *testing.T) {
	exposure := &Exposure{
		cfg: ExposeConfig{
			RelayURLs: []string{"https://relay-a.example"},
			Identity: types.Identity{
				Name:    "svc",
				Address: "portal-address",
			},
			Metadata: types.LeaseMetadata{
				Tags: []string{"initial"},
			},
		},
	}

	snapshot := exposure.Config()
	snapshot.RelayURLs[0] = "https://mutated.example"
	snapshot.Metadata.Tags[0] = "mutated"

	next := exposure.Config()
	if got := next.RelayURLs[0]; got != "https://relay-a.example" {
		t.Fatalf("RelayURLs[0] = %q, want original relay", got)
	}
	if got := next.Metadata.Tags[0]; got != "initial" {
		t.Fatalf("Metadata.Tags[0] = %q, want original tag", got)
	}

	if err := exposure.updateCfg(func(cfg *ExposeConfig) error {
		cfg.MaxActiveRelays = 2
		cfg.Metadata = types.LeaseMetadata{Tags: []string{"updated"}}
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	metadata := exposure.metadata()
	metadata.Tags[0] = "mutated"
	if got := exposure.metadata().Tags[0]; got != "updated" {
		t.Fatalf("MetadataSnapshot().Tags[0] = %q, want updated", got)
	}
	if got := exposure.Config().MaxActiveRelays; got != 2 {
		t.Fatalf("MaxActiveRelays = %d, want 2", got)
	}
}

func TestExposureReconcileRemovesBannedRelayFromActiveSet(t *testing.T) {
	const (
		relayA = "https://relay-a.example"
		relayB = "https://relay-b.example"
	)

	relayURL, err := url.Parse(relayA)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	relayBURL, err := url.Parse(relayB)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	exposure := &Exposure{
		cfg:            ExposeConfig{RelayURLs: []string{relayA, relayB}},
		relaySet:       mustRelaySet(t, relayA, relayB),
		relayListeners: make(map[string]*listener, 2),
	}
	relayAClosed := make(chan struct{})
	exposure.relayListeners = map[string]*listener{
		relayA: {
			relayURL: relayURL,
			cancel:   func() { close(relayAClosed) },
			doneCh:   relayAClosed,
		},
		relayB: {
			relayURL: relayBURL,
		},
	}

	exposure.relaySet.BanRelayURL(relayA)
	if err := exposure.reconcileRelayListeners(false); err != nil {
		t.Fatalf("reconcileRelayListeners() error = %v", err)
	}

	select {
	case <-relayAClosed:
	default:
		t.Fatal("banned relay listener was not closed")
	}

	if got := exposure.ActiveRelayURLs(); len(got) != 1 || got[0] != relayB {
		t.Fatalf("ActiveRelayURLs() = %v, want [%q]", got, relayB)
	}

	exposure.mu.RLock()
	_, listenerExists := exposure.relayListeners[relayA]
	exposure.mu.RUnlock()
	if listenerExists {
		t.Fatal("banned relay listener still exists in exposure.listeners")
	}
}

func TestExposureReconcileRemovesStaleListener(t *testing.T) {
	const (
		relayA = "https://relay-a.example"
		relayB = "https://relay-b.example"
	)

	relayAURL, err := url.Parse(relayA)
	if err != nil {
		t.Fatalf("url.Parse(relayA) error = %v", err)
	}
	relayBURL, err := url.Parse(relayB)
	if err != nil {
		t.Fatalf("url.Parse(relayB) error = %v", err)
	}

	relayAClosed := make(chan struct{})
	exposure := &Exposure{
		cfg:            ExposeConfig{RelayURLs: []string{relayA, relayB}},
		relaySet:       mustRelaySet(t, relayA, relayB),
		relayListeners: make(map[string]*listener, 2),
	}
	exposure.relayListeners = map[string]*listener{
		relayA: {
			relayURL: relayAURL,
			cancel:   func() { close(relayAClosed) },
			doneCh:   relayAClosed,
		},
		relayB: {
			relayURL: relayBURL,
		},
	}

	exposure.relaySet.SetBootstrapRelayURLs([]string{relayB})
	if err := exposure.reconcileRelayListeners(false); err != nil {
		t.Fatalf("reconcileRelayListeners() error = %v", err)
	}

	select {
	case <-relayAClosed:
	default:
		t.Fatal("stale relay listener was not closed")
	}

	knownRelayURLs := exposure.ActiveRelayURLs()
	exposure.mu.RLock()
	_, relayAExists := exposure.relayListeners[relayA]
	_, relayBExists := exposure.relayListeners[relayB]
	exposure.mu.RUnlock()
	if len(knownRelayURLs) != 1 || knownRelayURLs[0] != relayB {
		t.Fatalf("knownRelayURLs = %v, want [%q]", knownRelayURLs, relayB)
	}
	if relayAExists {
		t.Fatal("stale relay listener still exists in exposure.listeners")
	}
	if !relayBExists {
		t.Fatal("active relay listener missing from exposure.listeners")
	}
}

func TestExposureRemoveRelayStopsRunningListener(t *testing.T) {
	const relayA = "https://relay-a.example"

	relayAURL, err := url.Parse(relayA)
	if err != nil {
		t.Fatalf("url.Parse(relayA) error = %v", err)
	}

	relayAClosed := make(chan struct{})
	exposure := &Exposure{
		cfg:            ExposeConfig{RelayURLs: []string{relayA}},
		relaySet:       mustRelaySet(t, relayA),
		relayListeners: make(map[string]*listener, 1),
	}
	exposure.relayListeners[relayA] = &listener{
		relayURL: relayAURL,
		cancel:   func() { close(relayAClosed) },
		doneCh:   relayAClosed,
	}

	if err := exposure.RemoveRelay(relayA); err != nil {
		t.Fatalf("RemoveRelay() error = %v", err)
	}

	select {
	case <-relayAClosed:
	default:
		t.Fatal("removed relay listener was not closed")
	}
	if got := exposure.ActiveRelayURLs(); len(got) != 0 {
		t.Fatalf("ActiveRelayURLs() = %v, want empty", got)
	}
	if got := exposure.Config().RelayURLs; len(got) != 0 {
		t.Fatalf("RelayURLs = %v, want empty", got)
	}
	if got := exposure.relaySet.PriorityRelays(discovery.ClientState{}); len(got) != 0 {
		t.Fatalf("PriorityRelays() = %v, want empty", got)
	}
	relays := exposure.relaySet.AllRelays()
	if len(relays) != 1 || relays[0].Descriptor.APIHTTPSAddr != relayA || relays[0].Banned {
		t.Fatalf("AllRelays() = %+v, want unbanned candidate %q", relays, relayA)
	}
}
