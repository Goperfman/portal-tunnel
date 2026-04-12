package discovery

import (
	"errors"
	"time"

	"github.com/gosuda/portal-tunnel/v2/types"
)

const (
	DiscoveryPollInterval = 1 * time.Minute
)

func RequireOverlayRelayDescriptor(desc types.RelayDescriptor) error {
	if !desc.SupportsOverlayPeer {
		return errors.New("descriptor does not support overlay peer")
	}
	if desc.WireGuardPublicKey == "" {
		return errors.New("descriptor wireguard public key is required")
	}
	if desc.WireGuardEndpoint == "" {
		return errors.New("descriptor wireguard endpoint is required")
	}
	if desc.OverlayIPv4 == "" {
		return errors.New("descriptor overlay ipv4 is required")
	}
	return nil
}
