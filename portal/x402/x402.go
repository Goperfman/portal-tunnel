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

	facilitatorapi "github.com/gosuda/x402-facilitator/api"
	facilitatorcore "github.com/gosuda/x402-facilitator/facilitator"
	suischeme "github.com/gosuda/x402-facilitator/scheme/sui"
	facilitatortypes "github.com/gosuda/x402-facilitator/types"

	"github.com/gosuda/portal-tunnel/v2/types"
)

const (
	MainnetNetwork = "sui:mainnet"
	TestnetNetwork = "sui:testnet"

	defaultMaxTimeoutSeconds = 60
	paymentRequiredHeader    = "PAYMENT-REQUIRED"
	paymentResponseHeader    = "PAYMENT-RESPONSE"
	xPaymentHeader           = "X-PAYMENT"
	paymentSignatureHeader   = "PAYMENT-SIGNATURE"
)

var networkDisplayNames = map[string]string{
	MainnetNetwork: "Sui Mainnet",
	TestnetNetwork: "Sui Testnet",
}

func Network(testnet bool) string {
	if testnet {
		return TestnetNetwork
	}
	return MainnetNetwork
}

func NetworkDisplayName(network string) string {
	return networkDisplayNames[strings.TrimSpace(strings.ToLower(network))]
}

type FacilitatorConfig struct {
	Testnet bool
}

func MountFacilitator(mux *http.ServeMux, cfg FacilitatorConfig) error {
	if mux == nil {
		return errors.New("x402 facilitator requires an api mux")
	}
	facilitator, err := NewUSDCFacilitator(Network(cfg.Testnet))
	if err != nil {
		return fmt.Errorf("create sui x402 facilitator: %w", err)
	}
	mux.Handle(types.PathX402Facilitator+"/", http.StripPrefix(types.PathX402Facilitator, facilitatorapi.NewServer(facilitator)))
	return nil
}

func USDCAsset(network string) (string, error) {
	network = strings.ToLower(strings.TrimSpace(network))
	asset, ok := suischeme.GetGaslessStablecoinType(network, "USDC")
	if !ok {
		return "", fmt.Errorf("USDC is not gasless stablecoin allowlisted on %s", network)
	}
	return asset, nil
}

func NewUSDCFacilitator(network string) (facilitatorcore.Facilitator, error) {
	network = strings.ToLower(strings.TrimSpace(network))
	if network == "" {
		network = MainnetNetwork
	}
	asset, err := USDCAsset(network)
	if err != nil {
		return nil, err
	}
	return facilitatorcore.NewSuiFacilitatorWithOptions(network, "", "", facilitatorcore.SuiFacilitatorOptions{
		GaslessStablecoinTypes: []string{asset},
	})
}

type GateConfig struct {
	Network           string
	PayTo             string
	Amount            string
	MaxTimeoutSeconds int
}

type Gate struct {
	facilitator       facilitatorcore.Facilitator
	network           string
	asset             string
	payTo             string
	amount            string
	maxTimeoutSeconds int
}

func NewUSDCGate(cfg GateConfig) (*Gate, error) {
	network := strings.ToLower(strings.TrimSpace(cfg.Network))
	if network == "" {
		network = MainnetNetwork
	}
	asset, err := USDCAsset(network)
	if err != nil {
		return nil, err
	}
	payTo := suischeme.NormalizeAddress(cfg.PayTo)
	if payTo == "" {
		return nil, errors.New("x402 USDC payment requires a Sui pay-to address")
	}
	amount := strings.TrimSpace(cfg.Amount)
	n, err := strconv.ParseUint(amount, 10, 64)
	if err != nil || n == 0 {
		return nil, fmt.Errorf("x402 USDC payment amount must be a positive atomic amount: %s", cfg.Amount)
	}
	facilitator, err := NewUSDCFacilitator(network)
	if err != nil {
		return nil, err
	}
	maxTimeoutSeconds := cfg.MaxTimeoutSeconds
	if maxTimeoutSeconds <= 0 {
		maxTimeoutSeconds = defaultMaxTimeoutSeconds
	}
	return &Gate{
		facilitator:       facilitator,
		network:           network,
		asset:             asset,
		payTo:             payTo,
		amount:            amount,
		maxTimeoutSeconds: maxTimeoutSeconds,
	}, nil
}

func (g *Gate) Requirements() facilitatortypes.PaymentRequirements {
	if g == nil {
		return facilitatortypes.PaymentRequirements{}
	}
	return facilitatortypes.PaymentRequirements{
		Scheme:            string(facilitatortypes.Exact),
		Network:           g.network,
		Asset:             g.asset,
		Amount:            g.amount,
		PayTo:             g.payTo,
		MaxTimeoutSeconds: g.maxTimeoutSeconds,
		Extra: map[string]interface{}{
			"asset":               "USDC",
			"assetTransferMethod": "sui-gasless-stablecoin-address-balance",
		},
	}
}

type VerifiedPayment struct {
	Payload      facilitatortypes.PaymentPayload
	Requirements facilitatortypes.PaymentRequirements
}

type RequestError struct {
	StatusCode int
	Reason     string
	Err        error
}

func (e *RequestError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason != "" {
		return e.Reason
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return http.StatusText(e.StatusCode)
}

func (e *RequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (g *Gate) VerifyRequest(ctx context.Context, r *http.Request) (*VerifiedPayment, error) {
	if g == nil || g.facilitator == nil {
		return nil, &RequestError{StatusCode: http.StatusInternalServerError, Reason: "x402 payment gate is not configured"}
	}
	rawPayment := paymentHeader(r.Header)
	if rawPayment == "" {
		return nil, &RequestError{StatusCode: http.StatusPaymentRequired, Reason: "payment required"}
	}
	payload, err := DecodePaymentPayload(rawPayment)
	if err != nil {
		return nil, &RequestError{StatusCode: http.StatusPaymentRequired, Reason: "invalid payment payload", Err: err}
	}
	requirements := g.Requirements()
	verified, err := g.facilitator.Verify(ctx, payload, &requirements)
	if err != nil {
		return nil, &RequestError{StatusCode: http.StatusBadGateway, Reason: "verify x402 payment", Err: err}
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
		return nil, &RequestError{StatusCode: http.StatusPaymentRequired, Reason: reason}
	}
	return &VerifiedPayment{
		Payload:      *payload,
		Requirements: requirements,
	}, nil
}

func (g *Gate) SettleVerifiedPayment(ctx context.Context, payment *VerifiedPayment) (*facilitatortypes.PaymentSettleResponse, error) {
	if g == nil || g.facilitator == nil {
		return nil, errors.New("x402 payment gate is not configured")
	}
	if payment == nil {
		return nil, errors.New("x402 payment is missing")
	}
	settled, err := g.facilitator.Settle(ctx, &payment.Payload, &payment.Requirements)
	if err != nil {
		return nil, err
	}
	if settled == nil || !settled.Success {
		reason := "settlement failed"
		if settled != nil {
			reason = strings.TrimSpace(settled.ErrorReason)
			if reason == "" {
				reason = strings.TrimSpace(settled.ErrorMessage)
			}
		}
		return nil, errors.New(reason)
	}
	return settled, nil
}

func (g *Gate) WriteRequestError(w http.ResponseWriter, r *http.Request, err error) {
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		reqErr = &RequestError{StatusCode: http.StatusInternalServerError, Reason: err.Error(), Err: err}
	}
	if reqErr.StatusCode != http.StatusPaymentRequired {
		http.Error(w, reqErr.Error(), reqErr.StatusCode)
		return
	}
	g.WritePaymentRequired(w, r, reqErr.Error())
}

func (g *Gate) WritePaymentRequired(w http.ResponseWriter, r *http.Request, reason string) {
	body := paymentRequiredBody{
		X402Version: int(facilitatortypes.X402VersionV2),
		Error:       strings.TrimSpace(reason),
		Resource: &facilitatortypes.ResourceInfo{
			URL: PublicRequestURL(r),
		},
		Accepts: []facilitatortypes.PaymentRequirements{g.Requirements()},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "encode x402 payment requirements", http.StatusInternalServerError)
		return
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(paymentRequiredHeader, encoded)
	w.Header().Set("X-"+paymentRequiredHeader, encoded)
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write(raw)
}

type paymentRequiredBody struct {
	X402Version int                                    `json:"x402Version"`
	Error       string                                 `json:"error,omitempty"`
	Resource    *facilitatortypes.ResourceInfo         `json:"resource,omitempty"`
	Accepts     []facilitatortypes.PaymentRequirements `json:"accepts"`
}

func DecodePaymentPayload(value string) (*facilitatortypes.PaymentPayload, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty payment payload")
	}
	candidates := [][]byte{[]byte(value)}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := encoding.DecodeString(value); err == nil {
			candidates = append(candidates, decoded)
		}
	}
	var lastErr error
	for _, raw := range candidates {
		var payload facilitatortypes.PaymentPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			lastErr = err
			continue
		}
		return &payload, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("invalid payment payload")
}

func SetPaymentResponseHeaders(header http.Header, settled *facilitatortypes.PaymentSettleResponse) {
	if header == nil || settled == nil {
		return
	}
	raw, err := json.Marshal(settled)
	if err != nil {
		return
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	header.Set(paymentResponseHeader, encoded)
	header.Set("X-"+paymentResponseHeader, encoded)
}

func StripPaymentHeaders(header http.Header) {
	header.Del(xPaymentHeader)
	header.Del(paymentSignatureHeader)
	header.Del(paymentRequiredHeader)
	header.Del("X-" + paymentRequiredHeader)
	header.Del(paymentResponseHeader)
	header.Del("X-" + paymentResponseHeader)
}

func PublicRequestURL(r *http.Request) string {
	if r == nil || r.URL == nil {
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
		return r.URL.RequestURI()
	}
	return scheme + "://" + host + r.URL.RequestURI()
}

func paymentHeader(header http.Header) string {
	for _, name := range []string{xPaymentHeader, paymentSignatureHeader} {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

type verifiedPaymentContextKey struct{}

func ContextWithVerifiedPayment(ctx context.Context, payment *VerifiedPayment) context.Context {
	return context.WithValue(ctx, verifiedPaymentContextKey{}, payment)
}

func VerifiedPaymentFromContext(ctx context.Context) (*VerifiedPayment, bool) {
	payment, ok := ctx.Value(verifiedPaymentContextKey{}).(*VerifiedPayment)
	return payment, ok && payment != nil
}

var ErrSettlementFailed = errors.New("x402 settlement failed")

func SettlementError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrSettlementFailed, err)
}
