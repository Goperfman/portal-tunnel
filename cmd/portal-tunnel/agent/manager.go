package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal-tunnel/v2/sdk"
	"github.com/gosuda/portal-tunnel/v2/types"
)

const (
	tunnelStateStarting   = "starting"
	tunnelStateRunning    = "running"
	tunnelStateRestarting = "restarting"
	tunnelStateStopped    = "stopped"
	tunnelStateError      = "error"
)

type manager struct {
	startedAt   time.Time
	controlAddr string

	mu      sync.RWMutex
	tunnels map[string]*managedTunnel
	logs    []types.AgentLogEntry
	rootCtx context.Context
}

func newManager(cfg Config, controlAddr string) *manager {
	restartDelay, _ := time.ParseDuration(cfg.Agent.RestartDelay)
	if restartDelay <= 0 {
		restartDelay = 5 * time.Second
	}
	manager := &manager{
		startedAt:   time.Now().UTC(),
		controlAddr: controlAddr,
		tunnels:     make(map[string]*managedTunnel, len(cfg.Tunnels)),
	}
	for _, tunnelCfg := range cfg.Tunnels {
		manager.tunnels[tunnelCfg.ID] = newTunnel(tunnelCfg, restartDelay, manager.appendLog)
	}
	return manager
}

func (m *manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.rootCtx = ctx
	m.mu.Unlock()

	m.mu.RLock()
	tunnels := make([]*managedTunnel, 0, len(m.tunnels))
	for _, tunnel := range m.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	m.mu.RUnlock()

	for _, tunnel := range tunnels {
		tunnel.Start(ctx)
	}
}

func (m *manager) Stop(ctx context.Context) error {
	m.mu.RLock()
	tunnels := make([]*managedTunnel, 0, len(m.tunnels))
	for _, tunnel := range m.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	wg.Add(len(tunnels))
	for _, tunnel := range tunnels {
		go func(t *managedTunnel) {
			defer wg.Done()
			if err := t.Stop(ctx); err != nil {
				t.mu.RLock()
				tunnelID := t.cfg.ID
				t.mu.RUnlock()
				log.Warn().Err(err).Str("tunnel_id", tunnelID).Msg("stop tunnel")
			}
		}(tunnel)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *manager) RestartTunnel(id string) error {
	id = strings.TrimSpace(id)
	m.mu.RLock()
	tunnel := m.tunnels[id]
	m.mu.RUnlock()
	if tunnel == nil {
		return fmt.Errorf("unknown tunnel %q", id)
	}
	tunnel.Restart()
	return nil
}

func (m *manager) AddRelay(id, relayURL string) error {
	id = strings.TrimSpace(id)
	m.mu.RLock()
	tunnel := m.tunnels[id]
	m.mu.RUnlock()
	if tunnel == nil {
		return fmt.Errorf("unknown tunnel %q", id)
	}
	return tunnel.AddRelay(relayURL)
}

func (m *manager) RemoveRelay(id, relayURL string) error {
	id = strings.TrimSpace(id)
	m.mu.RLock()
	tunnel := m.tunnels[id]
	m.mu.RUnlock()
	if tunnel == nil {
		return fmt.Errorf("unknown tunnel %q", id)
	}
	return tunnel.RemoveRelay(relayURL)
}

func (m *manager) SetMultiHop(id string, relayURLs []string) error {
	id = strings.TrimSpace(id)
	m.mu.RLock()
	tunnel := m.tunnels[id]
	m.mu.RUnlock()
	if tunnel == nil {
		return fmt.Errorf("unknown tunnel %q", id)
	}
	return tunnel.SetMultiHop(relayURLs)
}

func (m *manager) Reload(cfg Config) error {
	m.mu.Lock()
	rootCtx := m.rootCtx
	restartDelay, _ := time.ParseDuration(cfg.Agent.RestartDelay)
	if restartDelay <= 0 {
		restartDelay = 5 * time.Second
	}
	next := make(map[string]TunnelConfig, len(cfg.Tunnels))
	for _, tunnelCfg := range cfg.Tunnels {
		next[tunnelCfg.ID] = tunnelCfg
	}
	toStop := make([]*managedTunnel, 0)
	toStart := make([]*managedTunnel, 0)
	toRestart := make([]*managedTunnel, 0)
	for id, tunnel := range m.tunnels {
		tunnelCfg, ok := next[id]
		if !ok {
			toStop = append(toStop, tunnel)
			delete(m.tunnels, id)
			continue
		}
		tunnel.mu.Lock()
		tunnel.restartDelay = restartDelay
		if !reflect.DeepEqual(tunnel.cfg, tunnelCfg) {
			tunnel.cfg = tunnelCfg
			tunnel.updatedAt = time.Now().UTC()
			toRestart = append(toRestart, tunnel)
		}
		tunnel.mu.Unlock()
		delete(next, id)
	}
	for _, tunnelCfg := range next {
		tunnel := newTunnel(tunnelCfg, restartDelay, m.appendLog)
		m.tunnels[tunnelCfg.ID] = tunnel
		toStart = append(toStart, tunnel)
	}
	m.mu.Unlock()

	for _, tunnel := range toStop {
		_ = tunnel.Stop(context.Background())
	}
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	for _, tunnel := range toStart {
		tunnel.Start(rootCtx)
	}
	for _, tunnel := range toRestart {
		tunnel.Restart()
	}
	return nil
}

func (m *manager) Snapshot() types.AgentStatusResponse {
	m.mu.RLock()
	tunnels := make([]*managedTunnel, 0, len(m.tunnels))
	for _, tunnel := range m.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	logs := append([]types.AgentLogEntry(nil), m.logs...)
	m.mu.RUnlock()

	statuses := make([]types.AgentTunnelStatus, 0, len(tunnels))
	summary := types.AgentMetricsSummary{TunnelCount: len(tunnels)}
	for _, tunnel := range tunnels {
		status := tunnel.Snapshot()
		switch status.State {
		case tunnelStateRunning:
			summary.RunningCount++
		case tunnelStateError:
			summary.ErrorCount++
		}
		statuses = append(statuses, status)
	}
	slices.SortFunc(statuses, func(a, b types.AgentTunnelStatus) int {
		return strings.Compare(a.ID, b.ID)
	})

	return types.AgentStatusResponse{
		ReleaseVersion: types.ReleaseVersion,
		StartedAt:      m.startedAt,
		ControlAddr:    m.controlAddr,
		Tunnels:        statuses,
		Logs:           logs,
		Summary:        summary,
	}
}

func (m *manager) appendLog(entry types.AgentLogEntry) {
	entry.Time = time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, entry)
	if len(m.logs) > 200 {
		copy(m.logs, m.logs[len(m.logs)-200:])
		m.logs = m.logs[:200]
	}
}

type managedTunnel struct {
	mu           sync.RWMutex
	cfg          TunnelConfig
	restartDelay time.Duration
	appendLog    func(types.AgentLogEntry)

	stopCancel context.CancelFunc
	runCancel  context.CancelFunc
	done       chan struct{}
	exposure   *sdk.Exposure

	state     string
	lastError string
	startedAt time.Time
	updatedAt time.Time
	restarts  int
}

func newTunnel(cfg TunnelConfig, restartDelay time.Duration, appendLog func(types.AgentLogEntry)) *managedTunnel {
	now := time.Now().UTC()
	return &managedTunnel{
		cfg:          cfg,
		restartDelay: restartDelay,
		appendLog:    appendLog,
		state:        tunnelStateStopped,
		updatedAt:    now,
	}
}

func (t *managedTunnel) Start(parent context.Context) {
	t.mu.Lock()
	if t.done != nil {
		t.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	t.stopCancel = cancel
	t.done = make(chan struct{})
	done := t.done
	t.mu.Unlock()

	go func() {
		defer close(done)
		t.runLoop(ctx)
	}()
}

func (t *managedTunnel) Stop(ctx context.Context) error {
	t.mu.Lock()
	stopCancel := t.stopCancel
	runCancel := t.runCancel
	done := t.done
	t.stopCancel = nil
	t.runCancel = nil
	t.done = nil
	if runCancel != nil {
		runCancel()
	}
	if stopCancel != nil {
		stopCancel()
	}
	t.mu.Unlock()

	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *managedTunnel) Restart() {
	t.mu.Lock()
	if t.runCancel != nil {
		t.state = tunnelStateRestarting
		t.updatedAt = time.Now().UTC()
		t.runCancel()
	}
	t.mu.Unlock()
}

func (t *managedTunnel) AddRelay(relayURL string) error {
	t.mu.RLock()
	id := t.cfg.ID
	exposure := t.exposure
	t.mu.RUnlock()
	if exposure == nil {
		return fmt.Errorf("tunnel %q is not running", id)
	}
	if err := exposure.AddRelay(relayURL); err != nil {
		return err
	}
	t.appendLog(types.AgentLogEntry{TunnelID: id, Level: "info", Message: "relay added"})
	return nil
}

func (t *managedTunnel) RemoveRelay(relayURL string) error {
	t.mu.RLock()
	id := t.cfg.ID
	exposure := t.exposure
	t.mu.RUnlock()
	if exposure == nil {
		return fmt.Errorf("tunnel %q is not running", id)
	}
	if err := exposure.RemoveRelay(relayURL); err != nil {
		return err
	}
	t.appendLog(types.AgentLogEntry{TunnelID: id, Level: "info", Message: "relay removed"})
	return nil
}

func (t *managedTunnel) SetMultiHop(relayURLs []string) error {
	t.mu.RLock()
	id := t.cfg.ID
	exposure := t.exposure
	t.mu.RUnlock()
	if exposure == nil {
		return fmt.Errorf("tunnel %q is not running", id)
	}
	if err := exposure.SetMultiHop(relayURLs); err != nil {
		return err
	}
	message := "multi-hop cleared"
	if len(relayURLs) > 0 {
		message = "multi-hop updated"
	}
	t.appendLog(types.AgentLogEntry{TunnelID: id, Level: "info", Message: message})
	return nil
}

func (t *managedTunnel) Snapshot() types.AgentTunnelStatus {
	t.mu.RLock()
	cfg := t.cfg
	state := t.state
	lastError := t.lastError
	startedAt := t.startedAt
	updatedAt := t.updatedAt
	restarts := t.restarts
	exposure := t.exposure
	t.mu.RUnlock()

	status := types.AgentTunnelStatus{
		ID:         cfg.ID,
		Name:       cfg.Name,
		State:      state,
		TargetAddr: cfg.TargetAddr,
		UDPAddr:    cfg.UDPAddr,
		LastError:  lastError,
		StartedAt:  startedAt,
		UpdatedAt:  updatedAt,
		Restarts:   restarts,
	}
	if exposure == nil {
		return status
	}
	snapshot := exposure.Snapshot()
	status.TargetAddr = snapshot.TargetAddr
	status.UDPAddr = snapshot.UDPAddr
	status.MultiHop = append([]string(nil), snapshot.MultiHop...)
	status.Relays = append([]types.AgentRelayStatus(nil), snapshot.Relays...)
	for _, relay := range status.Relays {
		if relay.PublicURL != "" {
			status.PublicURLs = append(status.PublicURLs, relay.PublicURL)
		}
	}
	return status
}

func (t *managedTunnel) runLoop(ctx context.Context) {
	stop := func() {
		t.mu.Lock()
		t.state = tunnelStateStopped
		t.updatedAt = time.Now().UTC()
		t.exposure = nil
		tunnelID := t.cfg.ID
		t.mu.Unlock()
		t.appendLog(types.AgentLogEntry{TunnelID: tunnelID, Level: "info", Message: "tunnel stopped"})
	}

	for {
		if ctx.Err() != nil {
			stop()
			return
		}
		runCtx, runCancel := context.WithCancel(ctx)
		t.mu.Lock()
		t.runCancel = runCancel
		t.mu.Unlock()
		err := t.runOnce(runCtx)
		t.mu.Lock()
		t.runCancel = nil
		t.exposure = nil
		t.mu.Unlock()
		if ctx.Err() != nil {
			stop()
			return
		}

		level := "error"
		t.mu.Lock()
		delay := t.restartDelay
		message := fmt.Sprintf("tunnel stopped; restarting in %s", delay)
		t.restarts++
		t.state = tunnelStateError
		t.lastError = ""
		if errors.Is(err, context.Canceled) {
			t.state = tunnelStateRestarting
			delay = 100 * time.Millisecond
			level = "info"
			message = "tunnel restarting"
		} else if err != nil {
			t.lastError = err.Error()
		}
		t.updatedAt = time.Now().UTC()
		tunnelID := t.cfg.ID
		t.mu.Unlock()

		t.appendLog(types.AgentLogEntry{TunnelID: tunnelID, Level: level, Message: message})
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			stop()
			return
		case <-timer.C:
		}
	}
}

func (t *managedTunnel) runOnce(ctx context.Context) error {
	t.mu.Lock()
	cfg := t.cfg
	t.state = tunnelStateStarting
	t.lastError = ""
	t.updatedAt = time.Now().UTC()
	t.mu.Unlock()

	discovery := true
	if cfg.Discovery != nil {
		discovery = *cfg.Discovery
	}
	banMITM := true
	if cfg.BanMITM != nil {
		banMITM = *cfg.BanMITM
	}
	exposure, err := sdk.Expose(ctx, sdk.ExposeConfig{
		RelayURLs:       append([]string(nil), cfg.RelayURLs...),
		Discovery:       discovery,
		IdentityPath:    cfg.IdentityPath,
		IdentityJSON:    cfg.IdentityJSON,
		Name:            cfg.Name,
		TargetAddr:      cfg.TargetAddr,
		UDPAddr:         cfg.UDPAddr,
		UDPEnabled:      cfg.UDPEnabled,
		TCPEnabled:      cfg.TCPEnabled,
		MultiHop:        append([]string(nil), cfg.MultiHop...),
		MultiHopDepth:   cfg.MultiHopDepth,
		BanMITM:         banMITM,
		MaxActiveRelays: cfg.MaxActiveRelays,
		Metadata: types.LeaseMetadata{
			Description: cfg.Description,
			Tags:        append([]string(nil), cfg.Tags...),
			Owner:       cfg.Owner,
			Thumbnail:   cfg.Thumbnail,
			Hide:        cfg.Hide,
		},
	})
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.exposure = exposure
	t.state = tunnelStateRunning
	t.lastError = ""
	t.startedAt = time.Now().UTC()
	t.updatedAt = t.startedAt
	tunnelID := t.cfg.ID
	t.mu.Unlock()

	t.appendLog(types.AgentLogEntry{TunnelID: tunnelID, Level: "info", Message: "tunnel started"})
	defer exposure.Close()

	if len(cfg.HTTPRoutes) > 0 {
		routes := make([]sdk.HTTPRoute, 0, len(cfg.HTTPRoutes))
		for _, route := range cfg.HTTPRoutes {
			routes = append(routes, sdk.HTTPRoute{
				Prefix:   route.Prefix,
				Upstream: route.Upstream,
			})
		}
		err = exposure.RunHTTPRoutes(ctx, routes, "")
	} else {
		err = sdk.ProxyExposure(ctx, exposure)
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return ctx.Err()
	}
	return err
}
