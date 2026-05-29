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
	methodNotAllowed := utils.MethodNotAllowedError()
	invalidRequestBody := utils.InvalidRequestError(errors.New("invalid request body"))

	switch path {
	case "/admin/metrics":
		promhttp.Handler().ServeHTTP(w, r)
		return
	case types.PathAdminSnapshot:
		if !utils.RequireMethod(w, r, http.MethodGet) {
			return
		}
		leases := api.server.AdminLeases()
		api.attachAutomaticAdminThumbnails(leases)
		utils.WriteAPIData(w, http.StatusOK, types.AdminSnapshotResponse{
			ApprovalMode:       string(runtime.Approver().Mode()),
			LandingPageEnabled: api.landingPageEnabled.Load(),
			Leases:             leases,
			UDP: types.AdminUDPSettingsResponse{
				Enabled:   runtime.IsUDPEnabled(),
				MaxLeases: runtime.UDPMaxLeases(),
			},
			TCPPort: types.AdminTCPPortSettingsResponse{
				Enabled:   runtime.IsTCPPortEnabled(),
				MaxLeases: runtime.TCPPortMaxLeases(),
			},
		})
	case types.PathAdminLandingPage:
		if !utils.RequireMethod(w, r, http.MethodPost) {
			return
		}
		req, ok := utils.DecodeJSONRequestAs[types.AdminLandingPageSettingsRequest](w, r, adminBodyLimit, invalidRequestBody)
		if !ok {
			return
		}
		api.landingPageEnabled.Store(req.Enabled)
		landingPageEnabled := api.landingPageEnabled.Load()
		saveAdminState(api.adminSettingsPath, runtime, landingPageEnabled)
		utils.WriteAPIData(w, http.StatusOK, types.AdminLandingPageSettingsResponse{
			Enabled: landingPageEnabled,
		})
	case types.PathAdminUDP:
		api.handlePortSettings(w, r, invalidRequestBody, runtime,
			api.server.SetUDPPolicy,
			func() any {
				return types.AdminUDPSettingsResponse{Enabled: runtime.IsUDPEnabled(), MaxLeases: runtime.UDPMaxLeases()}
			},
		)
	case types.PathAdminTCPPort:
		api.handlePortSettings(w, r, invalidRequestBody, runtime,
			api.server.SetTCPPortPolicy,
			func() any {
				return types.AdminTCPPortSettingsResponse{Enabled: runtime.IsTCPPortEnabled(), MaxLeases: runtime.TCPPortMaxLeases()}
			},
		)
	case types.PathAdminApproval:
		if !utils.RequireMethod(w, r, http.MethodPost) {
			return
		}
		req, ok := utils.DecodeJSONRequestAs[types.AdminApprovalModeRequest](w, r, adminBodyLimit, invalidRequestBody)
		if !ok {
			return
		}
		if err := runtime.Approver().SetMode(policy.Mode(strings.TrimSpace(req.Mode))); err != nil {
			utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidMode, "invalid mode (must be 'auto' or 'manual')")
			return
		}
		saveAdminState(api.adminSettingsPath, runtime, api.landingPageEnabled.Load())
		utils.WriteAPIData(w, http.StatusOK, types.AdminApprovalModeResponse{
			ApprovalMode: string(runtime.Approver().Mode()),
		})
	default:
		switch {
		case strings.HasPrefix(path, types.PathAdminLeasesPrefix):
			rest := strings.TrimPrefix(path, types.PathAdminLeasesPrefix)
			parts := strings.Split(rest, "/")
			if len(parts) != 3 {
				http.NotFound(w, r)
				return
			}

			name, err := utils.DecodeBase64URLString(parts[0])
			if err != nil {
				utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "invalid identity")
				return
			}
			address, err := utils.DecodeBase64URLString(parts[1])
			if err != nil {
				utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidAddress, "invalid address")
				return
			}
			normalizedIdentity, err := identity.NormalizeIdentity(types.Identity{
				Name:    name,
				Address: address,
			})
			if err != nil {
				utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "invalid identity")
				return
			}
			identityKey := normalizedIdentity.Key()
			approver := runtime.Approver()

			type identityAction struct {
				post   func() bool // returns true if response was already written (error path)
				delete func()
			}
			actions := map[string]identityAction{
				"ban": {
					post:   func() bool { runtime.BanIdentity(identityKey); return false },
					delete: func() { runtime.UnbanIdentity(identityKey) },
				},
				"bps": {
					post: func() bool {
						req, ok := utils.DecodeJSONRequestAs[types.AdminBPSRequest](w, r, adminBodyLimit, invalidRequestBody)
						if !ok {
							return true
						}
						if req.BPS <= 0 {
							utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "bps must be greater than zero")
							return true
						}
						runtime.BPSManager().SetIdentityBPS(identityKey, req.BPS)
						return false
					},
					delete: func() { runtime.BPSManager().DeleteIdentityBPS(identityKey) },
				},
				"approve": {
					post:   func() bool { approver.Approve(identityKey); approver.Undeny(identityKey); return false },
					delete: func() { approver.Revoke(identityKey) },
				},
				"deny": {
					post:   func() bool { approver.Deny(identityKey); return false },
					delete: func() { approver.Undeny(identityKey) },
				},
			}

			action, ok := actions[parts[2]]
			if !ok {
				http.NotFound(w, r)
				return
			}
			switch r.Method {
			case http.MethodPost:
				if action.post() {
					return
				}
			case http.MethodDelete:
				action.delete()
			default:
				methodNotAllowed.Write(w)
				return
			}
			saveAdminState(api.adminSettingsPath, runtime, api.landingPageEnabled.Load())
			utils.WriteAPIData(w, http.StatusOK, map[string]any{})
		case strings.HasPrefix(path, types.PathAdminIPsPrefix):
			if !strings.HasSuffix(path, "/ban") {
				http.NotFound(w, r)
				return
			}

			rawIP := strings.TrimSuffix(strings.TrimPrefix(path, types.PathAdminIPsPrefix), "/ban")
			rawIP = strings.Trim(rawIP, "/")
			if net.ParseIP(rawIP) == nil {
				utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidIP, "invalid IP address")
				return
			}

			filter := runtime.IPFilter()
			switch r.Method {
			case http.MethodPost:
				filter.BanIP(rawIP)
			case http.MethodDelete:
				filter.UnbanIP(rawIP)
			default:
				methodNotAllowed.Write(w)
				return
			}
			saveAdminState(api.adminSettingsPath, runtime, api.landingPageEnabled.Load())
			utils.WriteAPIData(w, http.StatusOK, map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}
}

func (api *RelayAPI) handlePortSettings(
	w http.ResponseWriter,
	r *http.Request,
	invalidBody utils.APIErrorResponse,
	runtime *policy.Runtime,
	setPolicy func(bool, int),
	buildResponse func() any,
) {
	if !utils.RequireMethod(w, r, http.MethodPost) {
		return
	}
	req, ok := utils.DecodeJSONRequestAs[types.AdminPortSettingsRequest](w, r, 1<<16, invalidBody)
	if !ok {
		return
	}
	if req.MaxLeases < 0 {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "max_leases must be non-negative")
		return
	}
	setPolicy(req.Enabled, req.MaxLeases)
	saveAdminState(api.adminSettingsPath, runtime, api.landingPageEnabled.Load())
	utils.WriteAPIData(w, http.StatusOK, buildResponse())
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
