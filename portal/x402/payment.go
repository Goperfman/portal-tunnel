package x402

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	facilitatorcore "github.com/gosuda/x402-facilitator/facilitator"
	suischeme "github.com/gosuda/x402-facilitator/scheme/sui"
	facilitatortypes "github.com/gosuda/x402-facilitator/types"

	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

// Payment owns one Sui USDC x402 payment contract and its facilitator runtime.
type Payment struct {
	payment      types.X402Payment
	facilitator  facilitatorcore.Facilitator
	requirements facilitatortypes.PaymentRequirements
}

func NewUSDCPayment(payment types.X402Payment) (*Payment, error) {
	network := strings.TrimSpace(payment.Network)
	if network == "" {
		network = Network(payment.Testnet)
	}
	network = strings.ToLower(network)
	asset, err := usdcAsset(network)
	if err != nil {
		return nil, err
	}
	payTo := suischeme.NormalizeAddress(payment.PayTo)
	if payTo == "" {
		return nil, errors.New("x402 USDC payment requires a Sui pay-to address")
	}
	amount := strings.TrimSpace(payment.Amount)
	n, err := strconv.ParseUint(amount, 10, 64)
	if err != nil || n == 0 {
		return nil, fmt.Errorf("x402 USDC payment amount must be a positive atomic amount: %s", amount)
	}
	maxTimeoutSeconds := payment.MaxTimeoutSeconds
	if maxTimeoutSeconds <= 0 {
		maxTimeoutSeconds = defaultMaxTimeoutSeconds
	}
	requirements := facilitatortypes.PaymentRequirements{
		Scheme:            string(facilitatortypes.Exact),
		Network:           network,
		Asset:             asset,
		Amount:            amount,
		PayTo:             payTo,
		MaxTimeoutSeconds: maxTimeoutSeconds,
		Extra: map[string]interface{}{
			"asset":               "USDC",
			"assetTransferMethod": "sui-gasless-stablecoin-address-balance",
		},
	}
	endpoints := append([]string(nil), payment.Endpoints...)
	facilitator, err := newUSDCFacilitator(requirements.Network, requirements.Asset, endpoints...)
	if err != nil {
		return nil, err
	}
	networkName := NetworkDisplayName(requirements.Network)
	if networkName == "" {
		networkName = requirements.Network
	}
	payment.Testnet = strings.EqualFold(requirements.Network, TestnetNetwork)
	payment.Network = requirements.Network
	payment.NetworkName = networkName
	payment.Asset = requirements.Asset
	payment.PayTo = requirements.PayTo
	payment.Amount = requirements.Amount
	payment.MaxTimeoutSeconds = requirements.MaxTimeoutSeconds
	payment.Endpoints = endpoints
	payment.ResourcePath = strings.TrimSpace(payment.ResourcePath)
	payment.ResourceDescription = strings.TrimSpace(payment.ResourceDescription)
	payment.ResourceMimeType = strings.TrimSpace(payment.ResourceMimeType)

	return &Payment{
		payment:      payment,
		facilitator:  facilitator,
		requirements: requirements,
	}, nil
}

func (p *Payment) Verify(ctx context.Context, w http.ResponseWriter, r *http.Request) (*facilitatortypes.PaymentPayload, bool) {
	if p == nil {
		http.Error(w, "payment is not configured", http.StatusInternalServerError)
		return nil, false
	}
	if p.facilitator == nil {
		http.Error(w, "x402 facilitator is not configured", http.StatusInternalServerError)
		return nil, false
	}

	rawPayment := ""
	for _, name := range []string{types.HeaderXPayment, types.HeaderPaymentSignature} {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			rawPayment = value
			break
		}
	}
	if rawPayment == "" {
		p.writePaymentRequired(w, r, "payment required")
		return nil, false
	}

	var payload *facilitatortypes.PaymentPayload
	var decoded facilitatortypes.PaymentPayload
	if err := json.Unmarshal([]byte(rawPayment), &decoded); err == nil {
		payload = &decoded
	}
	if payload == nil {
		for _, encoding := range []*base64.Encoding{
			base64.StdEncoding,
			base64.RawStdEncoding,
			base64.URLEncoding,
			base64.RawURLEncoding,
		} {
			raw, err := encoding.DecodeString(rawPayment)
			if err != nil {
				continue
			}
			var decoded facilitatortypes.PaymentPayload
			if err := json.Unmarshal(raw, &decoded); err == nil {
				payload = &decoded
				break
			}
		}
	}
	if payload == nil {
		p.writePaymentRequired(w, r, "invalid payment payload")
		return nil, false
	}
	verified, err := p.facilitator.Verify(ctx, payload, &p.requirements)
	if err != nil {
		http.Error(w, "verify x402 payment", http.StatusBadGateway)
		return nil, false
	}
	if verified == nil || !verified.IsValid {
		reason := "invalid payment"
		if verified != nil {
			reason = strings.TrimSpace(verified.InvalidReason)
			if reason == "" {
				reason = strings.TrimSpace(verified.InvalidMessage)
			}
		}
		if reason == "" {
			reason = "invalid payment"
		}
		p.writePaymentRequired(w, r, reason)
		return nil, false
	}
	return payload, true
}

func (p *Payment) Settle(ctx context.Context, w http.ResponseWriter, r *http.Request, payment *facilitatortypes.PaymentPayload) (*facilitatortypes.PaymentSettleResponse, bool) {
	if p == nil {
		http.Error(w, "payment is not configured", http.StatusInternalServerError)
		return nil, false
	}
	if p.facilitator == nil {
		http.Error(w, "x402 facilitator is not configured", http.StatusInternalServerError)
		return nil, false
	}
	if payment == nil {
		http.Error(w, "x402 payment is missing", http.StatusInternalServerError)
		return nil, false
	}
	settled, err := p.facilitator.Settle(ctx, payment, &p.requirements)
	if err != nil {
		p.writePaymentRequired(w, r, "payment settlement failed")
		return nil, false
	}
	if settled == nil || !settled.Success {
		p.writePaymentRequired(w, r, "payment settlement failed")
		return nil, false
	}
	return settled, true
}

func (p *Payment) writePaymentRequired(w http.ResponseWriter, r *http.Request, reason string) {
	if p == nil {
		http.Error(w, reason, http.StatusPaymentRequired)
		return
	}
	resourceURL := ""
	if r != nil && r.URL != nil {
		resourceURL = utils.PublicURLForPath(r, r.URL.RequestURI())
	}
	body := struct {
		X402Version int                                    `json:"x402Version"`
		Error       string                                 `json:"error,omitempty"`
		Resource    *facilitatortypes.ResourceInfo         `json:"resource,omitempty"`
		Accepts     []facilitatortypes.PaymentRequirements `json:"accepts"`
	}{
		X402Version: int(facilitatortypes.X402VersionV2),
		Error:       strings.TrimSpace(reason),
		Resource:    &facilitatortypes.ResourceInfo{URL: resourceURL},
		Accepts:     []facilitatortypes.PaymentRequirements{p.requirements},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "encode x402 payment requirements", http.StatusInternalServerError)
		return
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(types.HeaderPaymentRequired, encoded)
	w.Header().Set(types.HeaderXPaymentRequired, encoded)
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write(raw)
}

func (p *Payment) WritePrepare(w http.ResponseWriter, r *http.Request, sender, resourcePath string) {
	if p == nil {
		http.Error(w, "payment is not configured", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	cancel := func() {}
	if p.payment.RequestTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, p.payment.RequestTimeout)
	}
	defer cancel()

	sender = suischeme.NormalizeAddress(sender)
	if sender == "" {
		http.Error(w, "sender is required", http.StatusBadRequest)
		return
	}
	if p.requirements.Network == "" || p.requirements.Asset == "" {
		http.Error(w, "payment is not configured", http.StatusInternalServerError)
		return
	}

	coinObjects, err := suischeme.ListOwnedGaslessStablecoinCoinObjects(ctx, p.requirements.Network, sender, p.requirements.Asset, p.payment.Endpoints)
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

	var prepareTransaction *struct {
		Transaction string `json:"transaction"`
	}
	if len(nonZeroCoinObjects) > 0 {
		txBytes, err := suischeme.BuildCoinObjectsToAddressBalanceTransferTransaction(ctx, suischeme.CoinObjectsToAddressBalanceTransfer{
			Sender:      sender,
			Recipient:   sender,
			Network:     p.requirements.Network,
			Asset:       p.requirements.Asset,
			CoinObjects: nonZeroCoinObjects,
			Endpoints:   p.payment.Endpoints,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("build prepare transaction: %v", err), http.StatusBadGateway)
			return
		}
		prepareTransaction = &struct {
			Transaction string `json:"transaction"`
		}{Transaction: base64.StdEncoding.EncodeToString(txBytes)}
	}

	paymentTxBytes, err := suischeme.BuildGaslessStablecoinTransferTransaction(ctx, suischeme.GaslessStablecoinTransfer{
		Sender:    sender,
		Recipient: p.requirements.PayTo,
		Network:   p.requirements.Network,
		Asset:     p.requirements.Asset,
		Amount:    p.requirements.Amount,
		Endpoints: p.payment.Endpoints,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("build payment transaction: %v", err), http.StatusBadGateway)
		return
	}

	resourcePath = strings.TrimSpace(resourcePath)
	if resourcePath == "" {
		resourcePath = strings.TrimSpace(p.payment.ResourcePath)
	}
	if resourcePath == "" && r.URL != nil {
		resourcePath = r.URL.Path
	}
	if resourcePath == "" {
		resourcePath = "/"
	}
	resourceMimeType := strings.TrimSpace(p.payment.ResourceMimeType)
	if resourceMimeType == "" {
		resourceMimeType = "text/html"
	}
	utils.WritePaymentJSON(w, http.StatusOK, types.X402PreparePaymentResponse{
		X402Version:         int(facilitatortypes.X402VersionV2),
		PaymentRequirements: p.requirements,
		Resource: &facilitatortypes.ResourceInfo{
			URL:         utils.PublicURLForPath(r, resourcePath),
			Description: strings.TrimSpace(p.payment.ResourceDescription),
			MimeType:    resourceMimeType,
		},
		PrepareTransaction: prepareTransaction,
		PaymentTransaction: struct {
			Transaction string `json:"transaction"`
		}{Transaction: base64.StdEncoding.EncodeToString(paymentTxBytes)},
	})
}
