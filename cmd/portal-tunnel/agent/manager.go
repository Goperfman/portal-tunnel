package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal-tunnel/v2/sdk"
	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

const managedTunnelRetryInterval = 30 * time.Second

type manager struct {
	controlAddr string

	configMu sync.Mutex

	mu      sync.RWMutex
	cfg     Config
	tunnels map[string]*managedTunnel
	rootCtx context.Context
}

func newManager(cfg Config, controlAddr string) *manager {
	manager := &manager{
		controlAddr: controlAddr,
		cfg:         cfg,
		tunnels:     make(map[string]*managedTunnel, len(cfg.Tunnels)),
	}
	for _, tunnelCfg := range cfg.Tunnels {
		manager.tunnels[tunnelCfg.ID] = newTunnel(tunnelCfg)
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

func (m *manager) AddRelay(id, relayURL string) error {
	relayURL, err := utils.NormalizeRelayURL(relayURL)
	if err != nil {
		return err
	}
	return m.updateTunnelConfig(id, func(tunnel *TunnelConfig) error {
		relayURLs, err := utils.MergeRelayURLs(tunnel.RelayURLs, nil, []string{relayURL})
		if err != nil {
			return err
		}
		tunnel.RelayURLs = relayURLs
		tunnel.SeedRelayURLs = utils.RemoveRelayURL(tunnel.SeedRelayURLs, relayURL)
		return nil
	})
}

func (m *manager) RemoveRelay(id, relayURL string) error {
	relayURL, err := utils.NormalizeRelayURL(relayURL)
	if err != nil {
		return err
	}
	return m.updateTunnelConfig(id, func(tunnel *TunnelConfig) error {
		relayURLs, err := utils.NormalizeRelayURLs(tunnel.RelayURLs...)
		if err != nil {
			return err
		}
		tunnel.RelayURLs = utils.RemoveRelayURL(relayURLs, relayURL)
		tunnel.SeedRelayURLs = utils.RemoveRelayURL(tunnel.SeedRelayURLs, relayURL)
		return removeRelayFromTunnelRoute(tunnel, relayURL)
	})
}

func (m *manager) SeedRelay(id, relayURL string) error {
	relayURL, err := utils.NormalizeRelayURL(relayURL)
	if err != nil {
		return err
	}
	return m.updateTunnelConfig(id, func(tunnel *TunnelConfig) error {
		relayURLs, err := utils.NormalizeRelayURLs(tunnel.RelayURLs...)
		if err != nil {
			return err
		}
		tunnel.RelayURLs = utils.RemoveRelayURL(relayURLs, relayURL)
		seedRelayURLs, err := utils.MergeRelayURLs(tunnel.SeedRelayURLs, nil, []string{relayURL})
		if err != nil {
			return err
		}
		tunnel.SeedRelayURLs = seedRelayURLs
		return removeRelayFromTunnelRoute(tunnel, relayURL)
	})
}

func (m *manager) SetMultiHop(id string, relayURLs []string) error {
	multiHop, err := utils.NormalizeRelayURLs(relayURLs...)
	if err != nil {
		return fmt.Errorf("normalize multi-hop relay url: %w", err)
	}
	if len(multiHop) != len(relayURLs) {
		return errors.New("multi-hop relay url repeated")
	}
	if len(multiHop) == 1 {
		return errors.New("multi-hop requires at least entry and exit relay urls")
	}
	return m.updateTunnelConfig(id, func(tunnel *TunnelConfig) error {
		tunnel.MultiHop = append([]string(nil), multiHop...)
		tunnel.MultiHopDepth = 0
		return nil
	})
}

func (m *manager) AddTunnel(req types.AgentTunnelRequest) error {
	m.configMu.Lock()
	defer m.configMu.Unlock()

	doc, cfg, path, mode, err := m.loadConfigDocument()
	if err != nil {
		return err
	}
	if len(doc.Tunnels) == 1 && strings.TrimSpace(doc.Tunnels[0].IdentityPath) == "" {
		if len(cfg.Tunnels) == 1 {
			doc.Tunnels[0].IdentityPath = cfg.Tunnels[0].IdentityPath
		}
	}
	id := strings.TrimSpace(req.ID)
	name := strings.TrimSpace(req.Name)
	if id == "" {
		id = agentTunnelID(name)
	}
	if id == "" {
		return errors.New("tunnel name is required")
	}
	if err := validateAgentPathComponent("tunnel id", id); err != nil {
		return err
	}
	target := strings.TrimSpace(req.TargetAddr)
	if target == "" {
		target = defaultTargetAddr
	}
	if name == "" {
		name = id
	}
	relayURLs, err := utils.NormalizeRelayURLs(req.RelayURLs...)
	if err != nil {
		return err
	}
	discovery := true
	tunnelCfg := TunnelConfig{
		ID:         id,
		Name:       name,
		TargetAddr: target,
		RelayURLs:  relayURLs,
		Discovery:  &discovery,
	}
	if slices.ContainsFunc(cfg.Tunnels, func(tunnel TunnelConfig) bool { return tunnel.ID == tunnelCfg.ID }) {
		return fmt.Errorf("tunnel %q already exists", tunnelCfg.ID)
	}
	doc.Tunnels = append(doc.Tunnels, tunnelCfg)
	return m.writeConfigAndApply(path, mode, doc)
}

func agentTunnelID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var out strings.Builder
	dash := false
	for _, r := range name {
		if invalidAgentPathComponentRune(r) {
			if out.Len() > 0 && !dash {
				out.WriteByte('-')
				dash = true
			}
			continue
		}
		out.WriteRune(r)
		dash = false
	}
	return strings.Trim(out.String(), "-")
}

func (m *manager) updateTunnelConfig(id string, update func(*TunnelConfig) error) error {
	id = strings.TrimSpace(id)
	if err := validateAgentPathComponent("tunnel id", id); err != nil {
		return err
	}

	m.configMu.Lock()
	defer m.configMu.Unlock()

	doc, cfg, path, mode, err := m.loadConfigDocument()
	if err != nil {
		return err
	}
	index := slices.IndexFunc(cfg.Tunnels, func(tunnel TunnelConfig) bool { return tunnel.ID == id })
	if index < 0 {
		return fmt.Errorf("tunnel %q not found", id)
	}
	before := doc.Tunnels[index]
	if err := update(&doc.Tunnels[index]); err != nil {
		return err
	}
	if reflect.DeepEqual(before, doc.Tunnels[index]) {
		return nil
	}
	return m.writeConfigAndApply(path, mode, doc)
}

func removeRelayFromTunnelRoute(tunnel *TunnelConfig, relayURL string) error {
	if len(tunnel.MultiHop) == 0 {
		return nil
	}
	multiHop, err := utils.NormalizeRelayURLs(tunnel.MultiHop...)
	if err != nil {
		return fmt.Errorf("normalize multi-hop relay url: %w", err)
	}
	nextMultiHop := utils.RemoveRelayURL(multiHop, relayURL)
	if len(nextMultiHop) == len(multiHop) {
		tunnel.MultiHop = nextMultiHop
		return nil
	}
	if len(nextMultiHop) < 2 {
		nextMultiHop = nil
	}
	tunnel.MultiHop = nextMultiHop
	tunnel.MultiHopDepth = 0
	return nil
}

func (m *manager) DeleteTunnel(id string) error {
	m.configMu.Lock()
	defer m.configMu.Unlock()

	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("tunnel id is required")
	}
	doc, cfg, path, mode, err := m.loadConfigDocument()
	if err != nil {
		return err
	}
	if len(doc.Tunnels) <= 1 {
		return errors.New("cannot delete the last tunnel")
	}

	index := slices.IndexFunc(cfg.Tunnels, func(tunnel TunnelConfig) bool { return tunnel.ID == id })
	if index < 0 {
		return fmt.Errorf("tunnel %q not found", id)
	}
	next := doc.Tunnels[:0]
	for i, tunnel := range doc.Tunnels {
		if i == index {
			continue
		}
		next = append(next, tunnel)
	}
	doc.Tunnels = next
	return m.writeConfigAndApply(path, mode, doc)
}

func (m *manager) loadConfigDocument() (configDocument, Config, string, os.FileMode, error) {
	m.mu.RLock()
	configPath := m.cfg.sourcePath
	m.mu.RUnlock()
	doc, path, mode, err := loadConfigDocument(configPath)
	if err != nil {
		return configDocument{}, Config{}, "", 0, err
	}
	cfg, err := resolveConfigDocument(path, doc)
	if err != nil {
		return configDocument{}, Config{}, "", 0, err
	}
	return doc, cfg, path, mode, nil
}

func (m *manager) writeConfigAndApply(path string, mode os.FileMode, doc configDocument) error {
	next, err := resolveConfigDocument(path, doc)
	if err != nil {
		return err
	}
	if err := writeConfigDocument(path, mode, doc); err != nil {
		return err
	}
	return m.ApplyConfig(next)
}

func (m *manager) ApplyConfig(cfg Config) error {
	type liveUpdate struct {
		tunnel          *managedTunnel
		cfg             TunnelConfig
		relayChanged    bool
		multiHopChanged bool
	}

	m.mu.Lock()
	m.cfg = cfg
	rootCtx := m.rootCtx
	next := make(map[string]TunnelConfig, len(cfg.Tunnels))
	for _, tunnelCfg := range cfg.Tunnels {
		next[tunnelCfg.ID] = tunnelCfg
	}
	toStop := make([]*managedTunnel, 0)
	toStart := make([]*managedTunnel, 0)
	toUpdate := make([]*managedTunnel, 0)
	var liveUpdates []liveUpdate
	for id, tunnel := range m.tunnels {
		tunnelCfg, ok := next[id]
		if !ok {
			toStop = append(toStop, tunnel)
			delete(m.tunnels, id)
			continue
		}
		tunnel.mu.Lock()
		previous := tunnel.cfg
		if !reflect.DeepEqual(previous, tunnelCfg) {
			staticPrevious := previous
			staticNext := tunnelCfg
			staticPrevious.RelayURLs = nil
			staticPrevious.SeedRelayURLs = nil
			staticPrevious.MultiHop = nil
			staticNext.RelayURLs = nil
			staticNext.SeedRelayURLs = nil
			staticNext.MultiHop = nil
			if reflect.DeepEqual(staticPrevious, staticNext) {
				tunnel.cfg = tunnelCfg
				liveUpdates = append(liveUpdates, liveUpdate{
					tunnel:          tunnel,
					cfg:             tunnelCfg,
					relayChanged:    !slices.Equal(previous.RelayURLs, tunnelCfg.RelayURLs) || !slices.Equal(previous.SeedRelayURLs, tunnelCfg.SeedRelayURLs),
					multiHopChanged: !slices.Equal(previous.MultiHop, tunnelCfg.MultiHop),
				})
			} else {
				tunnel.cfg = tunnelCfg
				toUpdate = append(toUpdate, tunnel)
			}
		}
		tunnel.mu.Unlock()
		delete(next, id)
	}
	for _, tunnelCfg := range next {
		tunnel := newTunnel(tunnelCfg)
		m.tunnels[tunnelCfg.ID] = tunnel
		toStart = append(toStart, tunnel)
	}
	m.mu.Unlock()

	for _, tunnel := range append(toStop, toUpdate...) {
		_ = tunnel.Stop(context.Background())
	}
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	for _, tunnel := range append(toStart, toUpdate...) {
		tunnel.Start(rootCtx)
	}

	var liveErr error
	for _, update := range liveUpdates {
		liveErr = errors.Join(liveErr, update.tunnel.applyLiveConfig(update.cfg, update.relayChanged, update.multiHopChanged))
	}
	return liveErr
}

func (m *manager) Snapshot() types.AgentStatusResponse {
	m.mu.RLock()
	tunnels := make([]*managedTunnel, 0, len(m.tunnels))
	for _, tunnel := range m.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	m.mu.RUnlock()

	statuses := make([]types.AgentTunnelStatus, 0, len(tunnels))
	for _, tunnel := range tunnels {
		statuses = append(statuses, tunnel.Snapshot())
	}
	slices.SortFunc(statuses, func(a, b types.AgentTunnelStatus) int {
		return strings.Compare(a.ID, b.ID)
	})

	return types.AgentStatusResponse{
		ConfigPath:  m.cfg.sourcePath,
		ControlAddr: m.controlAddr,
		Tunnels:     statuses,
	}
}

type managedTunnel struct {
	mu  sync.RWMutex
	cfg TunnelConfig

	cancel    context.CancelFunc
	done      chan struct{}
	exposure  *sdk.Exposure
	lastError string
}

func newTunnel(cfg TunnelConfig) *managedTunnel {
	return &managedTunnel{cfg: cfg}
}

func (t *managedTunnel) applyLiveConfig(cfg TunnelConfig, relayChanged, multiHopChanged bool) error {
	t.mu.RLock()
	exposure := t.exposure
	t.mu.RUnlock()
	if exposure == nil {
		return nil
	}

	var err error
	if relayChanged {
		err = errors.Join(err, exposure.SetRelayConfig(cfg.RelayURLs, cfg.SeedRelayURLs))
	}
	if multiHopChanged {
		err = errors.Join(err, exposure.SetMultiHop(cfg.MultiHop))
	}

	t.mu.Lock()
	if err == nil {
		t.lastError = ""
	} else {
		t.lastError = err.Error()
	}
	t.mu.Unlock()
	return err
}

func (t *managedTunnel) Start(parent context.Context) {
	t.mu.Lock()
	if t.done != nil {
		t.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	t.cancel = cancel
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
	cancel := t.cancel
	done := t.done
	t.cancel = nil
	t.done = nil
	if cancel != nil {
		cancel()
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

func (t *managedTunnel) Snapshot() types.AgentTunnelStatus {
	t.mu.RLock()
	cfg := t.cfg
	lastError := t.lastError
	exposure := t.exposure
	done := t.done
	t.mu.RUnlock()

	running := false
	if done != nil {
		select {
		case <-done:
		default:
			running = true
		}
	}

	state := "stopped"
	switch {
	case lastError != "":
		state = "error"
	case exposure != nil:
		state = "running"
	case running:
		state = "starting"
	}

	status := types.AgentTunnelStatus{
		ID:         cfg.ID,
		Name:       cfg.Name,
		State:      state,
		TargetAddr: cfg.TargetAddr,
		LastError:  lastError,
	}
	if exposure == nil {
		return status
	}
	snapshot := exposure.Snapshot()
	status.TargetAddr = snapshot.TargetAddr
	status.MultiHop = append([]string(nil), snapshot.MultiHop...)
	status.Relays = append([]types.AgentRelayStatus(nil), snapshot.Relays...)
	return status
}

func (t *managedTunnel) runLoop(ctx context.Context) {
	for {
		err := t.runOnce(ctx)

		t.mu.Lock()
		t.exposure = nil
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || err == nil {
			t.lastError = ""
		} else {
			t.lastError = err.Error()
		}
		t.mu.Unlock()

		if ctx.Err() != nil || errors.Is(err, context.Canceled) || err == nil {
			return
		}
		log.Warn().Err(err).Msg("managed tunnel stopped with error; retrying")
		if !utils.SleepOrDone(ctx, managedTunnelRetryInterval) {
			return
		}
	}
}

func (t *managedTunnel) runOnce(ctx context.Context) error {
	t.mu.Lock()
	cfg := t.cfg
	t.lastError = ""
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
		SeedRelayURLs:   append([]string(nil), cfg.SeedRelayURLs...),
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
	t.lastError = ""
	t.mu.Unlock()

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
