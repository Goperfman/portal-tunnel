package portal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gosuda/portal-tunnel/v2/portal/policy"
	"github.com/gosuda/portal-tunnel/v2/portal/transport"
	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

func newTestRegistry(t *testing.T) *leaseRegistry {
	t.Helper()
	relay, err := utils.LoadOrCreateRelayIdentity(t.TempDir(), "example.com", false)
	if err != nil {
		t.Fatalf("LoadOrCreateRelayIdentity() error = %v", err)
	}
	registry, err := newLeaseRegistry(false, false, 10000, 10100, relay.Name, 443, relay.PrivateKey, relay.PublicKey, relay.Address, "https://example.com", false, "")
	if err != nil {
		t.Fatalf("newLeaseRegistry() error = %v", err)
	}
	return registry
}

func newTestLeaseIdentity(t *testing.T, name string) types.Identity {
	t.Helper()
	identity, err := utils.ResolveSecp256k1Identity("")
	if err != nil {
		t.Fatalf("ResolveSecp256k1Identity() error = %v", err)
	}
	identity.Name = name
	return identity
}

func TestLeaseRegistryLifecycle(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t)
	runtime := registry.policy
	record, registered, err := registry.Register(types.RegisterChallengeRequest{
		Identity: newTestLeaseIdentity(t, "demo"),
	}, "203.0.113.10", "")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	lookedUp, ok := registry.Lookup("demo.example.com")
	if !ok || lookedUp != record {
		t.Fatalf("Lookup() = %v, %v, want registered lease", lookedUp, ok)
	}

	renewed, err := registry.Renew(types.RenewRequest{
		AccessToken: registered.AccessToken,
		TTL:         int(time.Minute / time.Second),
	}, "203.0.113.11")
	if err != nil {
		t.Fatalf("Renew() error = %v", err)
	}
	if record.ClientIP != "203.0.113.11" {
		t.Fatalf("Renew() client ip = %q, want %q", record.ClientIP, "203.0.113.11")
	}
	if !renewed.ExpiresAt.Equal(record.ExpiresAt) {
		t.Fatalf("Renew() expires at = %v, want %v", renewed.ExpiresAt, record.ExpiresAt)
	}
	if renewed.AccessToken == "" {
		t.Fatal("Renew() access token is empty")
	}
	if got := runtime.IPFilter().IdentityIP(record.Key()); got != "203.0.113.11" {
		t.Fatalf("Renew() did not register client IP for lease")
	}

	removed, err := registry.Unregister(types.UnregisterRequest{AccessToken: renewed.AccessToken})
	if err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}
	if removed != record {
		t.Fatalf("Unregister() record = %v, want original record", removed)
	}

	if _, ok := registry.Lookup("demo.example.com"); ok {
		t.Fatal("Lookup() after Unregister() = true, want false")
	}
	if got := runtime.IPFilter().IdentityIP(record.Key()); got != "" {
		t.Fatalf("Unregister() lease IP = %q, want empty", got)
	}
}

func TestLeaseRegistryAutomaticECHRouteFallsBackToPlainSNI(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t)
	routeHostname := "ech-auto-ech.example.com"
	publicHostname := "auto-ech.example.com"
	record, registered, err := registry.Register(types.RegisterChallengeRequest{
		Identity:             newTestLeaseIdentity(t, "auto-ech"),
		RouteHostname:        routeHostname,
		FallbackHostnameHash: utils.HostnameHash(publicHostname),
	}, "203.0.113.10", "")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if registered.Hostname != routeHostname {
		t.Fatalf("Register() hostname = %q, want route hostname %q", registered.Hostname, routeHostname)
	}
	if registered.Hostname == publicHostname {
		t.Fatalf("Register() hostname = public hostname = %q", registered.Hostname)
	}
	if lookedUp, ok := registry.Lookup(publicHostname); !ok || lookedUp != record {
		t.Fatalf("Lookup(public hostname) = %v, %v, want fallback lease", lookedUp, ok)
	}
	lookedUp, ok := registry.Lookup(registered.Hostname)
	if !ok || lookedUp != record {
		t.Fatalf("Lookup(route hostname) = %v, %v, want registered lease", lookedUp, ok)
	}
	leases := registry.PublicLeases(time.Now())
	publicHostnameFromLease := ""
	if len(leases) == 1 {
		publicHostnameFromLease = leases[0].Hostname
	}
	if len(leases) != 1 || publicHostnameFromLease != registered.Hostname {
		t.Fatalf("PublicLeases() hostname = len %d, host %q, want len 1, host %q", len(leases), publicHostnameFromLease, registered.Hostname)
	}

	adminLeases := registry.AdminLeases(time.Now())
	if len(adminLeases) != 1 {
		t.Fatalf("AdminLeases() length = %d, want 1", len(adminLeases))
	}
	if adminLeases[0].Hostname != registered.Hostname {
		t.Fatalf("AdminLeases()[0] hostname = %q, want %q", adminLeases[0].Hostname, registered.Hostname)
	}
}

func TestLeaseRegistryHopRouteCanExposeECHAndPlainSNIFallback(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t)
	owner := newTestLeaseIdentity(t, "multi-hop-owner")
	wgPrivate, err := utils.GenerateWireGuardPrivateKey()
	if err != nil {
		t.Fatalf("GenerateWireGuardPrivateKey() error = %v", err)
	}
	wgPublic, err := utils.WireGuardPublicKeyFromPrivate(wgPrivate)
	if err != nil {
		t.Fatalf("WireGuardPublicKeyFromPrivate() error = %v", err)
	}
	now := time.Now()
	baseRoute := types.HopRoute{
		OwnerPublicKey: owner.PublicKey,
		ForwardRelay: types.RelayDescriptor{
			APIHTTPSAddr:       "https://next.example.com",
			WireGuardPublicKey: wgPublic,
		},
		ForwardToken: "hpt_forward",
		FirstSeenAt:  now,
		ExpiresAt:    now.Add(time.Minute),
	}
	plainRoute := baseRoute
	plainRoute.MatchHostnameHash = utils.HostnameHash("demo.example.com")
	echRoute := baseRoute
	echRoute.RouteHostname = "ech-demo.example.com"
	echRoute.Metadata.Hide = true

	if _, err := registry.RegisterHopRoute(&plainRoute, now); err != nil {
		t.Fatalf("RegisterHopRoute(plain) error = %v", err)
	}
	if _, err := registry.RegisterHopRoute(&echRoute, now); err != nil {
		t.Fatalf("RegisterHopRoute(ech) error = %v", err)
	}
	if _, ok := registry.Lookup("demo.example.com"); !ok {
		t.Fatal("Lookup(plain route) = false, want true")
	}
	if _, ok := registry.Lookup(echRoute.RouteHostname); !ok {
		t.Fatal("Lookup(ech route) = false, want true")
	}
	leases := registry.PublicLeases(now)
	if len(leases) != 0 {
		t.Fatalf("PublicLeases() length = %d, want 0 for hostname-minimized hop routes", len(leases))
	}
}

func TestLeaseRegistryWildcardAndConflict(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t)
	wildcardLease := &leaseRecord{
		Identity:  newTestLeaseIdentity(t, "wildcard"),
		Hostname:  "*.example.com",
		ExpiresAt: time.Now().Add(30 * time.Second),
	}
	registry.records = append(registry.records, wildcardLease)

	if _, ok := registry.Lookup("app.example.com"); !ok {
		t.Fatal("Lookup(one-level wildcard) = false, want true")
	}
	if _, ok := registry.Lookup("deep.app.example.com"); ok {
		t.Fatal("Lookup(multi-level wildcard) = true, want false")
	}

	if _, _, err := registry.Register(types.RegisterChallengeRequest{
		Identity: newTestLeaseIdentity(t, "conflict"),
	}, "203.0.113.10", ""); err != nil {
		t.Fatalf("Register(conflict first) error = %v", err)
	}
	_, _, err := registry.Register(types.RegisterChallengeRequest{
		Identity: newTestLeaseIdentity(t, "conflict"),
	}, "203.0.113.11", "")
	if !errors.Is(err, errHostnameConflict) {
		t.Fatalf("Register(conflict second) error = %v, want hostname conflict", err)
	}
}

func TestLeaseRegistryAdminLeasesAndRoutableUsePolicy(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t)
	runtime := registry.policy
	if err := runtime.Approver().SetMode(policy.ModeManual); err != nil {
		t.Fatalf("SetMode() error = %v", err)
	}
	record, _, err := registry.Register(types.RegisterChallengeRequest{
		Identity: newTestLeaseIdentity(t, "demo"),
	}, "203.0.113.20", "")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if registry.policy.IsIdentityRoutable(record.Key()) {
		t.Fatal("policy.IsIdentityRoutable() = true, want false before approval")
	}

	leases := registry.AdminLeases(time.Now())
	if len(leases) != 1 {
		t.Fatalf("AdminLeases() length = %d, want 1", len(leases))
	}
	if leases[0].IsApproved {
		t.Fatal("AdminLeases()[0].IsApproved = true, want false before approval")
	}
	if got := runtime.IPFilter().IdentityIP(record.Key()); got != "203.0.113.20" {
		t.Fatalf("Register() lease IP = %q, want %q", got, "203.0.113.20")
	}

	runtime.Approver().Approve(record.Key())
	if !registry.policy.IsIdentityRoutable(record.Key()) {
		t.Fatal("policy.IsIdentityRoutable() = false, want true after approval")
	}

	leases = registry.AdminLeases(time.Now())
	if len(leases) != 1 {
		t.Fatalf("AdminLeases() length = %d, want 1", len(leases))
	}
	if !leases[0].IsApproved {
		t.Fatal("AdminLeases()[0].IsApproved = false, want true after approval")
	}
}

func TestLeaseRegistryPublicLeasesIncludesIngressRouteInManualApproval(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t)
	if err := registry.policy.Approver().SetMode(policy.ModeManual); err != nil {
		t.Fatalf("SetMode() error = %v", err)
	}
	route := &leaseRecord{
		Identity:  newTestLeaseIdentity(t, "demo"),
		Hostname:  "demo.example.com",
		ExpiresAt: time.Now().Add(30 * time.Second),
	}
	registry.records = append(registry.records, route)

	leases := registry.PublicLeases(time.Now())
	if len(leases) != 1 {
		t.Fatalf("PublicLeases() length = %d, want 1", len(leases))
	}
	if leases[0].Hostname != route.Hostname {
		t.Fatalf("PublicLeases()[0].Hostname = %q, want %q", leases[0].Hostname, route.Hostname)
	}
}

func TestLeaseRegistryCleanupExpiredClosesBroker(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t)
	record := &leaseRecord{
		Identity:  newTestLeaseIdentity(t, "expired"),
		Hostname:  "expired.example.com",
		ExpiresAt: time.Now().Add(-time.Second),
		stream:    transport.NewRelayStream("addr-expired", time.Minute, 1),
	}
	registry.records = append(registry.records, record)

	registry.cleanupExpired(time.Now())

	if _, ok := registry.Lookup("expired.example.com"); ok {
		t.Fatal("Lookup() after cleanupExpired() = true, want false")
	}
	if _, err := record.stream.Claim(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Claim() after cleanupExpired() error = %v, want %v", err, net.ErrClosed)
	}
}

func TestIssueRegisterChallengeBoundsPendingPerIP(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t)
	clientIP := "203.0.113.50"
	for i := 0; i < defaultRegisterChallengeOutstandingPerIP; i++ {
		_, err := registry.issueRegisterChallenge(types.RegisterChallengeRequest{
			Identity: newTestLeaseIdentity(t, fmt.Sprintf("demo-%d", i)),
		}, "example.com", "https://example.com/sdk/register", clientIP)
		if err != nil {
			t.Fatalf("issueRegisterChallenge(%d) error = %v", i, err)
		}
	}

	_, err := registry.issueRegisterChallenge(types.RegisterChallengeRequest{
		Identity: newTestLeaseIdentity(t, "overflow"),
	}, "example.com", "https://example.com/sdk/register", clientIP)
	if !errors.Is(err, errRegisterChallengePending) {
		t.Fatalf("issueRegisterChallenge() error = %v, want pending limit", err)
	}

	expiredAt := time.Now().Add(-time.Second)
	registry.mu.Lock()
	for _, record := range registry.records {
		if record == nil || record.registerChallenge == nil {
			continue
		}
		record.ExpiresAt = expiredAt
		record.registerChallenge.ExpiresAt = expiredAt
	}
	registry.mu.Unlock()

	_, err = registry.issueRegisterChallenge(types.RegisterChallengeRequest{
		Identity: newTestLeaseIdentity(t, "after-cleanup"),
	}, "example.com", "https://example.com/sdk/register", clientIP)
	if err != nil {
		t.Fatalf("issueRegisterChallenge() after expired cleanup error = %v", err)
	}
}

func TestServerRunRegistryJanitorRejectsNonPositiveInterval(t *testing.T) {
	t.Parallel()

	server := &Server{registry: newTestRegistry(t)}
	err := server.runRegistryJanitor(context.Background(), 0)
	if err == nil {
		t.Fatal("runRegistryJanitor() error = nil, want validation error")
	}
}
