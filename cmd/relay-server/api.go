package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gosuda/portal-tunnel/v2/cmd/portal-tunnel/installer"
	"github.com/gosuda/portal-tunnel/v2/portal"
	"github.com/gosuda/portal-tunnel/v2/portal/auth"
	"github.com/gosuda/portal-tunnel/v2/portal/identity"
	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

//go:embed dist/*
var embeddedDistFS embed.FS

type RelayAPI struct {
	server            *portal.Server
	auth              *auth.WalletAuthenticator
	adminSettingsPath string
	thumbnails        *thumbnailService

	landingPageEnabled atomic.Bool
}

func NewRelayAPI(server *portal.Server, identityPath string, defaultLandingPageEnabled bool, headlessShellURL string, adminWallets []string) (*RelayAPI, error) {
	if server == nil {
		return nil, errors.New("relay api requires portal server")
	}
	runtime := server.PolicyRuntime()
	if runtime == nil {
		return nil, errors.New("relay api requires policy runtime")
	}
	adminSettingsPath := identity.ResolveRelayAdminSettingsPath(identityPath)
	if adminSettingsPath == "" {
		return nil, errors.New("relay api requires identity path")
	}
	state, err := loadAdminState(adminSettingsPath, server)
	if err != nil {
		return nil, err
	}
	relayIdentity := server.RelayIdentity()
	allowedWallets := append([]string{relayIdentity.Address}, adminWallets...)
	authenticator, err := auth.NewWalletAuthenticator(auth.WalletAuthConfig{
		AllowedAddresses: allowedWallets,
		Statement:        "Sign in to Portal relay admin",
	})
	if err != nil {
		return nil, err
	}

	api := &RelayAPI{
		server:            server,
		auth:              authenticator,
		adminSettingsPath: strings.TrimSpace(adminSettingsPath),
		thumbnails:        newThumbnailService(headlessShellURL),
	}
	landingPageEnabled := defaultLandingPageEnabled
	if state.LandingPageEnabled != nil {
		landingPageEnabled = *state.LandingPageEnabled
	}
	api.landingPageEnabled.Store(landingPageEnabled)
	return api, nil
}

func (api *RelayAPI) Handler() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		if !utils.RequireMethod(w, r, http.MethodGet) {
			return
		}
		utils.WriteAPIData(w, http.StatusOK, map[string]any{
			"service": "portal-relay",
			"root":    api.server.RelayIdentity().Name,
		})
	})
	mux.HandleFunc(types.PathAdmin, api.serveAdmin)
	mux.HandleFunc(types.PathAdminPrefix, api.serveAdmin)
	mux.HandleFunc(types.PathPublicState, api.servePublicState)
	mux.HandleFunc(types.PathServiceStatus, api.serveServiceStatus)
	mux.HandleFunc(types.PathThumbnailPrefix, api.serveThumbnail)
	mux.HandleFunc(types.PathInstallShell, func(w http.ResponseWriter, r *http.Request) {
		serveInstallScript(w, r, api.server.PortalURL(), false)
	})
	mux.HandleFunc(types.PathInstallPowerShell, func(w http.ResponseWriter, r *http.Request) {
		serveInstallScript(w, r, api.server.PortalURL(), true)
	})
	mux.HandleFunc(types.PathInstallBinPrefix, serveInstallBinary)

	return mux
}

func (api *RelayAPI) servePublicState(w http.ResponseWriter, r *http.Request) {
	if !utils.RequireMethod(w, r, http.MethodGet) {
		return
	}

	leases := api.server.PublicLeases()
	api.attachAutomaticThumbnails(leases)
	utils.WriteAPIData(w, http.StatusOK, types.PublicStateResponse{
		Leases:             leases,
		LandingPageEnabled: api.landingPageEnabled.Load(),
	})
}

func (api *RelayAPI) serveServiceStatus(w http.ResponseWriter, r *http.Request) {
	if !utils.RequireMethod(w, r, http.MethodGet) {
		return
	}

	hostname := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hostname")))
	if hostname == "" {
		utils.InvalidRequestError(errors.New("hostname is required")).Write(w)
		return
	}

	resp := types.ServiceStatusResponse{
		Hostname: hostname,
	}
	if lease, ok := api.publicLeaseByHostname(hostname); ok {
		resp.Hostname = lease.Hostname
		resp.Registered = true
		resp.ServiceAlive = lease.Ready > 0
	}
	utils.WriteAPIData(w, http.StatusOK, resp)
}

func (api *RelayAPI) serveThumbnail(w http.ResponseWriter, r *http.Request) {
	if !utils.RequireMethod(w, r, http.MethodGet) {
		return
	}

	hostname := strings.TrimPrefix(r.URL.Path, types.PathThumbnailPrefix)
	hostname = strings.TrimSpace(strings.ToLower(hostname))
	if hostname == "" || api.thumbnails == nil {
		http.NotFound(w, r)
		return
	}

	lease, ok := api.publicLeaseByHostname(hostname)
	if !ok || lease.Metadata.Thumbnail != "" {
		api.thumbnails.remove(hostname)
		http.NotFound(w, r)
		return
	}

	data, contentType, ok := api.thumbnails.get(hostname)
	if !ok {
		var err error
		data, contentType, err = api.thumbnails.load(hostname)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (api *RelayAPI) publicLeaseByHostname(hostname string) (types.Lease, bool) {
	hostname = utils.NormalizeHostname(hostname)
	if hostname == "" {
		return types.Lease{}, false
	}
	for _, lease := range api.server.PublicLeases() {
		if utils.HostnameMatchesPattern(lease.Hostname, hostname) {
			return lease, true
		}
	}
	return types.Lease{}, false
}

func (api *RelayAPI) attachAutomaticThumbnails(leases []types.Lease) {
	for i := range leases {
		api.attachAutomaticThumbnail(leases[i].Hostname, &leases[i].Metadata)
	}
}

func (api *RelayAPI) attachAutomaticAdminThumbnails(leases []types.AdminLease) {
	for i := range leases {
		api.attachAutomaticThumbnail(leases[i].Hostname, &leases[i].Metadata)
	}
}

func (api *RelayAPI) attachAutomaticThumbnail(hostname string, metadata *types.LeaseMetadata) {
	if api.thumbnails == nil {
		return
	}
	if hostname == "" || metadata == nil || metadata.Thumbnail != "" {
		return
	}
	metadata.Thumbnail = types.PathThumbnailPrefix + hostname
	api.thumbnails.triggerAsync(hostname)
}

func (api *RelayAPI) Close() {
	if api.thumbnails == nil {
		return
	}
	api.thumbnails.close()
}

func serveInstallBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, types.PathInstallBinPrefix), "/")
	checksumRequest := strings.HasSuffix(slug, ".sha256")
	if checksumRequest {
		slug = strings.TrimSuffix(slug, ".sha256")
	}

	filename, ok := installer.AssetFilename(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := embeddedDistFS.ReadFile("dist/tunnel/" + filename)
	if err != nil {
		redirectURL := types.OfficialReleaseBaseURL + "/latest/download/" + filename
		if checksumRequest {
			redirectURL += ".sha256"
		}
		http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
		return
	}
	sum := sha256.Sum256(data)
	checksumHex := hex.EncodeToString(sum[:])

	if checksumRequest {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = fmt.Fprintf(w, "%s  %s\n", checksumHex, filename)
		}
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("X-Checksum-Sha256", checksumHex)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func serveInstallScript(w http.ResponseWriter, r *http.Request, portalURL string, isWindows bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	script, filename, contentType, err := installer.RelayScript(portalURL, isWindows)
	if err != nil {
		http.Error(w, "failed to render install script", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filename))
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(script))
	}
}
