package discovery

import (
	"errors"
	"testing"
	"time"

	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

func mustSigningIdentity(t *testing.T) types.Identity {
	t.Helper()
	identity, err := utils.ResolveSecp256k1Identity("")
	if err != nil {
		t.Fatalf("ResolveSecp256k1Identity() error = %v", err)
	}
	return identity
}

func mustNormalizedDescriptor(t *testing.T, signing types.Identity, relayName, relayURL string) types.RelayDescriptor {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	desc, err := utils.NormalizeDescriptor(types.RelayDescriptor{
		Identity: types.Identity{
			Name:    relayName,
			Address: signing.Address,
		},
		RelayID:      relayURL,
		Version:      1,
		IssuedAt:     now,
		ExpiresAt:    now.Add(time.Hour),
		APIHTTPSAddr: relayURL,
		Discovery:    true,
	})
	if err != nil {
		t.Fatalf("NormalizeDescriptor() error = %v", err)
	}
	return desc
}

func TestSignDescriptorRoundtrip(t *testing.T) {
	signing := mustSigningIdentity(t)
	desc := mustNormalizedDescriptor(t, signing, "relay-rt", "https://relay-rt.example")
	signed, err := SignDescriptor(desc, signing.PrivateKey)
	if err != nil {
		t.Fatalf("SignDescriptor() error = %v", err)
	}
	if signed.Signature == "" {
		t.Fatal("signed descriptor must have non-empty Signature")
	}
	pubKey, err := VerifyDescriptor(signed)
	if err != nil {
		t.Fatalf("VerifyDescriptor() error = %v", err)
	}
	if pubKey == "" {
		t.Fatal("VerifyDescriptor must return recovered public key")
	}
}

func TestVerifyDescriptorRejectsUnsigned(t *testing.T) {
	signing := mustSigningIdentity(t)
	desc := mustNormalizedDescriptor(t, signing, "relay-unsigned", "https://relay-unsigned.example")
	if _, err := VerifyDescriptor(desc); !errors.Is(err, ErrDescriptorUnsigned) {
		t.Fatalf("VerifyDescriptor() err = %v, want ErrDescriptorUnsigned", err)
	}
}

func TestVerifyDescriptorRejectsTamperedSignedField(t *testing.T) {
	signing := mustSigningIdentity(t)
	desc := mustNormalizedDescriptor(t, signing, "relay-tamper", "https://relay-tamper.example")
	signed, err := SignDescriptor(desc, signing.PrivateKey)
	if err != nil {
		t.Fatalf("SignDescriptor() error = %v", err)
	}
	tampered := signed
	tampered.WireGuardEndpoint = "evil.example:51820"
	if _, err := VerifyDescriptor(tampered); err == nil {
		t.Fatal("VerifyDescriptor must reject tampered signed field")
	}
}

func TestVerifyDescriptorAcceptsTelemetryUpdate(t *testing.T) {
	signing := mustSigningIdentity(t)
	desc := mustNormalizedDescriptor(t, signing, "relay-telemetry", "https://relay-telemetry.example")
	signed, err := SignDescriptor(desc, signing.PrivateKey)
	if err != nil {
		t.Fatalf("SignDescriptor() error = %v", err)
	}
	updated := signed
	updated.Load = 42
	updated.LoadScore = 99
	updated.LastUpdated = time.Now().UnixMilli()
	if _, err := VerifyDescriptor(updated); err != nil {
		t.Fatalf("VerifyDescriptor() must ignore telemetry, got err = %v", err)
	}
}

func TestVerifyDescriptorRejectsAddressMismatch(t *testing.T) {
	signing := mustSigningIdentity(t)
	other := mustSigningIdentity(t)
	desc := mustNormalizedDescriptor(t, signing, "relay-mismatch", "https://relay-mismatch.example")
	// Sign with `signing` but rewrite Address to a different identity.
	signed, err := SignDescriptor(desc, signing.PrivateKey)
	if err != nil {
		t.Fatalf("SignDescriptor() error = %v", err)
	}
	signed.Address = other.Address
	if _, err := VerifyDescriptor(signed); err == nil {
		t.Fatal("VerifyDescriptor must reject signing-key/address mismatch")
	}
}

func TestCanonicalBytesDeterministic(t *testing.T) {
	signing := mustSigningIdentity(t)
	desc := mustNormalizedDescriptor(t, signing, "relay-det", "https://relay-det.example")
	first, err := types.CanonicalBytes(desc)
	if err != nil {
		t.Fatalf("CanonicalBytes() error = %v", err)
	}
	for i := range 16 {
		out, err := types.CanonicalBytes(desc)
		if err != nil {
			t.Fatalf("CanonicalBytes() error = %v", err)
		}
		if string(out) != string(first) {
			t.Fatalf("CanonicalBytes is not deterministic: iteration %d differs", i)
		}
	}
}

func TestApplyRelayDiscoveryResponseRejectsUnsignedDescriptor(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	desc := mustNormalizedDescriptor(t, signing, "relay-strict", "https://relay-strict.example")
	// Note: NOT signed.
	_, err = set.ApplyRelayDiscoveryResponse("", types.DiscoveryResponse{
		ProtocolVersion: types.DiscoveryVersion,
		Relays:          []types.RelayDescriptor{desc},
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("ApplyRelayDiscoveryResponse() error = %v", err)
	}
	if got := set.AggregateRelays(); len(got) != 0 {
		t.Fatalf("expected unsigned descriptor to be dropped, got %d relays", len(got))
	}
}

func TestApplyRelayDiscoveryResponseRejectsRollback(t *testing.T) {
	set, err := NewRelaySet(nil)
	if err != nil {
		t.Fatalf("NewRelaySet() error = %v", err)
	}
	signing := mustSigningIdentity(t)
	relayURL := "https://relay-rollback.example"

	now := time.Now().UTC().Truncate(time.Microsecond)
	build := func(issuedAt time.Time) types.RelayDescriptor {
		desc, err := utils.NormalizeDescriptor(types.RelayDescriptor{
			Identity: types.Identity{
				Name:    "relay-rollback",
				Address: signing.Address,
			},
			RelayID:      relayURL,
			Version:      1,
			IssuedAt:     issuedAt,
			ExpiresAt:    issuedAt.Add(time.Hour),
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

	newer := build(now)
	if _, err := set.ApplyRelayDiscoveryResponse("", types.DiscoveryResponse{
		ProtocolVersion: types.DiscoveryVersion,
		Relays:          []types.RelayDescriptor{newer},
	}, now); err != nil {
		t.Fatalf("apply newer error = %v", err)
	}

	older := build(now.Add(-time.Minute))
	changed, err := set.ApplyRelayDiscoveryResponse("", types.DiscoveryResponse{
		ProtocolVersion: types.DiscoveryVersion,
		Relays:          []types.RelayDescriptor{older},
	}, now)
	if err != nil {
		t.Fatalf("apply older error = %v", err)
	}
	if changed {
		t.Fatal("expected rollback descriptor to leave relay set unchanged")
	}
	states := set.AggregateRelays()
	if len(states) != 1 {
		t.Fatalf("len(AggregateRelays()) = %d, want 1", len(states))
	}
	if !states[0].Descriptor.IssuedAt.Equal(newer.IssuedAt) {
		t.Fatalf("retained descriptor IssuedAt = %v, want %v", states[0].Descriptor.IssuedAt, newer.IssuedAt)
	}
}
