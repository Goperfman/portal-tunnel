package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gosuda/portal-tunnel/v2/types"
)

const agentDashboardRefreshInterval = 2 * time.Second

type agentDashboardTab int

const (
	agentDashboardRelaysTab agentDashboardTab = iota
	agentDashboardMultiHopTab
	agentDashboardLogsTab
)

type agentDashboardClick int

const (
	agentDashboardClickRefresh agentDashboardClick = iota + 1
	agentDashboardClickReload
	agentDashboardClickRestart
	agentDashboardClickQuit
	agentDashboardClickTunnel
	agentDashboardClickTab
	agentDashboardClickRelay
	agentDashboardClickAttachRelay
	agentDashboardClickDetachRelay
	agentDashboardClickAddHop
	agentDashboardClickRemoveHop
	agentDashboardClickApplyHop
	agentDashboardClickClearHop
)

type agentDashboardModel struct {
	configPath string
	stateDir   string

	status  types.AgentStatusResponse
	err     error
	message string

	width  int
	height int

	selectedTunnel   int
	selectedRelay    int
	selectedTunnelID string
	selectedRelayURL string
	tab              agentDashboardTab

	multiHopDraft []string
	draftTunnelID string

	logs viewport.Model
}

type agentDashboardStatusMsg struct {
	status types.AgentStatusResponse
	err    error
}

type agentDashboardActionMsg struct {
	message string
	err     error
}

type agentDashboardTickMsg time.Time

type agentDashboardClickRegion struct {
	x0     int
	x1     int
	y      int
	action agentDashboardClick
	tunnel int
	relay  int
	tab    agentDashboardTab
}

type agentDashboardLayout struct {
	lines   []string
	regions []agentDashboardClickRegion
}

var (
	agentDashboardTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	agentDashboardSectionStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	agentDashboardMutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	agentDashboardSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("25"))
	agentDashboardButtonStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("238"))
	agentDashboardDisabledStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	agentDashboardErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	agentDashboardMessageStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	agentDashboardOKStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("120"))
	agentDashboardHelpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	agentDashboardInputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("120"))
	agentDashboardTabStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	agentDashboardActiveTab     = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("31"))
)

type agentDashboardButton struct {
	label    string
	action   agentDashboardClick
	disabled bool
}

type agentDashboardPane struct {
	lines   []string
	regions []agentDashboardClickRegion
}

func RunDashboard(configPath, stateDir string) error {
	logs := viewport.New(0, 0)
	logs.MouseWheelEnabled = true
	logs.MouseWheelDelta = 3

	_, err := tea.NewProgram(agentDashboardModel{
		configPath: configPath,
		stateDir:   stateDir,
		tab:        agentDashboardRelaysTab,
		logs:       logs,
	}, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func (m agentDashboardModel) Init() tea.Cmd {
	return tea.Batch(agentDashboardFetchStatus(m.stateDir), agentDashboardTick())
}

func (m agentDashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncLogViewport()
		return m, nil
	case agentDashboardTickMsg:
		return m, tea.Batch(agentDashboardFetchStatus(m.stateDir), agentDashboardTick())
	case agentDashboardStatusMsg:
		m.err = msg.err
		if msg.err == nil {
			m.status = msg.status
			m.message = ""
			m.clampSelection()
		}
		m.syncLogViewport()
		return m, nil
	case agentDashboardActionMsg:
		m.err = msg.err
		m.message = msg.message
		if msg.err != nil {
			m.message = msg.err.Error()
		} else if msg.message == "multi-hop applied" || msg.message == "multi-hop cleared" {
			m.multiHopDraft = nil
			m.draftTunnelID = ""
		}
		return m, agentDashboardFetchStatus(m.stateDir)
	case tea.KeyMsg:
		return m.updateKeys(msg)
	case tea.MouseMsg:
		return m.updateMouse(msg)
	default:
		return m, nil
	}
}

func (m agentDashboardModel) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "enter":
		m.message = "refreshing..."
		return m, agentDashboardFetchStatus(m.stateDir)
	case "up", "k":
		if m.selectedTunnel > 0 {
			m.selectedTunnel--
			m.selectedTunnelID = m.status.Tunnels[m.selectedTunnel].ID
			m.selectedRelay = 0
			m.selectedRelayURL = ""
		}
	case "down", "j":
		if m.selectedTunnel+1 < len(m.status.Tunnels) {
			m.selectedTunnel++
			m.selectedTunnelID = m.status.Tunnels[m.selectedTunnel].ID
			m.selectedRelay = 0
			m.selectedRelayURL = ""
		}
	case "left", "h", "shift+tab":
		m.prevTab()
	case "right", "l", "tab":
		m.nextTab()
	}
	return m, nil
}

func (m agentDashboardModel) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	event := tea.MouseEvent(msg)
	switch event.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		if m.tab == agentDashboardLogsTab {
			var cmd tea.Cmd
			m.logs, cmd = m.logs.Update(msg)
			return m, cmd
		}
		if event.Button == tea.MouseButtonWheelUp && m.selectedTunnel > 0 {
			m.selectedTunnel--
			m.selectedTunnelID = m.status.Tunnels[m.selectedTunnel].ID
			m.selectedRelay = 0
			m.selectedRelayURL = ""
		}
		if event.Button == tea.MouseButtonWheelDown && m.selectedTunnel+1 < len(m.status.Tunnels) {
			m.selectedTunnel++
			m.selectedTunnelID = m.status.Tunnels[m.selectedTunnel].ID
			m.selectedRelay = 0
			m.selectedRelayURL = ""
		}
		return m, nil
	}
	if event.Action != tea.MouseActionPress || event.Button != tea.MouseButtonLeft {
		return m, nil
	}
	for _, region := range m.layout().regions {
		if event.Y == region.y && event.X >= region.x0 && event.X < region.x1 {
			return m.applyClick(region)
		}
	}
	return m, nil
}

func (m agentDashboardModel) applyClick(region agentDashboardClickRegion) (tea.Model, tea.Cmd) {
	switch region.action {
	case agentDashboardClickRefresh:
		m.message = "refreshing..."
		return m, agentDashboardFetchStatus(m.stateDir)
	case agentDashboardClickReload:
		m.message = "reloading config..."
		return m, agentDashboardReload(m.stateDir)
	case agentDashboardClickRestart:
		return m.restartSelectedTunnel()
	case agentDashboardClickQuit:
		return m, tea.Quit
	case agentDashboardClickTunnel:
		if region.tunnel >= 0 && region.tunnel < len(m.status.Tunnels) {
			m.selectedTunnel = region.tunnel
			m.selectedTunnelID = m.status.Tunnels[region.tunnel].ID
			m.selectedRelay = 0
			m.selectedRelayURL = ""
		}
	case agentDashboardClickTab:
		m.tab = region.tab
		m.syncLogViewport()
	case agentDashboardClickRelay:
		if tunnel, ok := m.selectedTunnelStatus(); ok && region.relay >= 0 && region.relay < len(tunnel.Relays) {
			m.selectedRelay = region.relay
			m.selectedRelayURL = tunnel.Relays[region.relay].RelayURL
		}
	case agentDashboardClickAttachRelay:
		return m.attachSelectedRelay()
	case agentDashboardClickDetachRelay:
		return m.detachSelectedRelay()
	case agentDashboardClickAddHop:
		return m.addSelectedHop()
	case agentDashboardClickRemoveHop:
		return m.removeSelectedHop()
	case agentDashboardClickApplyHop:
		return m.applyMultiHop()
	case agentDashboardClickClearHop:
		return m.clearMultiHop()
	}
	return m, nil
}

func (m agentDashboardModel) View() string {
	layout := m.layout()
	return strings.Join(layout.lines, "\n") + "\n"
}

func (m *agentDashboardModel) clampSelection() {
	if len(m.status.Tunnels) == 0 {
		m.selectedTunnel = 0
		m.selectedRelay = 0
		m.selectedTunnelID = ""
		m.selectedRelayURL = ""
		return
	}
	if m.selectedTunnelID != "" {
		for i, tunnel := range m.status.Tunnels {
			if tunnel.ID == m.selectedTunnelID {
				m.selectedTunnel = i
				break
			}
		}
	}
	if m.selectedTunnel < 0 {
		m.selectedTunnel = 0
	}
	if m.selectedTunnel >= len(m.status.Tunnels) {
		m.selectedTunnel = len(m.status.Tunnels) - 1
	}
	m.selectedTunnelID = m.status.Tunnels[m.selectedTunnel].ID

	relays := m.status.Tunnels[m.selectedTunnel].Relays
	if len(relays) == 0 {
		m.selectedRelay = 0
		m.selectedRelayURL = ""
		return
	}
	if m.selectedRelayURL != "" {
		for i, relay := range relays {
			if relay.RelayURL == m.selectedRelayURL {
				m.selectedRelay = i
				break
			}
		}
	}
	if m.selectedRelay < 0 {
		m.selectedRelay = 0
	}
	if m.selectedRelay >= len(relays) {
		m.selectedRelay = len(relays) - 1
	}
	m.selectedRelayURL = relays[m.selectedRelay].RelayURL
}

func (m agentDashboardModel) selectedTunnelStatus() (types.AgentTunnelStatus, bool) {
	if m.selectedTunnel < 0 || m.selectedTunnel >= len(m.status.Tunnels) {
		return types.AgentTunnelStatus{}, false
	}
	return m.status.Tunnels[m.selectedTunnel], true
}

func (m agentDashboardModel) selectedRelayStatus() (types.AgentRelayStatus, bool) {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok || m.selectedRelay < 0 || m.selectedRelay >= len(tunnel.Relays) {
		return types.AgentRelayStatus{}, false
	}
	return tunnel.Relays[m.selectedRelay], true
}

func (m *agentDashboardModel) prevTab() {
	if m.tab == agentDashboardRelaysTab {
		m.tab = agentDashboardLogsTab
	} else {
		m.tab--
	}
	m.syncLogViewport()
}

func (m *agentDashboardModel) nextTab() {
	if m.tab == agentDashboardLogsTab {
		m.tab = agentDashboardRelaysTab
	} else {
		m.tab++
	}
	m.syncLogViewport()
}

func (m *agentDashboardModel) syncLogViewport() {
	_, rightWidth, bodyHeight := agentDashboardPaneSizes(m.width, m.height)
	m.logs.Width = rightWidth
	m.logs.Height = max(4, bodyHeight-8)

	wasAtBottom := m.logs.AtBottom()
	m.logs.SetContent(m.logContent())
	if wasAtBottom {
		m.logs.GotoBottom()
	}
}

func (m agentDashboardModel) restartSelectedTunnel() (tea.Model, tea.Cmd) {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		m.message = "no tunnel selected"
		return m, nil
	}
	m.message = "restarting " + tunnel.ID + "..."
	return m, agentDashboardRestart(m.stateDir, tunnel.ID)
}

func (m agentDashboardModel) attachSelectedRelay() (tea.Model, tea.Cmd) {
	tunnel, relay, ok := m.selectedTunnelRelay()
	if !ok {
		m.message = "select a relay first"
		return m, nil
	}
	if relayDashboardAttached(relay) {
		m.message = "relay is already attached"
		return m, nil
	}
	m.message = "attaching relay..."
	return m, agentDashboardAddRelay(m.stateDir, tunnel.ID, relay.RelayURL)
}

func (m agentDashboardModel) detachSelectedRelay() (tea.Model, tea.Cmd) {
	tunnel, relay, ok := m.selectedTunnelRelay()
	if !ok {
		m.message = "select a relay first"
		return m, nil
	}
	if !relayDashboardAttached(relay) {
		m.message = "relay is not attached"
		return m, nil
	}
	m.message = "detaching relay..."
	return m, agentDashboardRemoveRelay(m.stateDir, tunnel.ID, relay.RelayURL)
}

func (m agentDashboardModel) addSelectedHop() (tea.Model, tea.Cmd) {
	tunnel, relay, ok := m.selectedTunnelRelay()
	if !ok {
		m.message = "select a relay first"
		return m, nil
	}
	if !relay.SupportsOverlay {
		m.message = "selected relay does not support multi-hop"
		return m, nil
	}
	m.ensureMultiHopDraft(tunnel)
	if slices.Contains(m.multiHopDraft, relay.RelayURL) {
		m.message = "relay is already in the route"
		return m, nil
	}
	m.multiHopDraft = append(m.multiHopDraft, relay.RelayURL)
	m.message = "route draft updated"
	return m, nil
}

func (m agentDashboardModel) removeSelectedHop() (tea.Model, tea.Cmd) {
	tunnel, relay, ok := m.selectedTunnelRelay()
	if !ok {
		m.message = "select a relay first"
		return m, nil
	}
	m.ensureMultiHopDraft(tunnel)

	next := m.multiHopDraft[:0]
	for _, relayURL := range m.multiHopDraft {
		if relayURL != relay.RelayURL {
			next = append(next, relayURL)
		}
	}
	if len(next) == len(m.multiHopDraft) {
		m.message = "selected relay is not in the route"
		return m, nil
	}
	m.multiHopDraft = next
	m.message = "route draft updated"
	return m, nil
}

func (m agentDashboardModel) applyMultiHop() (tea.Model, tea.Cmd) {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		m.message = "no tunnel selected"
		return m, nil
	}
	route := m.displayedMultiHop(tunnel)
	if len(route) < 2 {
		m.message = "multi-hop requires at least two relays"
		return m, nil
	}
	m.message = "applying route..."
	return m, agentDashboardSetMultiHop(m.stateDir, tunnel.ID, route)
}

func (m agentDashboardModel) clearMultiHop() (tea.Model, tea.Cmd) {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		m.message = "no tunnel selected"
		return m, nil
	}
	m.message = "clearing route..."
	return m, agentDashboardClearMultiHop(m.stateDir, tunnel.ID)
}

func (m agentDashboardModel) selectedTunnelRelay() (types.AgentTunnelStatus, types.AgentRelayStatus, bool) {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		return types.AgentTunnelStatus{}, types.AgentRelayStatus{}, false
	}
	relay, ok := m.selectedRelayStatus()
	if !ok {
		return types.AgentTunnelStatus{}, types.AgentRelayStatus{}, false
	}
	return tunnel, relay, true
}

func (m *agentDashboardModel) ensureMultiHopDraft(tunnel types.AgentTunnelStatus) {
	if m.draftTunnelID == tunnel.ID {
		return
	}
	m.draftTunnelID = tunnel.ID
	m.multiHopDraft = append([]string(nil), tunnel.MultiHop...)
}

func (m agentDashboardModel) displayedMultiHop(tunnel types.AgentTunnelStatus) []string {
	if m.draftTunnelID == tunnel.ID {
		return append([]string(nil), m.multiHopDraft...)
	}
	return append([]string(nil), tunnel.MultiHop...)
}

func agentDashboardFetchStatus(stateDir string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		status, err := Status(ctx, stateDir)
		return agentDashboardStatusMsg{status: status, err: err}
	}
}

func agentDashboardTick() tea.Cmd {
	return tea.Tick(agentDashboardRefreshInterval, func(t time.Time) tea.Msg {
		return agentDashboardTickMsg(t)
	})
}

func agentDashboardReload(stateDir string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := Reload(ctx, stateDir)
		return agentDashboardActionMsg{message: "reload accepted", err: err}
	}
}

func agentDashboardRestart(stateDir, tunnelID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := RestartTunnel(ctx, stateDir, tunnelID)
		return agentDashboardActionMsg{message: "restart accepted", err: err}
	}
}

func agentDashboardAddRelay(stateDir, tunnelID, relayURL string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := AddRelay(ctx, stateDir, tunnelID, relayURL)
		return agentDashboardActionMsg{message: "relay attach accepted", err: err}
	}
}

func agentDashboardRemoveRelay(stateDir, tunnelID, relayURL string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := RemoveRelay(ctx, stateDir, tunnelID, relayURL)
		return agentDashboardActionMsg{message: "relay detach accepted", err: err}
	}
}

func agentDashboardSetMultiHop(stateDir, tunnelID string, relayURLs []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := SetMultiHop(ctx, stateDir, tunnelID, relayURLs)
		return agentDashboardActionMsg{message: "multi-hop applied", err: err}
	}
}

func agentDashboardClearMultiHop(stateDir, tunnelID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := ClearMultiHop(ctx, stateDir, tunnelID)
		return agentDashboardActionMsg{message: "multi-hop cleared", err: err}
	}
}

func (m agentDashboardModel) layout() agentDashboardLayout {
	width := max(m.width, 88)
	leftWidth, rightWidth, bodyHeight := agentDashboardPaneSizes(width, m.height)

	var layout agentDashboardLayout
	layout.addLine(agentDashboardTitleStyle.Render("Portal Agent") + "  " + agentDashboardMutedStyle.Render(agentDashboardSummaryLine(m.status)))
	layout.addLine(agentDashboardMutedStyle.Render(strings.Repeat("-", min(width, 120))))

	if m.err != nil && m.status.ControlAddr == "" {
		layout.addLine(agentDashboardErrorStyle.Render(fmt.Sprintf("Agent unavailable: %v", m.err)))
		layout.addLine("")
		layout.addLine("Start managed service: " + agentDashboardInputStyle.Render("portal agent run --config "+m.configPath))
		layout.addLine("No service manager:   " + agentDashboardInputStyle.Render("portal agent run --foreground --config "+m.configPath))
		layout.addLine("")
		layout.addButtons(
			agentDashboardButton{label: "Refresh", action: agentDashboardClickRefresh},
			agentDashboardButton{label: "Quit", action: agentDashboardClickQuit},
		)
		return layout
	}

	layout.addLine(fmt.Sprintf("Control: %s  Tunnels: %d  Running: %d  Errors: %d  Uptime: %s",
		valueOrDash(m.status.ControlAddr),
		m.status.Summary.TunnelCount,
		m.status.Summary.RunningCount,
		m.status.Summary.ErrorCount,
		durationSince(m.status.StartedAt),
	))
	if m.message != "" {
		layout.addLine(agentDashboardMessageStyle.Render("Message: " + m.message))
	}
	if m.err != nil {
		layout.addLine(agentDashboardErrorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
	}
	layout.addButtons(
		agentDashboardButton{label: "Refresh", action: agentDashboardClickRefresh},
		agentDashboardButton{label: "Reload Config", action: agentDashboardClickReload},
		agentDashboardButton{label: "Quit", action: agentDashboardClickQuit},
	)
	layout.addLine("")

	left := m.renderTunnelsPane(leftWidth, bodyHeight)
	right := m.renderTunnelPane(rightWidth, bodyHeight)
	layout.addPanes(left, right, leftWidth, 2)
	layout.addLine("")
	layout.addLine(agentDashboardHelpStyle.Render("Mouse: select tunnels, relays, tabs, and action buttons. Keyboard fallback: arrows, tab, enter, q."))
	return layout
}

func (m agentDashboardModel) renderTunnelsPane(width, height int) agentDashboardPane {
	var pane agentDashboardPane
	pane.addLine(agentDashboardSectionStyle.Render(agentDashboardFit("Tunnels", width)))
	pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit(fmt.Sprintf("%d managed", len(m.status.Tunnels)), width)))
	pane.addLine(agentDashboardMutedStyle.Render(strings.Repeat("-", width)))

	if len(m.status.Tunnels) == 0 {
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("no managed tunnels", width)))
		return pane
	}

	for i, tunnel := range m.status.Tunnels {
		if len(pane.lines) >= height {
			pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit(fmt.Sprintf("+ %d more", len(m.status.Tunnels)-i), width)))
			break
		}
		name := tunnel.ID
		if strings.TrimSpace(tunnel.Name) != "" {
			name = tunnel.Name
		}
		line := fmt.Sprintf("%-10s %-16s %s",
			truncateDashboardValue(tunnel.State, 10),
			truncateDashboardValue(name, 16),
			firstOrDash(tunnel.PublicURLs),
		)
		pane.addClickRow(line, width, agentDashboardTunnelStyle(i == m.selectedTunnel, tunnel.State), agentDashboardClickTunnel, i, -1)
	}
	return pane
}

func (m agentDashboardModel) renderTunnelPane(width, height int) agentDashboardPane {
	var pane agentDashboardPane
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		pane.addLine(agentDashboardSectionStyle.Render(agentDashboardFit("Tunnel", width)))
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("select a managed tunnel", width)))
		return pane
	}

	title := tunnel.ID
	if strings.TrimSpace(tunnel.Name) != "" {
		title = tunnel.Name + " (" + tunnel.ID + ")"
	}
	pane.addLine(agentDashboardSectionStyle.Render(agentDashboardFit(title, width)))
	pane.addLine(agentDashboardFit(fmt.Sprintf("State: %s  Target: %s  Restarts: %d",
		valueOrDash(tunnel.State),
		valueOrDash(tunnel.TargetAddr),
		tunnel.Restarts,
	), width))
	pane.addLine(agentDashboardFit("Public: "+firstOrDash(tunnel.PublicURLs), width))
	if strings.TrimSpace(tunnel.LastError) != "" {
		pane.addLine(agentDashboardErrorStyle.Render(agentDashboardFit("Error: "+tunnel.LastError, width)))
	}
	pane.addButtons(agentDashboardButton{label: "Restart Tunnel", action: agentDashboardClickRestart})
	pane.addTabs(m.tab)
	pane.addLine(agentDashboardMutedStyle.Render(strings.Repeat("-", width)))

	switch m.tab {
	case agentDashboardMultiHopTab:
		m.renderMultiHopTab(&pane, width, height, tunnel)
	case agentDashboardLogsTab:
		m.renderLogsTab(&pane, width, height)
	default:
		m.renderRelaysTab(&pane, width, height, tunnel)
	}
	return pane
}

func (m agentDashboardModel) renderRelaysTab(pane *agentDashboardPane, width, height int, tunnel types.AgentTunnelStatus) {
	relay, hasRelay := m.selectedRelayStatus()
	attachDisabled := !hasRelay || relayDashboardAttached(relay)
	detachDisabled := !hasRelay || !relayDashboardAttached(relay)
	pane.addButtons(
		agentDashboardButton{label: "Attach Relay", action: agentDashboardClickAttachRelay, disabled: attachDisabled},
		agentDashboardButton{label: "Detach Relay", action: agentDashboardClickDetachRelay, disabled: detachDisabled},
	)
	if hasRelay {
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("Selected: "+relay.RelayURL+"  Public: "+valueOrDash(relay.PublicURL), width)))
	}
	pane.addLine("")
	pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit(fmt.Sprintf("%-9s %-9s %-9s %-7s %s", "STATE", "ROLE", "CAPS", "RTT", "RELAY"), width)))

	if len(tunnel.Relays) == 0 {
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("no discovered relays", width)))
		return
	}
	for i, relay := range tunnel.Relays {
		if len(pane.lines) >= height {
			pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit(fmt.Sprintf("+ %d more", len(tunnel.Relays)-i), width)))
			break
		}
		line := fmt.Sprintf("%-9s %-9s %-9s %-7s %s",
			relayDashboardState(relay),
			relayDashboardRole(relay),
			relayDashboardCaps(relay),
			relayDashboardRTT(relay),
			relay.RelayURL,
		)
		pane.addClickRow(line, width, agentDashboardRelayStyle(i == m.selectedRelay, relay), agentDashboardClickRelay, m.selectedTunnel, i)
	}
}

func (m agentDashboardModel) renderMultiHopTab(pane *agentDashboardPane, width, height int, tunnel types.AgentTunnelStatus) {
	route := m.displayedMultiHop(tunnel)
	relay, hasRelay := m.selectedRelayStatus()
	inRoute := hasRelay && slices.Contains(route, relay.RelayURL)
	canAdd := hasRelay && relay.SupportsOverlay && !inRoute

	pane.addButtons(
		agentDashboardButton{label: "Add to Route", action: agentDashboardClickAddHop, disabled: !canAdd},
		agentDashboardButton{label: "Remove from Route", action: agentDashboardClickRemoveHop, disabled: !inRoute},
		agentDashboardButton{label: "Apply Route", action: agentDashboardClickApplyHop, disabled: len(route) < 2},
		agentDashboardButton{label: "Clear Route", action: agentDashboardClickClearHop, disabled: len(route) == 0},
	)
	if hasRelay {
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("Selected: "+relay.RelayURL+"  Public: "+valueOrDash(relay.PublicURL), width)))
	}
	pane.addLine("")

	routeLabel := "Route: none"
	if len(route) > 0 {
		routeLabel = "Route: " + strings.Join(route, " -> ")
		if m.draftTunnelID == tunnel.ID {
			routeLabel += " (draft)"
		}
	}
	pane.addLine(agentDashboardFit(routeLabel, width))
	pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("Select discovered relays below, then add/remove them from the route.", width)))
	pane.addLine("")
	pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit(fmt.Sprintf("%-9s %-9s %-9s %-7s %s", "STATE", "ROLE", "CAPS", "RTT", "RELAY"), width)))

	if len(tunnel.Relays) == 0 {
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("no discovered relays", width)))
		return
	}
	for i, relay := range tunnel.Relays {
		if len(pane.lines) >= height {
			pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit(fmt.Sprintf("+ %d more", len(tunnel.Relays)-i), width)))
			break
		}
		line := fmt.Sprintf("%-9s %-9s %-9s %-7s %s",
			relayDashboardState(relay),
			relayDashboardRole(relay),
			relayDashboardCaps(relay),
			relayDashboardRTT(relay),
			relay.RelayURL,
		)
		pane.addClickRow(line, width, agentDashboardRelayStyle(i == m.selectedRelay, relay), agentDashboardClickRelay, m.selectedTunnel, i)
	}
}

func (m agentDashboardModel) renderLogsTab(pane *agentDashboardPane, width, height int) {
	pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("Recent logs", width)))

	logs := m.logs
	logs.Width = width
	logs.Height = max(4, height-len(pane.lines))
	logs.SetContent(m.logContent())

	for _, line := range strings.Split(logs.View(), "\n") {
		if len(pane.lines) >= height {
			break
		}
		pane.addLine(agentDashboardFit(line, width))
	}
}

func (m agentDashboardModel) logContent() string {
	if len(m.status.Logs) == 0 {
		return "no recent logs"
	}

	width := m.logs.Width
	if width <= 0 {
		_, width, _ = agentDashboardPaneSizes(m.width, m.height)
	}

	lines := make([]string, 0, len(m.status.Logs))
	for _, entry := range m.status.Logs {
		line := fmt.Sprintf("%s %-5s %-14s %s",
			entry.Time.Local().Format("15:04:05"),
			strings.ToUpper(entry.Level),
			truncateDashboardValue(valueOrDash(entry.TunnelID), 14),
			entry.Message,
		)
		lines = append(lines, agentDashboardFit(line, width))
	}
	return strings.Join(lines, "\n")
}

func (l *agentDashboardLayout) addLine(line string) {
	l.lines = append(l.lines, line)
}

func (l *agentDashboardLayout) addButtons(buttons ...agentDashboardButton) {
	line, regions := agentDashboardRenderButtons(len(l.lines), 0, buttons...)
	l.lines = append(l.lines, line)
	l.regions = append(l.regions, regions...)
}

func (l *agentDashboardLayout) addPanes(left, right agentDashboardPane, leftWidth, gutter int) {
	startY := len(l.lines)
	height := max(len(left.lines), len(right.lines))
	for i := 0; i < height; i++ {
		leftLine := ""
		if i < len(left.lines) {
			leftLine = left.lines[i]
		}
		rightLine := ""
		if i < len(right.lines) {
			rightLine = right.lines[i]
		}
		l.lines = append(l.lines, agentDashboardPadStyled(leftLine, leftWidth)+strings.Repeat(" ", gutter)+rightLine)
	}
	for _, region := range left.regions {
		region.y += startY
		l.regions = append(l.regions, region)
	}
	for _, region := range right.regions {
		region.y += startY
		region.x0 += leftWidth + gutter
		region.x1 += leftWidth + gutter
		l.regions = append(l.regions, region)
	}
}

func (p *agentDashboardPane) addLine(line string) {
	p.lines = append(p.lines, line)
}

func (p *agentDashboardPane) addButtons(buttons ...agentDashboardButton) {
	line, regions := agentDashboardRenderButtons(len(p.lines), 0, buttons...)
	p.lines = append(p.lines, line)
	p.regions = append(p.regions, regions...)
}

func (p *agentDashboardPane) addTabs(active agentDashboardTab) {
	y := len(p.lines)
	x := 0
	var b strings.Builder
	tabs := []struct {
		label string
		tab   agentDashboardTab
	}{
		{label: "Relays", tab: agentDashboardRelaysTab},
		{label: "Multi-Hop", tab: agentDashboardMultiHopTab},
		{label: "Logs", tab: agentDashboardLogsTab},
	}

	for i, tab := range tabs {
		if i > 0 {
			b.WriteString(" ")
			x++
		}
		plain := "[ " + tab.label + " ]"
		style := agentDashboardTabStyle
		if tab.tab == active {
			style = agentDashboardActiveTab
		}
		p.regions = append(p.regions, agentDashboardClickRegion{
			x0:     x,
			x1:     x + lipgloss.Width(plain),
			y:      y,
			action: agentDashboardClickTab,
			tab:    tab.tab,
		})
		b.WriteString(style.Render(plain))
		x += lipgloss.Width(plain)
	}
	p.lines = append(p.lines, b.String())
}

func (p *agentDashboardPane) addClickRow(line string, width int, style lipgloss.Style, action agentDashboardClick, tunnel, relay int) {
	plain := agentDashboardFit(line, width)
	y := len(p.lines)
	p.lines = append(p.lines, style.Width(width).Render(plain))
	p.regions = append(p.regions, agentDashboardClickRegion{
		x0:     0,
		x1:     width,
		y:      y,
		action: action,
		tunnel: tunnel,
		relay:  relay,
	})
}

func agentDashboardRenderButtons(y, x int, buttons ...agentDashboardButton) (string, []agentDashboardClickRegion) {
	var line strings.Builder
	var regions []agentDashboardClickRegion
	for i, button := range buttons {
		if i > 0 {
			line.WriteString(" ")
			x++
		}
		plain := "[ " + button.label + " ]"
		style := agentDashboardButtonStyle
		if button.disabled {
			style = agentDashboardDisabledStyle
		} else {
			regions = append(regions, agentDashboardClickRegion{
				x0:     x,
				x1:     x + lipgloss.Width(plain),
				y:      y,
				action: button.action,
			})
		}
		line.WriteString(style.Render(plain))
		x += lipgloss.Width(plain)
	}
	return line.String(), regions
}

func agentDashboardPaneSizes(width, height int) (int, int, int) {
	if width <= 0 {
		width = 104
	}
	width = max(width, 88)

	leftWidth := width / 3
	leftWidth = min(max(leftWidth, 30), 42)
	rightWidth := max(42, width-leftWidth-2)

	bodyHeight := height - 8
	if height <= 0 {
		bodyHeight = 22
	}
	bodyHeight = max(bodyHeight, 14)
	return leftWidth, rightWidth, bodyHeight
}

func agentDashboardSummaryLine(status types.AgentStatusResponse) string {
	if strings.TrimSpace(status.ReleaseVersion) == "" {
		return ""
	}
	return "v" + status.ReleaseVersion
}

func agentDashboardTunnelStyle(selected bool, state string) lipgloss.Style {
	if selected {
		return agentDashboardSelectedStyle
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running":
		return agentDashboardOKStyle
	case "error":
		return agentDashboardErrorStyle
	case "starting", "restarting":
		return agentDashboardMessageStyle
	default:
		return lipgloss.NewStyle()
	}
}

func agentDashboardRelayStyle(selected bool, relay types.AgentRelayStatus) lipgloss.Style {
	if selected {
		return agentDashboardSelectedStyle
	}
	if relay.Banned {
		return agentDashboardErrorStyle
	}
	if relay.Connected || relay.Active {
		return agentDashboardOKStyle
	}
	return agentDashboardMutedStyle
}

func relayDashboardState(relay types.AgentRelayStatus) string {
	switch {
	case relay.Banned:
		return "removed"
	case relay.Connected:
		return "up"
	case relay.Active:
		return "active"
	case relay.Confirmed:
		return "known"
	case relay.Bootstrap:
		return "seed"
	default:
		return "seen"
	}
}

func relayDashboardRole(relay types.AgentRelayStatus) string {
	switch {
	case relay.Banned:
		return "blocked"
	case relay.Active || relay.Connected:
		return "attached"
	case relay.Bootstrap:
		return "bootstrap"
	case relay.Confirmed:
		return "known"
	default:
		return "discovered"
	}
}

func relayDashboardAttached(relay types.AgentRelayStatus) bool {
	return relay.Active || relay.Connected || relay.Bootstrap
}

func relayDashboardCaps(relay types.AgentRelayStatus) string {
	var caps []string
	if relay.SupportsOverlay {
		caps = append(caps, "hop")
	}
	if relay.SupportsUDP {
		caps = append(caps, "udp")
	}
	if relay.SupportsTCP {
		caps = append(caps, "tcp")
	}
	if len(caps) == 0 {
		return "-"
	}
	return strings.Join(caps, "/")
}

func relayDashboardRTT(relay types.AgentRelayStatus) string {
	if relay.DiscoveryRTTMillis <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", relay.DiscoveryRTTMillis)
}

func durationSince(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Round(time.Second).String()
}

func firstOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return valueOrDash(values[0])
}

func valueOrDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func truncateDashboardValue(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return agentDashboardFit(value, maxLength)
}

func agentDashboardFit(value string, width int) string {
	value = strings.TrimSpace(value)
	if value == "" || width <= 0 {
		return ""
	}

	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "~"
	}
	return string(runes[:width-1]) + "~"
}

func agentDashboardPadStyled(value string, width int) string {
	if lipgloss.Width(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-lipgloss.Width(value))
}
