package x402

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	facilitatorapi "github.com/gosuda/x402-facilitator/api"
	facilitatorcore "github.com/gosuda/x402-facilitator/facilitator"
	facilitatortypes "github.com/gosuda/x402-facilitator/types"
	foundationx402 "github.com/x402-foundation/x402/go"
	x402http "github.com/x402-foundation/x402/go/http"
	x402nethttp "github.com/x402-foundation/x402/go/http/nethttp"
	evmserver "github.com/x402-foundation/x402/go/mechanisms/evm/exact/server"

	"github.com/gosuda/portal-tunnel/v2/portal/identity"
	"github.com/gosuda/portal-tunnel/v2/types"
)

const (
	defaultPaymentTimeout   = 30 * time.Second
	defaultRouteDescription = "Portal protected route"
)

var networkDisplayNames = map[string]string{
	"eip155:1":      "Ethereum Mainnet",
	"eip155:8453":   "Base Mainnet",
	"eip155:84532":  "Base Sepolia",
	"eip155:42161":  "Arbitrum One",
	"eip155:421614": "Arbitrum Sepolia",
}

var networkPublicNodeRPCURLs = map[string]string{
	"eip155:1":      "https://ethereum-rpc.publicnode.com",
	"eip155:8453":   "https://base-rpc.publicnode.com",
	"eip155:84532":  "https://base-sepolia-rpc.publicnode.com",
	"eip155:42161":  "https://arbitrum-one-rpc.publicnode.com",
	"eip155:421614": "https://arbitrum-sepolia-rpc.publicnode.com",
}

func NetworkDisplayName(network string) string {
	return networkDisplayNames[strings.TrimSpace(strings.ToLower(network))]
}

func PublicNodeRPCURL(network string) string {
	return networkPublicNodeRPCURLs[strings.TrimSpace(strings.ToLower(network))]
}

func isTestnetNetwork(network string) bool {
	switch strings.TrimSpace(strings.ToLower(network)) {
	case "eip155:84532", "eip155:421614":
		return true
	default:
		return false
	}
}

type FacilitatorConfig struct {
	Network  string
	RPCURL   string
	Identity types.Identity
}

func MountFacilitator(mux *http.ServeMux, cfg FacilitatorConfig) error {
	if mux == nil {
		return errors.New("x402 facilitator requires an api mux")
	}
	network := strings.TrimSpace(cfg.Network)
	if network == "" {
		return errors.New("--x402-network is required when --x402-facilitator-enabled is set")
	}
	privateKey := strings.TrimSpace(cfg.Identity.PrivateKey)
	if privateKey == "" {
		return errors.New("relay identity private key is required when --x402-facilitator-enabled is set")
	}
	rpcURL := strings.TrimSpace(cfg.RPCURL)
	if rpcURL == "" {
		rpcURL = PublicNodeRPCURL(network)
	}
	facilitator, err := facilitatorcore.NewFacilitator(facilitatortypes.Exact, network, rpcURL, privateKey)
	if err != nil {
		return fmt.Errorf("create x402 facilitator: %w", err)
	}
	mux.Handle(types.PathX402FacilitatorPrefix, http.StripPrefix(types.PathX402Facilitator, facilitatorapi.NewServer(facilitator)))
	return nil
}

type HTTPRequestContext = x402http.HTTPRequestContext

type PriceResolver func(context.Context, HTTPRequestContext) (string, error)

type HTTPRouteHandlerConfig struct {
	Prefix         string
	Next           http.Handler
	X402           types.X402Config
	TunnelIdentity types.Identity
	Metadata       types.LeaseMetadata
	PriceResolver  PriceResolver
}

func NewHTTPRouteHandler(cfg HTTPRouteHandlerConfig) (http.Handler, error) {
	next := cfg.Next
	if next == nil {
		next = http.NotFoundHandler()
	}
	prefix := strings.TrimSpace(cfg.Prefix)
	if prefix == "" {
		prefix = "/"
	}
	network := strings.TrimSpace(cfg.X402.Network)
	if network == "" {
		return nil, fmt.Errorf("http route %q x402 network is required", prefix)
	}
	facilitatorURL := strings.TrimSpace(cfg.X402.FacilitatorURL)
	if facilitatorURL == "" {
		return nil, fmt.Errorf("http route %q x402 facilitator_url is required", prefix)
	}
	priceValue := strings.TrimSpace(cfg.X402.Price)
	if priceValue == "" && cfg.PriceResolver == nil {
		return nil, fmt.Errorf("http route %q x402 price is required", prefix)
	}
	var price any = foundationx402.Price(priceValue)
	if cfg.PriceResolver != nil {
		price = x402http.DynamicPriceFunc(func(ctx context.Context, req x402http.HTTPRequestContext) (foundationx402.Price, error) {
			resolvedPrice, err := cfg.PriceResolver(ctx, req)
			if err != nil {
				return "", err
			}
			resolvedPrice = strings.TrimSpace(resolvedPrice)
			if resolvedPrice == "" {
				return "", fmt.Errorf("http route %q x402 dynamic price is empty", prefix)
			}
			return foundationx402.Price(resolvedPrice), nil
		})
	}
	payTo := strings.TrimSpace(cfg.X402.PayTo)
	if payTo == "" || strings.EqualFold(payTo, types.X402PayToIdentity) {
		payTo = strings.TrimSpace(cfg.TunnelIdentity.Address)
	}
	if payTo == "" {
		return nil, fmt.Errorf("http route %q x402 pay_to is required", prefix)
	}
	payTo, err := identity.NormalizeEVMAddress(payTo)
	if err != nil {
		return nil, fmt.Errorf("http route %q x402 pay_to: %w", prefix, err)
	}
	if cfg.X402.PaymentTimeoutSecs < 0 {
		return nil, errors.New("x402 payment_timeout_seconds cannot be negative")
	}
	if cfg.X402.MaxTimeoutSeconds < 0 {
		return nil, errors.New("x402 max_timeout_seconds cannot be negative")
	}

	timeout := defaultPaymentTimeout
	if cfg.X402.PaymentTimeoutSecs > 0 {
		timeout = time.Duration(cfg.X402.PaymentTimeoutSecs) * time.Second
	}
	resource := strings.TrimSpace(cfg.X402.Resource)
	description := strings.TrimSpace(cfg.Metadata.Description)
	if description == "" {
		description = defaultRouteDescription
	}
	middleware := x402nethttp.X402Payment(x402nethttp.Config{
		Routes: x402http.RoutesConfig{
			"*": x402http.RouteConfig{
				Accepts: []x402http.PaymentOption{
					{
						Scheme:            types.X402SchemeExact,
						PayTo:             payTo,
						Price:             price,
						Network:           foundationx402.Network(network),
						MaxTimeoutSeconds: cfg.X402.MaxTimeoutSeconds,
					},
				},
				Resource:    resource,
				Description: description,
				MimeType:    strings.TrimSpace(cfg.X402.MimeType),
			},
		},
		Facilitator: x402http.NewHTTPFacilitatorClient(&x402http.FacilitatorConfig{
			URL: facilitatorURL,
		}),
		Schemes: []x402nethttp.SchemeConfig{
			{
				Network: foundationx402.Network(network),
				Server:  evmserver.NewExactEvmScheme(),
			},
		},
		PaywallConfig: &x402http.PaywallConfig{
			AppName: strings.TrimSpace(cfg.TunnelIdentity.Name),
			AppLogo: strings.TrimSpace(cfg.Metadata.Thumbnail),
			Testnet: isTestnetNetwork(network),
		},
		SyncFacilitatorOnStart: true,
		Timeout:                timeout,
	})
	return middleware(next), nil
}
