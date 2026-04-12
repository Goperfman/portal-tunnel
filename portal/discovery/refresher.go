package discovery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

const (
	defaultRequestTimeout   = 15 * time.Second
	DiscoveryPollInterval   = 1 * time.Minute
	defaultRecoveryFailures = 3
)

type OverlayRuntime interface {
	DiscoverRelay(context.Context, types.RelayDescriptor) (types.DiscoveryResponse, error)
	Sync([]RelayState) error
}

type Refresher struct {
	relaySet                *RelaySet
	httpClient              *http.Client
	overlay                 OverlayRuntime
	directRecoveryFailures  int
	overlayRecoveryFailures int
}

func NewRefresher(relaySet *RelaySet, rootCAPEM []byte, overlay OverlayRuntime) (*Refresher, error) {
	if relaySet == nil {
		return nil, errors.New("relay set is required")
	}
	var rootCAs *x509.CertPool
	if len(rootCAPEM) > 0 {
		rootCAs = x509.NewCertPool()
		if !rootCAs.AppendCertsFromPEM(rootCAPEM) {
			return nil, errors.New("failed to parse relay root ca")
		}
	}
	return &Refresher{
		relaySet: relaySet,
		httpClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					RootCAs:    rootCAs,
					NextProtos: []string{"http/1.1"},
				},
				ForceAttemptHTTP2: false,
			},
			Timeout: defaultRequestTimeout,
		},
		overlay:                 overlay,
		directRecoveryFailures:  defaultRecoveryFailures,
		overlayRecoveryFailures: defaultRecoveryFailures,
	}, nil
}

func (r *Refresher) Refresh(ctx context.Context) error {
	if err := r.refreshHTTPS(ctx); err != nil {
		return err
	}
	if r.overlay == nil {
		return ctx.Err()
	}
	if err := r.overlay.Sync(r.relaySet.OverlayPeerStates()); err != nil {
		log.Warn().
			Err(err).
			Msg("sync wireguard peers")
		return ctx.Err()
	}
	return r.refreshOverlay(ctx)
}

func (r *Refresher) refreshHTTPS(ctx context.Context) error {
	r.relaySet.mu.RLock()
	states := r.relaySet.relayStatesLocked()
	r.relaySet.mu.RUnlock()

	now := time.Now().UTC()
	for _, state := range states {
		if !state.discoverable(now) || !state.Bootstrap {
			continue
		}
		relay := state.Descriptor
		baseURL, err := url.Parse(relay.APIHTTPSAddr)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if utils.IsLocalRelayHost(baseURL.Hostname()) {
			log.Info().
				Str("relay", relay.APIHTTPSAddr).
				Msg("skip loopback relay as discovery source")
			continue
		}
		var resp types.DiscoveryResponse
		if err := utils.HTTPDoAPIPath(ctx, r.httpClient, baseURL, http.MethodGet, types.PathDiscovery, nil, nil, &resp); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		now := time.Now().UTC()
		_, err = r.relaySet.ApplyRelayDiscoveryResponse(relay.Identity, relay.APIHTTPSAddr, resp, now)
		if err != nil {
			continue
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.relaySet.mu.RLock()
	states = r.relaySet.relayStatesLocked()
	r.relaySet.mu.RUnlock()

	now = time.Now().UTC()
	for _, state := range states {
		if !state.discoverable(now) || state.Bootstrap {
			continue
		}
		if r.overlay != nil && state.Descriptor.SupportsOverlayPeer {
			continue
		}
		relay := state.Descriptor
		baseURL, err := url.Parse(relay.APIHTTPSAddr)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.logDirectDiscoveryFailure(relay, fmt.Errorf("parse discovery base url: %w", err), r.directRecoveryFailures)
			continue
		}
		if utils.IsLocalRelayHost(baseURL.Hostname()) {
			log.Info().
				Str("relay", relay.APIHTTPSAddr).
				Msg("skip loopback relay as discovery source")
			continue
		}
		var resp types.DiscoveryResponse
		if err := utils.HTTPDoAPIPath(ctx, r.httpClient, baseURL, http.MethodGet, types.PathDiscovery, nil, nil, &resp); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.logDirectDiscoveryFailure(relay, err, r.directRecoveryFailures)
			continue
		}

		now := time.Now().UTC()
		_, err = r.relaySet.ApplyRelayDiscoveryResponse(relay.Identity, relay.APIHTTPSAddr, resp, now)
		if err != nil {
			r.logDirectDiscoveryFailure(relay, err, r.directRecoveryFailures)
			continue
		}
	}
	return ctx.Err()
}

func (r *Refresher) refreshOverlay(ctx context.Context) error {
	r.relaySet.mu.RLock()
	states := r.relaySet.relayStatesLocked()
	r.relaySet.mu.RUnlock()

	now := time.Now().UTC()
	for _, state := range states {
		if !state.discoverable(now) || state.Bootstrap || !state.Descriptor.SupportsOverlayPeer {
			continue
		}
		relay := state.Descriptor
		var failureErr error

		resp, err := r.overlay.DiscoverRelay(ctx, relay)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failureErr = err
		} else {
			now := time.Now().UTC()
			relaySetChanged, err := r.relaySet.ApplyRelayDiscoveryResponse(relay.Identity, relay.APIHTTPSAddr, resp, now)
			if relaySetChanged {
				if syncErr := r.overlay.Sync(r.relaySet.OverlayPeerStates()); syncErr != nil {
					log.Warn().
						Err(syncErr).
						Str("relay", relay.APIHTTPSAddr).
						Msg("sync wireguard peers")
				}
			}
			if err != nil {
				failureErr = err
			} else {
				continue
			}
		}

		expired, expireReason, consecutiveFailures := r.relaySet.RecordDiscoveryFailure(relay.Identity, relay.APIHTTPSAddr, failureErr, r.overlayRecoveryFailures)
		if expired {
			if syncErr := r.overlay.Sync(r.relaySet.OverlayPeerStates()); syncErr != nil && failureErr == nil {
				failureErr = syncErr
			}
		}

		event := log.Warn().
			Err(failureErr).
			Str("relay", relay.APIHTTPSAddr)
		if expired {
			event = event.
				Bool("expired", true).
				Str("reason", expireReason)
			if consecutiveFailures > 0 {
				event = event.Int("consecutive_failures", consecutiveFailures)
			}
		}
		event.Msg("overlay relay discovery failed")
	}
	return ctx.Err()
}

func (r *Refresher) logDirectDiscoveryFailure(relay types.RelayDescriptor, err error, recoveryFailures int) {
	expired, expireReason, consecutiveFailures := r.relaySet.RecordDiscoveryFailure(relay.Identity, relay.APIHTTPSAddr, err, recoveryFailures)
	if !expired {
		return
	}

	event := log.Warn().
		Err(err).
		Str("relay", relay.APIHTTPSAddr).
		Bool("expired", true).
		Str("reason", expireReason)
	if consecutiveFailures > 0 {
		event = event.Int("consecutive_failures", consecutiveFailures)
	}
	event.Msg("direct relay discovery expired")
}
