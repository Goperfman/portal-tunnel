package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	portalx402 "github.com/gosuda/portal-tunnel/v2/portal/x402"
	suischeme "github.com/gosuda/x402-facilitator/scheme/sui"
	facilitatortypes "github.com/gosuda/x402-facilitator/types"

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
	gate           *portalx402.Gate
	requirements   facilitatortypes.PaymentRequirements
	metadata       types.LeaseMetadata
	networkName    string
	requestTimeout time.Duration
	endpoints      []string
	photoURL       string
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

type preparePaymentRequest struct {
	Sender string `json:"sender"`
}

type walletTransaction struct {
	Transaction string `json:"transaction"`
}

type preparePaymentResponse struct {
	X402Version         int                                  `json:"x402Version"`
	PaymentRequirements facilitatortypes.PaymentRequirements `json:"paymentRequirements"`
	Resource            *facilitatortypes.ResourceInfo       `json:"resource,omitempty"`
	PrepareTransaction  *walletTransaction                   `json:"prepareTransaction,omitempty"`
	PaymentTransaction  walletTransaction                    `json:"paymentTransaction"`
}

func newHandler(cfg paymentHandlerConfig) (http.Handler, error) {
	gate, err := portalx402.NewUSDCGate(portalx402.GateConfig{
		Network:           portalx402.Network(cfg.Testnet),
		PayTo:             cfg.PayTo,
		Amount:            cfg.Amount,
		MaxTimeoutSeconds: cfg.MaxTimeoutSeconds,
	})
	if err != nil {
		return nil, err
	}
	requirements := gate.Requirements()
	networkName := portalx402.NetworkDisplayName(requirements.Network)
	if networkName == "" {
		networkName = requirements.Network
	}
	handler := &paymentHandler{
		gate:           gate,
		requirements:   requirements,
		metadata:       cfg.Metadata.Copy(),
		networkName:    networkName,
		requestTimeout: cfg.RequestTimeout,
		endpoints:      append([]string(nil), cfg.Endpoints...),
		photoURL:       strings.TrimSpace(cfg.PhotoURL),
	}

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/static/style.css", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("/api/payment/prepare", handler.handlePreparePayment)
	mux.HandleFunc("/", handler.handleIndex)
	mux.HandleFunc(paidPhotoPath, handler.handlePaidPhoto)
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

func (h *paymentHandler) handlePreparePayment(w http.ResponseWriter, r *http.Request) {
	if !utils.RequireMethod(w, r, http.MethodPost) {
		return
	}
	var req preparePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payment prepare request", http.StatusBadRequest)
		return
	}
	sender := suischeme.NormalizeAddress(req.Sender)
	if sender == "" {
		http.Error(w, "sender is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := h.requestContext(r)
	defer cancel()

	requirements := h.requirements
	coinObjects, err := suischeme.ListOwnedGaslessStablecoinCoinObjects(ctx, requirements.Network, sender, requirements.Asset, h.endpoints)
	if err != nil {
		http.Error(w, fmt.Sprintf("list USDC coin objects: %v", err), http.StatusBadGateway)
		return
	}
	nonZeroCoinObjects := make([]suischeme.OwnedCoinObject, 0, len(coinObjects))
	for _, coinObject := range coinObjects {
		if coinObject.Balance == 0 {
			continue
		}
		nonZeroCoinObjects = append(nonZeroCoinObjects, coinObject)
	}

	var prepareTransaction *walletTransaction
	if len(nonZeroCoinObjects) > 0 {
		txBytes, err := suischeme.BuildCoinObjectsToAddressBalanceTransferTransaction(ctx, suischeme.CoinObjectsToAddressBalanceTransfer{
			Sender:      sender,
			Recipient:   sender,
			Network:     requirements.Network,
			Asset:       requirements.Asset,
			CoinObjects: nonZeroCoinObjects,
			Endpoints:   h.endpoints,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("build prepare transaction: %v", err), http.StatusBadGateway)
			return
		}
		prepareTransaction = &walletTransaction{Transaction: base64.StdEncoding.EncodeToString(txBytes)}
	}

	paymentTxBytes, err := suischeme.BuildGaslessStablecoinTransferTransaction(ctx, suischeme.GaslessStablecoinTransfer{
		Sender:    sender,
		Recipient: requirements.PayTo,
		Network:   requirements.Network,
		Asset:     requirements.Asset,
		Amount:    requirements.Amount,
		Endpoints: h.endpoints,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("build payment transaction: %v", err), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, preparePaymentResponse{
		X402Version:         int(facilitatortypes.X402VersionV2),
		PaymentRequirements: requirements,
		Resource: &facilitatortypes.ResourceInfo{
			URL:         publicURLForPath(r, paidPhotoPath),
			Description: h.metadata.Description,
			MimeType:    "text/html",
		},
		PrepareTransaction: prepareTransaction,
		PaymentTransaction: walletTransaction{Transaction: base64.StdEncoding.EncodeToString(paymentTxBytes)},
	})
}

func (h *paymentHandler) handlePaidPhoto(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != paidPhotoPath {
		http.NotFound(w, r)
		return
	}
	if !utils.RequireMethod(w, r, http.MethodGet) {
		return
	}

	ctx, cancel := h.requestContext(r)
	defer cancel()

	payment, err := h.gate.VerifyRequest(ctx, r)
	if err != nil {
		h.gate.WriteRequestError(w, r, err)
		return
	}
	settled, err := h.gate.SettleVerifiedPayment(ctx, payment)
	if err != nil {
		h.gate.WritePaymentRequired(w, r, "payment settlement failed")
		return
	}
	portalx402.SetPaymentResponseHeaders(w.Header(), settled)

	data := h.newPaymentPageData(r)
	data.URL = publicURLForPath(r, paidPhotoPath)
	data.TransactionID = strings.TrimSpace(settled.Transaction)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = photoPage.Execute(w, data)
}

func (h *paymentHandler) requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	if h.requestTimeout <= 0 {
		return r.Context(), func() {}
	}
	return context.WithTimeout(r.Context(), h.requestTimeout)
}

func (h *paymentHandler) newPaymentPageData(r *http.Request) paymentPageData {
	requirements := h.requirements
	description := strings.TrimSpace(h.metadata.Description)
	if description == "" {
		description = "Connect a Sui wallet, settle USDC with x402, and reveal the protected image."
	}
	config := map[string]string{
		"network":       requirements.Network,
		"networkName":   h.networkName,
		"asset":         requirements.Asset,
		"amount":        requirements.Amount,
		"payTo":         requirements.PayTo,
		"protectedPath": paidPhotoPath,
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		configJSON = []byte("{}")
	}
	return paymentPageData{
		PageTitle:        "Portal Sui Wallet Payment",
		PageDescription:  description,
		URL:              publicURLForPath(r, "/"),
		OGImage:          h.photoURL,
		ProtectedPath:    paidPhotoPath,
		Network:          requirements.Network,
		NetworkName:      h.networkName,
		Asset:            requirements.Asset,
		Amount:           requirements.Amount,
		PhotoURL:         h.photoURL,
		RecipientAddress: requirements.PayTo,
		ConfigJSON:       template.JS(string(configJSON)),
	}
}

func publicURLForPath(r *http.Request, path string) string {
	if r == nil {
		return ""
	}
	scheme, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	host = strings.TrimSpace(host)
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return scheme + "://" + host + path
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
