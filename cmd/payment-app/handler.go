package main

import (
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gosuda/portal-tunnel/v2/portal/x402"
	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

//go:embed static/index.html static/style.css
var staticFiles embed.FS

const paidPhotoPath = "/paid/photo"

var (
	indexPage = template.Must(template.ParseFS(staticFiles, "static/index.html"))
	photoPage = template.Must(template.New("photo").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.PageTitle}}</title>
  <meta name="description" content="{{.PageDescription}}">
  <meta property="og:type" content="website">
  <meta property="og:title" content="{{.PageTitle}}">
  <meta property="og:description" content="{{.PageDescription}}">
  <meta property="og:image" content="{{.OGImage}}">
  <meta property="og:url" content="{{.URL}}">
  <meta name="twitter:card" content="summary_large_image">
  <meta name="twitter:title" content="{{.PageTitle}}">
  <meta name="twitter:description" content="{{.PageDescription}}">
  <meta name="twitter:image" content="{{.OGImage}}">
  <style>
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: flex;
      padding: 0;
      background: #f7f8fb;
      color: #182033;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    }
    main {
      display: grid;
      width: 100%;
      min-height: 100vh;
      grid-template-rows: minmax(0, 1fr) auto;
      overflow: hidden;
      background: #ffffff;
    }
    img {
      width: 100%;
      height: 100%;
      min-height: 0;
      display: block;
      object-fit: contain;
      background: #111827;
    }
    section {
      display: grid;
      gap: 10px;
      padding: 18px;
      border-top: 1px solid #e5eaf0;
    }
    .eyebrow {
      margin: 0;
      color: #0e7490;
      font-size: 12px;
      font-weight: 800;
      text-transform: uppercase;
      letter-spacing: 0;
    }
    h1 {
      margin: 0;
      color: #111827;
      font-size: 25px;
      line-height: 1.18;
    }
    p {
      margin: 0;
      color: #4b5565;
      font-size: 15px;
      line-height: 1.55;
    }
    dl {
      display: grid;
      grid-template-columns: repeat(3, 1fr);
      gap: 10px;
      margin: 4px 0 0;
    }
    div { min-width: 0; }
    dt {
      color: #667085;
      font-size: 12px;
      font-weight: 700;
    }
    dd {
      margin: 4px 0 0;
      overflow-wrap: anywhere;
      color: #111827;
      font-size: 13px;
      font-weight: 750;
    }
    @media (max-width: 640px) {
      section { padding: 14px; }
      dl { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <main>
    <img src="{{.PhotoURL}}" alt="Unlocked protected image">
    <section>
      <p class="eyebrow">Payment complete</p>
      <h1>Image unlocked</h1>
      <p>The protected image is available after the {{.Amount}} atomic USDC x402 settlement.</p>
      <dl>
        <div>
          <dt>Amount</dt>
          <dd>{{.Amount}} atomic USDC</dd>
        </div>
        <div>
          <dt>Network</dt>
          <dd>{{.NetworkName}}</dd>
        </div>
        <div>
          <dt>Recipient</dt>
          <dd>{{.RecipientAddress}}</dd>
        </div>
        {{if .TransactionID}}
        <div>
          <dt>Transaction</dt>
          <dd>{{.TransactionID}}</dd>
        </div>
        {{end}}
      </dl>
    </section>
  </main>
</body>
</html>
`))
)

type paymentHandlerConfig struct {
	Metadata          types.LeaseMetadata
	Testnet           bool
	PayTo             string
	Amount            string
	MaxTimeoutSeconds int
	RequestTimeout    time.Duration
	Endpoints         []string
	PhotoURL          string
}

type paymentHandler struct {
	metadata    types.LeaseMetadata
	network     string
	networkName string
	asset       string
	amount      string
	payTo       string
	endpoints   []string
	photoURL    string
}

type paymentPageData struct {
	PageTitle        string
	PageDescription  string
	URL              string
	OGImage          string
	ProtectedPath    string
	Network          string
	NetworkName      string
	Asset            string
	Amount           string
	PhotoURL         string
	RecipientAddress string
	TransactionID    string
	ConfigJSON       template.JS
}

func newHandler(cfg paymentHandlerConfig) (http.Handler, error) {
	handler := &paymentHandler{
		metadata: cfg.Metadata.Copy(),
		photoURL: strings.TrimSpace(cfg.PhotoURL),
	}
	paidPhotoHandler, err := x402.NewUSDCPaymentHandler(types.X402Payment{
		Testnet:             cfg.Testnet,
		PayTo:               cfg.PayTo,
		Amount:              cfg.Amount,
		MaxTimeoutSeconds:   cfg.MaxTimeoutSeconds,
		RequestTimeout:      cfg.RequestTimeout,
		Endpoints:           cfg.Endpoints,
		ResourcePath:        paidPhotoPath,
		ResourceDescription: cfg.Metadata.Description,
		ResourceMimeType:    "text/html",
	}, paidPhotoPath, http.MethodGet, handler.renderPaidPhoto)
	if err != nil {
		return nil, err
	}
	payment := paidPhotoHandler.Payment()
	handler.network = payment.Network
	handler.networkName = payment.NetworkName
	handler.asset = payment.Asset
	handler.amount = payment.Amount
	handler.payTo = payment.PayTo
	handler.endpoints = append([]string(nil), payment.Endpoints...)
	handler.photoURL = strings.TrimSpace(cfg.PhotoURL)

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/static/style.css", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.Handle(types.X402PreparePath, paidPhotoHandler)
	mux.HandleFunc("/", handler.handleIndex)
	mux.Handle(paidPhotoPath, paidPhotoHandler)
	return mux, nil
}

func (h *paymentHandler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !utils.RequireMethod(w, r, http.MethodGet) {
		return
	}
	data := h.newPaymentPageData(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = indexPage.Execute(w, data)
}

func (h *paymentHandler) renderPaidPhoto(w http.ResponseWriter, r *http.Request, result types.X402PaymentResult) {
	data := h.newPaymentPageData(r)
	data.URL = utils.PublicURLForPath(r, paidPhotoPath)
	data.TransactionID = result.TransactionID
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = photoPage.Execute(w, data)
}

func (h *paymentHandler) newPaymentPageData(r *http.Request) paymentPageData {
	description := strings.TrimSpace(h.metadata.Description)
	if description == "" {
		description = "Connect a Sui wallet, settle USDC with x402, and reveal the protected image."
	}
	config := map[string]any{
		"network":       h.network,
		"networkName":   h.networkName,
		"asset":         h.asset,
		"amount":        h.amount,
		"payTo":         h.payTo,
		"preparePath":   types.X402PreparePath,
		"protectedPath": paidPhotoPath,
	}
	if len(h.endpoints) > 0 {
		config["endpoints"] = append([]string(nil), h.endpoints...)
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		configJSON = []byte("{}")
	}
	return paymentPageData{
		PageTitle:        "Portal Sui Wallet Payment",
		PageDescription:  description,
		URL:              utils.PublicURLForPath(r, "/"),
		OGImage:          h.photoURL,
		ProtectedPath:    paidPhotoPath,
		Network:          h.network,
		NetworkName:      h.networkName,
		Asset:            h.asset,
		Amount:           h.amount,
		PhotoURL:         h.photoURL,
		RecipientAddress: h.payTo,
		ConfigJSON:       template.JS(string(configJSON)),
	}
}
