package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	portalx402 "github.com/gosuda/portal-tunnel/v2/portal/x402"
	"github.com/gosuda/portal-tunnel/v2/sdk"
	"github.com/gosuda/portal-tunnel/v2/types"
)

//go:embed static
var staticFiles embed.FS

type premiumContent struct {
	Title string
	Price string
}

var premiumCatalog = map[string]premiumContent{
	"/api/premium":         {Title: "Premium overview"},
	"/api/premium/basic":   {Title: "Basic premium payload", Price: "$0.001"},
	"/api/premium/report":  {Title: "Market report", Price: "$0.010"},
	"/api/premium/dataset": {Title: "Dataset export", Price: "$0.050"},
}

func newHandler(appIdentity types.Identity, metadata types.LeaseMetadata, x402Config *types.X402Config) (http.Handler, error) {
	staticFS, _ := fs.Sub(staticFiles, "static")

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	mux.HandleFunc("/api/ping", handlePing)
	mux.Handle("/ws", websocket.Handler(handleWebSocket))
	mux.HandleFunc("/api/test-cookies", handleCookies)

	premiumHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, ok := premiumCatalog[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		price := item.Price
		if price == "" && x402Config != nil {
			price = strings.TrimSpace(x402Config.Price)
		}
		if price == "" {
			price = "$0.001"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content":           item.Title,
			"message":           "premium data unlocked",
			"paid":              true,
			"price":             price,
			"recipient_address": appIdentity.Address,
			"time":              time.Now().UTC().Format(time.RFC3339),
		})
	})
	if x402Config == nil || x402Config.Empty() {
		mux.HandleFunc("/api/premium", handlePaymentRequired)
		mux.HandleFunc("/api/premium/", handlePaymentRequired)
		return mux, nil
	}

	protectedPremium, err := portalx402.NewHTTPRouteHandler(portalx402.HTTPRouteHandlerConfig{
		Prefix:         "/api/premium",
		Next:           premiumHandler,
		X402:           *x402Config,
		TunnelIdentity: appIdentity,
		Metadata:       metadata,
		PriceResolver: func(_ context.Context, req portalx402.HTTPRequestContext) (string, error) {
			item, ok := premiumCatalog[req.Path]
			if !ok {
				return "", fmt.Errorf("unknown premium content path %q", req.Path)
			}
			if item.Price != "" {
				return item.Price, nil
			}
			price := strings.TrimSpace(x402Config.Price)
			if price == "" {
				price = "$0.001"
			}
			return price, nil
		},
	})
	if err != nil {
		return nil, err
	}
	for path := range premiumCatalog {
		mux.Handle(path, protectedPremium)
	}
	return mux, nil
}

func handlePing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message": "pong",
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

func handleWebSocket(conn *websocket.Conn) {
	defer conn.Close()
	for {
		var msg string
		if err := websocket.Message.Receive(conn, &msg); err != nil {
			return
		}
		if err := websocket.Message.Send(conn, "echo: "+msg); err != nil {
			return
		}
	}
}

func handleCookies(w http.ResponseWriter, r *http.Request) {
	secure := r != nil && r.TLS != nil

	for _, cookie := range []*http.Cookie{
		{Name: "session_id", Value: "abc123", Path: "/", MaxAge: 3600, Secure: secure},
		{Name: "auth_token", Value: "secret456", Path: "/", MaxAge: 3600, Secure: secure},
		{Name: "csrf_token", Value: "xyz789", Path: "/", MaxAge: 3600, Secure: secure},
		{Name: "user_pref", Value: "dark_mode", Path: "/", MaxAge: 86400, Secure: secure},
	} {
		http.SetCookie(w, cookie)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message": "4 cookies set: session_id, auth_token, csrf_token, user_pref",
	})
}

func newUDPInfoHandler(exposure *sdk.Exposure) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		udpAddrs, _ := exposure.WaitDatagramReady(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message":   "demo-udp is running",
			"udp_addrs": udpAddrs,
		})
	})
	return mux
}

func handlePaymentRequired(w http.ResponseWriter, r *http.Request) {
	path := "/api/premium"
	if r != nil && r.URL != nil && r.URL.Path != "" {
		path = r.URL.Path
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message": "payment required",
		"path":    path,
		"hint":    "start demo-app with --x402-facilitator-url and --x402-network to enable x402 settlement",
	})
}
