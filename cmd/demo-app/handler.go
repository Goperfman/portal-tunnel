package main

import (
	"embed"
	"encoding/json"
	"html/template"
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

const (
	premiumPath        = "/api/premium"
	defaultPremiumCost = "$0.01"
	premiumPhotoURL    = "https://image.s-h.day/generated/905a4835ad50.png"
)

var premiumPage = template.Must(template.New("premium").Parse(`<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Payment Complete</title>
  <style>
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 24px;
      background: #f6f7f9;
      color: #1f2937;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    }
    main {
      width: min(100%, 560px);
      overflow: hidden;
      border: 1px solid #e5e7eb;
      border-radius: 8px;
      background: #ffffff;
      box-shadow: 0 12px 32px rgba(17, 24, 39, 0.10);
    }
    img {
      width: 100%;
      aspect-ratio: 16 / 9;
      display: block;
      object-fit: cover;
      background: #e5e7eb;
    }
    section { padding: 22px; }
    .eyebrow {
      margin: 0 0 8px;
      color: #0f766e;
      font-size: 13px;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0;
    }
    h1 {
      margin: 0 0 10px;
      color: #111827;
      font-size: 24px;
      line-height: 1.2;
    }
    p {
      margin: 0;
      color: #4b5563;
      font-size: 15px;
      line-height: 1.55;
    }
    .details {
      display: flex;
      gap: 12px;
      justify-content: space-between;
      margin-top: 18px;
      padding-top: 16px;
      border-top: 1px solid #e5e7eb;
      color: #374151;
      font-size: 13px;
    }
    .details strong {
      display: block;
      color: #111827;
      font-size: 14px;
    }
    a {
      display: inline-block;
      margin-top: 20px;
      padding: 9px 13px;
      border-radius: 6px;
      background: #111827;
      color: #ffffff;
      font-size: 14px;
      font-weight: 700;
      text-decoration: none;
    }
    @media (max-width: 480px) {
      body { padding: 14px; }
      section { padding: 18px; }
      .details { flex-direction: column; }
    }
  </style>
</head>
<body>
  <main>
    <img src="{{.PhotoURL}}" alt="Unlocked premium photo">
    <section>
      <p class="eyebrow">Payment complete</p>
      <h1>Thanks for the payment.</h1>
      <p>Your {{.Price}} x402 payment unlocked this photo.</p>
      <div class="details">
        <div>
          <span>Amount</span>
          <strong>{{.Price}}</strong>
        </div>
        <div>
          <span>Recipient</span>
          <strong>{{.RecipientAddress}}</strong>
        </div>
      </div>
      <a href="/">Back to demo</a>
    </section>
  </main>
</body>
`))

type premiumPageData struct {
	Price            string
	PhotoURL         string
	RecipientAddress string
}

func newHandler(appIdentity types.Identity, metadata types.LeaseMetadata, x402Config *types.X402Config) (http.Handler, error) {
	staticFS, _ := fs.Sub(staticFiles, "static")

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	mux.HandleFunc("/api/ping", handlePing)
	mux.Handle("/ws", websocket.Handler(handleWebSocket))
	mux.HandleFunc("/api/test-cookies", handleCookies)

	if x402Config == nil || x402Config.Empty() {
		mux.HandleFunc(premiumPath, handlePaymentRequired)
		mux.HandleFunc(premiumPath+"/", handlePaymentRequired)
		return mux, nil
	}

	price := strings.TrimSpace(x402Config.Price)
	if price == "" {
		price = defaultPremiumCost
	}

	premiumHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = premiumPage.Execute(w, premiumPageData{
			Price:            price,
			PhotoURL:         premiumPhotoURL,
			RecipientAddress: appIdentity.Address,
		})
	})
	routeX402 := *x402Config
	routeX402.Price = price
	if strings.TrimSpace(routeX402.Resource) == "" {
		routeX402.Resource = premiumPath
	}
	if strings.TrimSpace(routeX402.MimeType) == "" {
		routeX402.MimeType = "text/html"
	}
	protectedPremium, err := portalx402.NewHTTPRouteHandler(portalx402.HTTPRouteHandlerConfig{
		Prefix:         premiumPath,
		Next:           premiumHandler,
		X402:           routeX402,
		TunnelIdentity: appIdentity,
		Metadata:       metadata,
	})
	if err != nil {
		return nil, err
	}
	mux.Handle(premiumPath, protectedPremium)
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
	path := premiumPath
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
