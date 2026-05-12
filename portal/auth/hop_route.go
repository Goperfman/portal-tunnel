package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

var ErrHopRouteSignatureInvalid = errors.New("hop route signature is invalid")

func SignHopRoute(method string, route types.HopRoute, identity types.Identity, expiresAt time.Time) (types.HopRoute, error) {
	route.ExpiresAt = expiresAt.UTC()
	route.Signature = ""
	route.OwnerPublicKey = identity.PublicKey

	route, err := normalizeHopRoute(route, true)
	if err != nil {
		return types.HopRoute{}, err
	}
	if identity.PrivateKey == "" || identity.PublicKey == "" {
		return types.HopRoute{}, errors.New("hop route owner identity is required")
	}

	payload, err := types.HopRouteBytes(method, route)
	if err != nil {
		return types.HopRoute{}, err
	}
	route.Signature, err = utils.SignSHA256Secp256k1DER(payload, identity.PrivateKey)
	if err != nil {
		return types.HopRoute{}, err
	}
	return route, nil
}

func VerifyHopRoute(method string, route types.HopRoute) (types.HopRoute, error) {
	signature := route.Signature
	route.Signature = ""

	route, err := normalizeHopRoute(route, true)
	if err != nil {
		return types.HopRoute{}, err
	}
	payload, err := types.HopRouteBytes(method, route)
	if err != nil {
		return types.HopRoute{}, err
	}
	if err := utils.VerifySHA256Secp256k1DER(payload, route.OwnerPublicKey, signature); err != nil {
		return types.HopRoute{}, ErrHopRouteSignatureInvalid
	}
	route.Signature = signature
	return route, nil
}

func normalizeHopRoute(route types.HopRoute, requireOwner bool) (types.HopRoute, error) {
	if route.OwnerPublicKey != "" {
		if _, err := utils.ParseSecp256k1PublicKeyHex(route.OwnerPublicKey); err != nil {
			return types.HopRoute{}, fmt.Errorf("hop route owner public key: %w", err)
		}
	} else if requireOwner {
		return types.HopRoute{}, errors.New("hop route owner public key is required")
	}

	relayURL, err := utils.NormalizeRelayURL(route.RelayURL)
	if err != nil {
		return types.HopRoute{}, fmt.Errorf("hop relay url: %w", err)
	}

	route.RelayURL = relayURL
	route.PublicHostname = utils.NormalizeHostname(route.PublicHostname)
	route.RouteHostname = utils.NormalizeHostname(route.RouteHostname)
	route.ExpiresAt = route.ExpiresAt.UTC()
	return route, nil
}
