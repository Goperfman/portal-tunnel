package main

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gosuda/portal-tunnel/v2/portal"
	"github.com/gosuda/portal-tunnel/v2/portal/auth"
	"github.com/gosuda/portal-tunnel/v2/portal/identity"
	"github.com/gosuda/portal-tunnel/v2/portal/policy"
	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	adminBodyLimit = 1 << 16
)

func loadAdminState(path string, server *portal.Server) (persistedAdminState, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return persistedAdminState{}, nil
	}

	var payload persistedAdminState
	if _, err := utils.ReadJSONFileIfExists(path, &payload); err != nil {
		return persistedAdminState{}, err
	}
	if err := payload.apply(server); err != nil {
		return persistedAdminState{}, err
	}
	return payload, nil
}

func (api *RelayAPI) serveAdmin(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(strings.TrimSpace(r.URL.Path), "/")
	if path == "" {
		path = types.PathRoot
	}

	switch path {
	case types.PathAdmin:
		http.NotFound(w, r)
		return
	case types.PathAdminAuthChallenge:
		if !utils.RequireMethod(w, r, http.MethodPost) {
			return
		}
		api.handleWalletChallenge(w, r)
		return
	case types.PathAdminAuthLogin:
		if !utils.RequireMethod(w, r, http.MethodPost) {
			return
		}
		api.handleWalletLogin(w, r)
		return
	case types.PathAdminLogout:
		if !utils.RequireMethod(w, r, http.MethodPost) {
			return
		}
		api.auth.DeleteSession(adminAccessToken(r))
		utils.WriteAPIData(w, http.StatusOK, map[string]any{})
		return
	case types.PathAdminAuthStatus:
		if !utils.RequireMethod(w, r, http.MethodGet) {
			return
		}
		walletAddress, authenticated := api.authenticatedWallet(r)
		utils.WriteAPIData(w, http.StatusOK, types.WalletAuthStatusResponse{
			Authenticated: authenticated,
			WalletAddress: walletAddress,
		})
		return
	}

	if _, ok := api.authenticatedWallet(r); !ok {
		utils.WriteAPIError(w, http.StatusUnauthorized, types.APIErrorCodeUnauthorized, "unauthorized")
		return
	}

	runtime := api.server.PolicyRuntime()
	invalidRequestBody := utils.InvalidRequestError(errors.New("invalid request body"))

	switch path {
	case "/admin/metrics":
		promhttp.Handler().ServeHTTP(w, r)
		return
	case types.PathAdminState:
		if !utils.RequireMethod(w, r, http.MethodGet) {
			return
		}
		leases := api.server.AdminLeases()
		api.attachAutomaticAdminThumbnails(leases)
		utils.WriteAPIData(w, http.StatusOK, types.AdminStateResponse{
			Settings: api.adminSettings(runtime),
			Leases:   leases,
		})
	case types.PathAdminSettings:
		if !utils.RequireMethod(w, r, http.MethodPost) {
			return
		}
		req, ok := utils.DecodeJSONRequestAs[types.AdminSettings](w, r, adminBodyLimit, invalidRequestBody)
		if !ok {
			return
		}
		if !api.applyAdminSettings(w, runtime, req) {
			return
		}
		utils.WriteAPIData(w, http.StatusOK, api.adminSettings(runtime))
	case types.PathAdminLeasePolicy:
		if !utils.RequireMethod(w, r, http.MethodPost) {
			return
		}
		req, ok := utils.DecodeJSONRequestAs[types.AdminLeasePolicy](w, r, adminBodyLimit, invalidRequestBody)
		if !ok {
			return
		}
		identityKey, ok := normalizeAdminIdentityKey(w, req.IdentityKey)
		if !ok {
			return
		}
		if !applyAdminLeasePolicy(w, runtime, identityKey, req) {
			return
		}
		saveAdminState(api.adminSettingsPath, runtime, api.landingPageEnabled.Load())
		utils.WriteAPIData(w, http.StatusOK, map[string]any{})
	case types.PathAdminIPPolicy:
		if !utils.RequireMethod(w, r, http.MethodPost) {
			return
		}
		req, ok := utils.DecodeJSONRequestAs[types.AdminIPPolicy](w, r, adminBodyLimit, invalidRequestBody)
		if !ok {
			return
		}
		ip := strings.TrimSpace(req.IP)
		if net.ParseIP(ip) == nil {
			utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidIP, "invalid IP address")
			return
		}
		if req.IsBanned {
			runtime.IPFilter().BanIP(ip)
		} else {
			runtime.IPFilter().UnbanIP(ip)
		}
		saveAdminState(api.adminSettingsPath, runtime, api.landingPageEnabled.Load())
		utils.WriteAPIData(w, http.StatusOK, map[string]any{})
	default:
		http.NotFound(w, r)
	}
}

func (api *RelayAPI) adminSettings(runtime *policy.Runtime) types.AdminSettings {
	return types.AdminSettings{
		ApprovalMode:       string(runtime.Approver().Mode()),
		LandingPageEnabled: api.landingPageEnabled.Load(),
		UDP: types.AdminPortSettings{
			Enabled:   runtime.IsUDPEnabled(),
			MaxLeases: runtime.UDPMaxLeases(),
		},
		TCPPort: types.AdminPortSettings{
			Enabled:   runtime.IsTCPPortEnabled(),
			MaxLeases: runtime.TCPPortMaxLeases(),
		},
	}
}

func (api *RelayAPI) applyAdminSettings(w http.ResponseWriter, runtime *policy.Runtime, req types.AdminSettings) bool {
	if req.UDP.MaxLeases < 0 || req.TCPPort.MaxLeases < 0 {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "max_leases must be non-negative")
		return false
	}
	if err := runtime.Approver().SetMode(policy.Mode(strings.TrimSpace(req.ApprovalMode))); err != nil {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidMode, "approval_mode must be 'auto' or 'manual'")
		return false
	}
	api.landingPageEnabled.Store(req.LandingPageEnabled)
	api.server.SetUDPPolicy(req.UDP.Enabled, req.UDP.MaxLeases)
	api.server.SetTCPPortPolicy(req.TCPPort.Enabled, req.TCPPort.MaxLeases)
	saveAdminState(api.adminSettingsPath, runtime, api.landingPageEnabled.Load())
	return true
}

func normalizeAdminIdentityKey(w http.ResponseWriter, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	name, address, ok := strings.Cut(raw, types.IdentityKeySeparator)
	if !ok {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "invalid identity")
		return "", false
	}
	normalizedIdentity, err := identity.NormalizeIdentity(types.Identity{Name: name, Address: address})
	if err != nil {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "invalid identity")
		return "", false
	}
	return normalizedIdentity.Key(), true
}

func applyAdminLeasePolicy(w http.ResponseWriter, runtime *policy.Runtime, identityKey string, req types.AdminLeasePolicy) bool {
	if req.IsBanned == nil && req.IsApproved == nil && req.IsDenied == nil && req.BPS == nil {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "lease policy update is empty")
		return false
	}
	if req.IsApproved != nil && req.IsDenied != nil && *req.IsApproved && *req.IsDenied {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "lease cannot be approved and denied")
		return false
	}
	if req.BPS != nil {
		if *req.BPS < 0 {
			utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "bps must be non-negative")
			return false
		}
		if *req.BPS == 0 {
			runtime.BPSManager().DeleteIdentityBPS(identityKey)
		} else {
			runtime.BPSManager().SetIdentityBPS(identityKey, *req.BPS)
		}
	}
	if req.IsBanned != nil {
		if *req.IsBanned {
			runtime.BanIdentity(identityKey)
		} else {
			runtime.UnbanIdentity(identityKey)
		}
	}
	approver := runtime.Approver()
	if req.IsDenied != nil {
		if *req.IsDenied {
			approver.Deny(identityKey)
			approver.Revoke(identityKey)
		} else {
			approver.Undeny(identityKey)
		}
	}
	if req.IsApproved != nil {
		if *req.IsApproved {
			approver.Approve(identityKey)
			approver.Undeny(identityKey)
		} else {
			approver.Revoke(identityKey)
		}
	}
	return true
}

func (api *RelayAPI) handleWalletChallenge(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONRequestAs[types.WalletAuthChallengeRequest](w, r, adminBodyLimit, utils.InvalidRequestError(errors.New("invalid request body")))
	if !ok {
		return
	}
	resp, err := api.auth.IssueChallenge(req, adminAuthDomain(r, api.server.RelayIdentity().Name), adminAuthURI(r, types.PathAdminAuthLogin), time.Now().UTC())
	if err != nil {
		writeWalletAuthError(w, err)
		return
	}
	utils.WriteAPIData(w, http.StatusCreated, resp)
}

func (api *RelayAPI) handleWalletLogin(w http.ResponseWriter, r *http.Request) {
	req, ok := utils.DecodeJSONRequestAs[types.WalletAuthLoginRequest](w, r, adminBodyLimit, utils.InvalidRequestError(errors.New("invalid request body")))
	if !ok {
		return
	}
	token, walletAddress, err := api.auth.Login(req, time.Now().UTC())
	if err != nil {
		writeWalletAuthError(w, err)
		return
	}

	utils.WriteAPIData(w, http.StatusOK, types.WalletAuthLoginResponse{
		AccessToken:   token,
		WalletAddress: walletAddress,
	})
}

func (api *RelayAPI) authenticatedWallet(r *http.Request) (string, bool) {
	return api.auth.ValidateSession(adminAccessToken(r))
}

func adminAuthDomain(r *http.Request, fallback string) string {
	domain := strings.TrimSpace(r.Host)
	if domain != "" {
		return domain
	}
	return strings.TrimSpace(fallback)
}

func adminAuthURI(r *http.Request, endpointPath string) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return (&url.URL{
		Scheme: scheme,
		Host:   adminAuthDomain(r, "localhost"),
		Path:   endpointPath,
	}).String()
}

func adminAccessToken(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func writeWalletAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrWalletAuthUnauthorized):
		utils.WriteAPIError(w, http.StatusForbidden, types.APIErrorCodeUnauthorized, err.Error())
	case errors.Is(err, auth.ErrWalletAuthChallengeNotFound), errors.Is(err, auth.ErrWalletAuthChallengeExpired), errors.Is(err, auth.ErrWalletAuthInvalidSignature):
		utils.WriteAPIError(w, http.StatusUnauthorized, types.APIErrorCodeUnauthorized, err.Error())
	default:
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, err.Error())
	}
}

func saveAdminState(path string, runtime *policy.Runtime, landingPageEnabled bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}

	approver := runtime.Approver()
	udpEnabled := runtime.IsUDPEnabled()
	udpMaxLeases := runtime.UDPMaxLeases()
	tcpPortEnabled := runtime.IsTCPPortEnabled()
	tcpPortMaxLeases := runtime.TCPPortMaxLeases()
	payload := persistedAdminState{
		ApprovalMode:         string(approver.Mode()),
		ApprovedIdentityKeys: approver.ApprovedKeys(),
		DeniedIdentityKeys:   approver.DeniedKeys(),
		BannedIdentityKeys:   runtime.BannedIdentityKeys(),
		BannedIPs:            runtime.IPFilter().BannedIPs(),
		IdentityBPS:          runtime.BPSManager().IdentityBPSLimits(),
		UDPEnabled:           &udpEnabled,
		UDPMaxLeases:         &udpMaxLeases,
		TCPPortEnabled:       &tcpPortEnabled,
		TCPPortMaxLeases:     &tcpPortMaxLeases,
		LandingPageEnabled:   &landingPageEnabled,
	}
	_ = utils.WriteJSONFile(path, payload, 0o600)
}

type persistedAdminState struct {
	ApprovalMode         string           `json:"approval_mode"`
	ApprovedIdentityKeys []string         `json:"approved_identity_keys,omitempty"`
	DeniedIdentityKeys   []string         `json:"denied_identity_keys,omitempty"`
	BannedIdentityKeys   []string         `json:"banned_identity_keys,omitempty"`
	BannedIPs            []string         `json:"banned_ips,omitempty"`
	IdentityBPS          map[string]int64 `json:"identity_bps,omitempty"`
	UDPEnabled           *bool            `json:"udp_enabled,omitempty"`
	UDPMaxLeases         *int             `json:"udp_max_leases,omitempty"`
	TCPPortEnabled       *bool            `json:"tcp_port_enabled,omitempty"`
	TCPPortMaxLeases     *int             `json:"tcp_port_max_leases,omitempty"`
	LandingPageEnabled   *bool            `json:"landing_page_enabled,omitempty"`
}

func applyOptionalPolicy(enabled *bool, maxLeases *int, getEnabled func() bool, getMax func() int, set func(bool, int)) {
	if enabled == nil && maxLeases == nil {
		return
	}
	e := getEnabled()
	m := getMax()
	if enabled != nil {
		e = *enabled
	}
	if maxLeases != nil {
		m = *maxLeases
	}
	set(e, m)
}

func (s persistedAdminState) apply(server *portal.Server) error {
	if server == nil {
		return nil
	}
	runtime := server.PolicyRuntime()
	if runtime == nil {
		return nil
	}
	if mode := strings.TrimSpace(s.ApprovalMode); mode != "" {
		if err := runtime.Approver().SetMode(policy.Mode(mode)); err != nil {
			return err
		}
	}
	runtime.Approver().SetDecisions(
		identity.NormalizeIdentityKeys(s.ApprovedIdentityKeys),
		identity.NormalizeIdentityKeys(s.DeniedIdentityKeys),
	)
	runtime.SetBannedIdentityKeys(identity.NormalizeIdentityKeys(s.BannedIdentityKeys))
	runtime.IPFilter().SetBannedIPs(s.BannedIPs)
	runtime.BPSManager().SetIdentityBPSLimits(identity.NormalizeIdentityKeyBPS(s.IdentityBPS))
	applyOptionalPolicy(s.UDPEnabled, s.UDPMaxLeases, runtime.IsUDPEnabled, runtime.UDPMaxLeases, server.SetUDPPolicy)
	applyOptionalPolicy(s.TCPPortEnabled, s.TCPPortMaxLeases, runtime.IsTCPPortEnabled, runtime.TCPPortMaxLeases, server.SetTCPPortPolicy)
	return nil
}
