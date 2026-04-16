package portal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/gosuda/portal-tunnel/v2/portal/overlay"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

var (
	errHopRouteConflict = errors.New("hop route conflict")
	errHopRouteNotFound = errors.New("hop route not found")
)

type hopRoute struct {
	matchHostname      string
	matchToken         string
	forwardOverlayIPv4 string
	forwardToken       string
	identityKey        string
	expiresAt          time.Time
}

type hopManager struct {
	mux        *overlay.HopMux
	registry   *leaseRegistry
	proxy      *proxy
	routesByID map[string]hopRoute
	mu         sync.RWMutex
}

func newHopManager(ov *overlay.Overlay, registry *leaseRegistry, relayProxy *proxy) (*hopManager, error) {
	mux, err := overlay.NewHopMux(ov)
	if err != nil {
		return nil, err
	}
	return &hopManager{
		mux:        mux,
		registry:   registry,
		proxy:      relayProxy,
		routesByID: make(map[string]hopRoute),
	}, nil
}

func (h *hopManager) install(record *leaseRecord) error {
	if record.isDirect() {
		return nil
	}
	if h == nil || h.mux == nil {
		return errFeatureUnavailable
	}
	if len(record.multiHop) < 2 {
		return errors.New("multi-hop lease path is invalid")
	}

	rawHopID, err := utils.RandomHex(32)
	if err != nil {
		return err
	}
	hopID := "hpr_" + rawHopID
	tokens := make([]string, len(record.multiHop)-1)
	for i := range tokens {
		token, err := utils.RandomHex(32)
		if err != nil {
			return err
		}
		tokens[i] = "hpt_" + token
	}
	record.hopID = hopID

	exitRouteID := fmt.Sprintf("%s_%d", record.hopID, len(record.multiHop)-1)
	if err := h.installRoute(exitRouteID, hopRoute{
		matchToken:  tokens[len(tokens)-1],
		identityKey: record.Key(),
		expiresAt:   record.ExpiresAt,
	}, time.Now()); err != nil {
		return err
	}

	type remoteRoute struct {
		overlayIPv4 string
		routeID     string
	}
	installedRemoteRoutes := make([]remoteRoute, 0, len(record.multiHop)-1)
	rollback := true
	defer func() {
		if !rollback {
			return
		}
		h.deleteRoute(exitRouteID)
		for _, route := range installedRemoteRoutes {
			_ = h.mux.Control(context.Background(), route.overlayIPv4, overlay.HopControl{
				Action: overlay.HopControlDelete,
				Route: overlay.HopRouteSpec{
					RouteID: route.routeID,
				},
			})
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), defaultClaimTimeout)
	defer cancel()

	for i := len(record.multiHop) - 2; i >= 0; i-- {
		current := record.multiHop[i]
		next := record.multiHop[i+1]
		routeID := fmt.Sprintf("%s_%d", record.hopID, i)
		route := overlay.HopRouteSpec{
			RouteID:            routeID,
			ForwardOverlayIPv4: next.OverlayIPv4,
			ForwardToken:       tokens[i],
			ExpiresAt:          record.ExpiresAt,
		}
		if i == 0 {
			route.MatchHostname = record.Hostname
		} else {
			route.MatchToken = tokens[i-1]
		}
		if err := h.mux.Control(ctx, current.OverlayIPv4, overlay.HopControl{
			Action: overlay.HopControlInstall,
			Route:  route,
		}); err != nil {
			return fmt.Errorf("install hop %d route: %w", i, err)
		}
		installedRemoteRoutes = append(installedRemoteRoutes, remoteRoute{
			overlayIPv4: current.OverlayIPv4,
			routeID:     routeID,
		})
	}

	rollback = false
	return nil
}

func (h *hopManager) renew(record *leaseRecord) error {
	if record.isDirect() {
		return nil
	}
	if h == nil || h.mux == nil {
		return errFeatureUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultClaimTimeout)
	defer cancel()

	if len(record.multiHop) < 2 || record.hopID == "" {
		return errors.New("multi-hop lease path is invalid")
	}
	if err := h.renewRoute(fmt.Sprintf("%s_%d", record.hopID, len(record.multiHop)-1), record.ExpiresAt, time.Now()); err != nil {
		return err
	}
	for i := len(record.multiHop) - 2; i >= 0; i-- {
		if err := h.mux.Control(ctx, record.multiHop[i].OverlayIPv4, overlay.HopControl{
			Action: overlay.HopControlRenew,
			Route: overlay.HopRouteSpec{
				RouteID:   fmt.Sprintf("%s_%d", record.hopID, i),
				ExpiresAt: record.ExpiresAt,
			},
		}); err != nil {
			return fmt.Errorf("renew hop %d route: %w", i, err)
		}
	}
	return nil
}

func (h *hopManager) delete(record *leaseRecord) {
	if record == nil || record.isDirect() {
		return
	}
	if h == nil || h.mux == nil {
		return
	}
	h.deleteRoute(fmt.Sprintf("%s_%d", record.hopID, len(record.multiHop)-1))
	ctx, cancel := context.WithTimeout(context.Background(), defaultClaimTimeout)
	defer cancel()
	for i := 0; i < len(record.multiHop)-1; i++ {
		_ = h.mux.Control(ctx, record.multiHop[i].OverlayIPv4, overlay.HopControl{
			Action: overlay.HopControlDelete,
			Route: overlay.HopRouteSpec{
				RouteID: fmt.Sprintf("%s_%d", record.hopID, i),
			},
		})
	}
}

func (h *hopManager) run(ctx context.Context) error {
	if h == nil || h.mux == nil {
		<-ctx.Done()
		return nil
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return h.mux.Serve(groupCtx)
	})
	group.Go(func() error {
		for {
			stream, err := h.mux.Accept(groupCtx)
			if err != nil {
				if groupCtx.Err() != nil {
					return nil
				}
				return err
			}
			if stream.Control != nil {
				_ = stream.Respond(h.handleControl(*stream.Control))
				continue
			}
			go h.handleStream(groupCtx, stream.Conn, stream.Token)
		}
	})
	return group.Wait()
}

func (h *hopManager) handleStream(ctx context.Context, conn net.Conn, token string) {
	if h == nil || h.registry == nil {
		_ = conn.Close()
		return
	}
	route, record, ok := h.routeForToken(token, time.Now())
	if !ok {
		_ = conn.Close()
		return
	}
	if record != nil {
		if record.stream == nil || !h.registry.policy.IsIdentityRoutable(record.Key()) {
			_ = conn.Close()
			return
		}
		claimCtx, cancel := context.WithTimeout(ctx, defaultClaimTimeout)
		session, err := record.stream.Claim(claimCtx)
		cancel()
		if err != nil {
			_ = conn.Close()
			return
		}
		h.proxy.bridge(conn, session, record.Key(), h.registry.policy.BPSManager())
		return
	}
	if route.forwardOverlayIPv4 == "" || route.forwardToken == "" || h.mux == nil {
		_ = conn.Close()
		return
	}
	next, err := h.mux.OpenStream(ctx, route.forwardOverlayIPv4, route.forwardToken)
	if err != nil {
		_ = conn.Close()
		log.Warn().Err(err).Str("next_overlay_ipv4", route.forwardOverlayIPv4).Msg("open next hop stream")
		return
	}
	h.proxy.bridge(conn, next, "", nil)
}

func (h *hopManager) handleControl(control overlay.HopControl) error {
	if h == nil || h.registry == nil {
		return errFeatureUnavailable
	}
	switch strings.TrimSpace(control.Action) {
	case overlay.HopControlInstall:
		return h.installRoute(control.Route.RouteID, hopRoute{
			matchHostname:      control.Route.MatchHostname,
			matchToken:         control.Route.MatchToken,
			forwardOverlayIPv4: control.Route.ForwardOverlayIPv4,
			forwardToken:       control.Route.ForwardToken,
			expiresAt:          control.Route.ExpiresAt,
		}, time.Now())
	case overlay.HopControlRenew:
		return h.renewRoute(control.Route.RouteID, control.Route.ExpiresAt, time.Now())
	case overlay.HopControlDelete:
		h.deleteRoute(control.Route.RouteID)
		return nil
	default:
		return errors.New("unknown hop control action")
	}
}

func (h *hopManager) forwardHostname(ctx context.Context, conn net.Conn, hostname string) bool {
	if h == nil || h.registry == nil || h.mux == nil {
		return false
	}
	route, ok := h.routeForHostname(hostname, time.Now())
	if !ok || route.forwardOverlayIPv4 == "" || route.forwardToken == "" {
		return false
	}
	next, err := h.mux.OpenStream(ctx, route.forwardOverlayIPv4, route.forwardToken)
	if err != nil {
		log.Warn().Err(err).Str("next_overlay_ipv4", route.forwardOverlayIPv4).Msg("open next hop stream")
		return false
	}
	h.proxy.bridge(conn, next, "", nil)
	return true
}

func (h *hopManager) close() error {
	if h == nil || h.mux == nil {
		return nil
	}
	return h.mux.Close()
}

func (h *hopManager) routeForHostname(hostname string, now time.Time) (hopRoute, bool) {
	hostname = utils.NormalizeHostname(hostname)
	if h == nil || hostname == "" {
		return hopRoute{}, false
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, route := range h.routesByID {
		if route.matchHostname == hostname && now.Before(route.expiresAt) {
			return route, true
		}
	}
	return hopRoute{}, false
}

func (h *hopManager) routeForToken(token string, now time.Time) (hopRoute, *leaseRecord, bool) {
	token = strings.TrimSpace(token)
	if h == nil || token == "" {
		return hopRoute{}, nil, false
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, route := range h.routesByID {
		if route.matchToken != token || !now.Before(route.expiresAt) {
			continue
		}
		if route.identityKey == "" {
			return route, nil, true
		}
		record, ok := h.registry.RecordByKey(route.identityKey, now)
		if !ok {
			return hopRoute{}, nil, false
		}
		return route, record, true
	}
	return hopRoute{}, nil, false
}

func (h *hopManager) installRoute(routeID string, route hopRoute, now time.Time) error {
	routeID = strings.TrimSpace(routeID)
	route.matchHostname = utils.NormalizeHostname(route.matchHostname)
	route.matchToken = strings.TrimSpace(route.matchToken)
	route.forwardOverlayIPv4 = strings.TrimSpace(route.forwardOverlayIPv4)
	route.forwardToken = strings.TrimSpace(route.forwardToken)
	route.identityKey = strings.TrimSpace(route.identityKey)
	route.expiresAt = route.expiresAt.UTC()

	switch {
	case h == nil:
		return errFeatureUnavailable
	case routeID == "":
		return errors.New("route id is required")
	case !route.expiresAt.After(now):
		return errors.New("route expiry must be in the future")
	case route.matchHostname == "" && route.matchToken == "":
		return errors.New("hostname or token matcher is required")
	case route.matchHostname != "" && route.matchToken != "":
		return errors.New("hostname and token matchers are mutually exclusive")
	case route.identityKey == "" && route.forwardOverlayIPv4 == "":
		return errors.New("forward overlay ipv4 is required")
	case route.identityKey == "" && route.forwardToken == "":
		return errors.New("forward token is required")
	}
	if route.matchHostname != "" {
		if record, ok := h.registry.Lookup(route.matchHostname); ok && record != nil && now.Before(record.ExpiresAt) {
			return errHostnameConflict
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.routesByID == nil {
		h.routesByID = make(map[string]hopRoute)
	}
	for existingID, existing := range h.routesByID {
		if existingID == routeID {
			continue
		}
		if !now.Before(existing.expiresAt) {
			delete(h.routesByID, existingID)
			continue
		}
		if route.matchHostname != "" && existing.matchHostname == route.matchHostname {
			return errHopRouteConflict
		}
		if route.matchToken != "" && existing.matchToken == route.matchToken {
			return errHopRouteConflict
		}
	}
	h.routesByID[routeID] = route
	return nil
}

func (h *hopManager) renewRoute(routeID string, expiresAt, now time.Time) error {
	routeID = strings.TrimSpace(routeID)
	expiresAt = expiresAt.UTC()
	if routeID == "" {
		return errors.New("route id is required")
	}
	if !expiresAt.After(now) {
		return errors.New("route expiry must be in the future")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	route, ok := h.routesByID[routeID]
	if !ok {
		return errHopRouteNotFound
	}
	route.expiresAt = expiresAt
	h.routesByID[routeID] = route
	return nil
}

func (h *hopManager) deleteRoute(routeID string) {
	routeID = strings.TrimSpace(routeID)
	if h == nil || routeID == "" {
		return
	}

	h.mu.Lock()
	delete(h.routesByID, routeID)
	h.mu.Unlock()
}
