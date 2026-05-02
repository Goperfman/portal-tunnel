package agent

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gosuda/portal-tunnel/v2/types"
)

const agentDashboardPollInterval = 2 * time.Second

type agentDashboardMode int

const (
	agentDashboardNormalMode agentDashboardMode = iota
	agentDashboardAddTunnelMode
	agentDashboardAddRelayMode
)

type agentDashboardClick int

const (
	agentDashboardClickTunnel agentDashboardClick = iota + 1
	agentDashboardClickRelay
	agentDashboardClickAddTunnel
	agentDashboardClickDeleteTunnel
	agentDashboardClickAddRelay
	agentDashboardClickDeleteRelay
	agentDashboardClickAttachRelay
	agentDashboardClickSeedRelay
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

	multiHopDraft []string
	draftTunnelID string

	mode  agentDashboardMode
	input textinput.Model
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
}

type agentDashboardLayout struct {
	lines   []string
	regions []agentDashboardClickRegion
}

type agentDashboardButton struct {
	label    string
	action   agentDashboardClick
	disabled bool
}

type agentDashboardPane struct {
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
	agentDashboardInputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("120"))
)

func RunDashboard(configPath, stateDir string) error {
	input := textinput.New()
	input.CharLimit = 512
	input.Prompt = "> "
	input.Width = 72

	_, err := tea.NewProgram(agentDashboardModel{
		configPath: configPath,
		stateDir:   stateDir,
		input:      input,
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
		m.input.Width = max(1, min(88, msg.Width-8))
		return m, nil
	case agentDashboardTickMsg:
		return m, tea.Batch(agentDashboardFetchStatus(m.stateDir), agentDashboardTick())
	case agentDashboardStatusMsg:
		m.err = msg.err
		if msg.err == nil {
			m.status = msg.status
			m.clampSelection()
		}
		return m, nil
	case agentDashboardActionMsg:
		m.err = msg.err
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.message = msg.message
			if msg.message == "multi-hop applied" || msg.message == "multi-hop cleared" {
				m.multiHopDraft = nil
				m.draftTunnelID = ""
			}
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
	if m.mode != agentDashboardNormalMode {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.cancelInput()
			return m, nil
		case "enter":
			return m.submitInput()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.selectPreviousTunnel()
	case "down", "j":
		m.selectNextTunnel()
	case "left", "h":
		m.selectPreviousRelay()
	case "right", "l":
		m.selectNextRelay()
	case "n":
		return m.startInput(agentDashboardAddTunnelMode, "New tunnel: ", "name port")
	case "x":
		return m.deleteSelectedTunnel()
	case "a":
		if _, ok := m.selectedTunnelStatus(); !ok {
			m.message = "select a tunnel first"
			return m, nil
		}
		return m.startInput(agentDashboardAddRelayMode, "Add relay: ", "https://relay.example.com")
	case "d":
		return m.deleteSelectedRelay()
	case "m":
		return m.addSelectedHop()
	case "u":
		return m.removeSelectedHop()
	case "p":
		return m.applyMultiHop()
	case "c":
		return m.clearMultiHop()
	}
	return m, nil
}

func (m agentDashboardModel) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	event := tea.MouseEvent(msg)
	switch event.Button {
	case tea.MouseButtonWheelUp:
		m.selectPreviousTunnel()
		return m, nil
	case tea.MouseButtonWheelDown:
		m.selectNextTunnel()
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
	case agentDashboardClickTunnel:
		if region.tunnel >= 0 && region.tunnel < len(m.status.Tunnels) {
			m.selectTunnel(region.tunnel)
		}
	case agentDashboardClickRelay:
		if tunnel, ok := m.selectedTunnelStatus(); ok && region.relay >= 0 && region.relay < len(tunnel.Relays) {
			m.selectRelay(region.relay, tunnel.Relays[region.relay].RelayURL)
		}
	case agentDashboardClickAddTunnel:
		return m.startInput(agentDashboardAddTunnelMode, "New tunnel: ", "name port")
	case agentDashboardClickDeleteTunnel:
		return m.deleteSelectedTunnel()
	case agentDashboardClickAddRelay:
		if _, ok := m.selectedTunnelStatus(); !ok {
			m.message = "select a tunnel first"
			return m, nil
		}
		return m.startInput(agentDashboardAddRelayMode, "Add relay: ", "https://relay.example.com")
	case agentDashboardClickDeleteRelay:
		return m.deleteSelectedRelay()
	case agentDashboardClickAttachRelay:
		return m.attachSelectedRelay()
	case agentDashboardClickSeedRelay:
		return m.seedSelectedRelay()
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
	lines := layout.lines
	if m.height > 0 {
		if len(lines) > m.height {
			lines = lines[:m.height]
		}
		for len(lines) < m.height {
			lines = append(lines, "")
		}
	}
	return strings.Join(lines, "\n")
}

func (m *agentDashboardModel) selectTunnel(index int) {
	if index < 0 || index >= len(m.status.Tunnels) {
		return
	}
	m.selectedTunnel = index
	m.selectedTunnelID = m.status.Tunnels[index].ID
	m.selectedRelay = 0
	m.selectedRelayURL = ""
	if len(m.status.Tunnels[index].Relays) > 0 {
		m.selectedRelayURL = m.status.Tunnels[index].Relays[0].RelayURL
	}
}

func (m *agentDashboardModel) selectPreviousTunnel() {
	if m.selectedTunnel > 0 {
		m.selectTunnel(m.selectedTunnel - 1)
	}
}

func (m *agentDashboardModel) selectNextTunnel() {
	if m.selectedTunnel+1 < len(m.status.Tunnels) {
		m.selectTunnel(m.selectedTunnel + 1)
	}
}

func (m *agentDashboardModel) selectRelay(index int, relayURL string) {
	m.selectedRelay = index
	m.selectedRelayURL = relayURL
}

func (m *agentDashboardModel) selectPreviousRelay() {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok || len(tunnel.Relays) == 0 || m.selectedRelay <= 0 {
		return
	}
	m.selectRelay(m.selectedRelay-1, tunnel.Relays[m.selectedRelay-1].RelayURL)
}

func (m *agentDashboardModel) selectNextRelay() {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok || m.selectedRelay+1 >= len(tunnel.Relays) {
		return
	}
	m.selectRelay(m.selectedRelay+1, tunnel.Relays[m.selectedRelay+1].RelayURL)
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
		found := false
		for i, tunnel := range m.status.Tunnels {
			if tunnel.ID == m.selectedTunnelID {
				m.selectedTunnel = i
				found = true
				break
			}
		}
		if !found {
			m.selectedTunnel = 0
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
		found := false
		for i, relay := range relays {
			if relay.RelayURL == m.selectedRelayURL {
				m.selectedRelay = i
				found = true
				break
			}
		}
		if !found {
			m.selectedRelay = 0
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

func (m agentDashboardModel) startInput(mode agentDashboardMode, prompt, placeholder string) (tea.Model, tea.Cmd) {
	m.mode = mode
	m.input.Reset()
	m.input.Prompt = prompt
	m.input.Placeholder = placeholder
	m.input.PromptStyle = agentDashboardSectionStyle
	m.input.TextStyle = agentDashboardInputStyle
	m.input.PlaceholderStyle = agentDashboardMutedStyle
	m.input.Width = max(1, min(88, m.width-8))
	m.message = ""
	return m, tea.Batch(m.input.Focus(), textinput.Blink)
}

func (m *agentDashboardModel) cancelInput() {
	m.mode = agentDashboardNormalMode
	m.input.Blur()
	m.input.Reset()
	m.message = "input canceled"
}

func (m agentDashboardModel) submitInput() (tea.Model, tea.Cmd) {
	mode := m.mode
	value := strings.TrimSpace(m.input.Value())
	m.mode = agentDashboardNormalMode
	m.input.Blur()
	m.input.Reset()

	switch mode {
	case agentDashboardAddTunnelMode:
		fields := strings.Fields(value)
		if len(fields) < 2 {
			m.message = "use: name port"
			return m, nil
		}
		name := strings.Join(fields[:len(fields)-1], " ")
		port := strings.TrimPrefix(fields[len(fields)-1], ":")
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			m.message = "port must be 1-65535"
			return m, nil
		}
		m.message = "adding tunnel..."
		return m, agentDashboardAddTunnel(m.stateDir, name, "127.0.0.1:"+port)
	case agentDashboardAddRelayMode:
		if value == "" {
			m.message = "relay url is required"
			return m, nil
		}
		tunnel, ok := m.selectedTunnelStatus()
		if !ok {
			m.message = "select a tunnel first"
			return m, nil
		}
		m.message = "adding relay..."
		return m, agentDashboardAddRelay(m.stateDir, tunnel.ID, value)
	}
	return m, nil
}

func (m agentDashboardModel) deleteSelectedTunnel() (tea.Model, tea.Cmd) {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		m.message = "no tunnel selected"
		return m, nil
	}
	if len(m.status.Tunnels) <= 1 {
		m.message = "cannot delete the last tunnel"
		return m, nil
	}
	m.message = "deleting " + tunnel.ID + "..."
	return m, agentDashboardDeleteTunnel(m.stateDir, tunnel.ID)
}

func (m agentDashboardModel) deleteSelectedRelay() (tea.Model, tea.Cmd) {
	tunnel, relay, ok := m.selectedTunnelRelay()
	if !ok {
		m.message = "select a relay first"
		return m, nil
	}
	m.message = "deleting relay..."
	return m, agentDashboardRemoveRelay(m.stateDir, tunnel.ID, relay.RelayURL)
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

func (m agentDashboardModel) seedSelectedRelay() (tea.Model, tea.Cmd) {
	tunnel, relay, ok := m.selectedTunnelRelay()
	if !ok {
		m.message = "select a relay first"
		return m, nil
	}
	m.message = "switching relay to seed..."
	return m, agentDashboardSeedRelay(m.stateDir, tunnel.ID, relay.RelayURL)
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
	return m, agentDashboardSetMultiHop(m.stateDir, tunnel.ID, nil)
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
	return tea.Tick(agentDashboardPollInterval, func(t time.Time) tea.Msg {
		return agentDashboardTickMsg(t)
	})
}

func agentDashboardAddRelay(stateDir, tunnelID, relayURL string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := AddRelay(ctx, stateDir, tunnelID, relayURL)
		return agentDashboardActionMsg{message: "relay added", err: err}
	}
}

func agentDashboardRemoveRelay(stateDir, tunnelID, relayURL string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := RemoveRelay(ctx, stateDir, tunnelID, relayURL)
		return agentDashboardActionMsg{message: "relay deleted", err: err}
	}
}

func agentDashboardSeedRelay(stateDir, tunnelID, relayURL string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := SeedRelay(ctx, stateDir, tunnelID, relayURL)
		return agentDashboardActionMsg{message: "relay switched to seed", err: err}
	}
}

func agentDashboardSetMultiHop(stateDir, tunnelID string, relayURLs []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := SetMultiHop(ctx, stateDir, tunnelID, relayURLs)
		message := "multi-hop applied"
		if relayURLs == nil {
			message = "multi-hop cleared"
		}
		return agentDashboardActionMsg{message: message, err: err}
	}
}

func agentDashboardAddTunnel(stateDir, name, target string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := AddTunnel(ctx, stateDir, types.AgentTunnelRequest{
			Name:       name,
			TargetAddr: target,
		})
		return agentDashboardActionMsg{message: "tunnel added", err: err}
	}
}

func agentDashboardDeleteTunnel(stateDir, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := DeleteTunnel(ctx, stateDir, id)
		return agentDashboardActionMsg{message: "tunnel deleted", err: err}
	}
}

func (m agentDashboardModel) layout() agentDashboardLayout {
	width := m.width
	if width <= 0 {
		width = 88
	}
	leftWidth, rightWidth, bodyHeight := agentDashboardPaneSizes(width, m.height)

	var layout agentDashboardLayout
	layout.addLine(agentDashboardTitleStyle.Render(agentDashboardFit("Portal Agent  "+types.ReleaseVersion, width)))
	layout.addLine(agentDashboardMutedStyle.Render(strings.Repeat("-", min(width, 120))))

	if m.err != nil && m.status.ControlAddr == "" {
		layout.addLine(agentDashboardErrorStyle.Render(agentDashboardFit(fmt.Sprintf("Agent unavailable: %v", m.err), width)))
		layout.addLine("")
		layout.addLine(agentDashboardFit("Start managed service: portal agent run --config "+m.configPath, width))
		layout.addLine(agentDashboardFit("No service manager:   portal agent run --foreground --config "+m.configPath, width))
		return layout
	}

	tunnelCount := len(m.status.Tunnels)
	runningCount := 0
	errorCount := 0
	for _, tunnel := range m.status.Tunnels {
		switch tunnel.State {
		case tunnelStateRunning:
			runningCount++
		case tunnelStateError:
			errorCount++
		}
	}
	layout.addLine(agentDashboardFit(fmt.Sprintf("Control: %s  Tunnels: %d  Running: %d  Errors: %d",
		valueOrDash(m.status.ControlAddr),
		tunnelCount,
		runningCount,
		errorCount,
	), width))
	if m.message != "" {
		layout.addLine(agentDashboardMessageStyle.Render(agentDashboardFit("Message: "+m.message, width)))
	}
	if m.err != nil {
		layout.addLine(agentDashboardErrorStyle.Render(agentDashboardFit(fmt.Sprintf("Error: %v", m.err), width)))
	}
	if m.mode != agentDashboardNormalMode {
		layout.addLine(m.input.View())
	}
	layout.addLine("")
	if m.height > 0 {
		bodyHeight = max(1, m.height-len(layout.lines))
	}

	left := m.renderTunnelsPane(leftWidth, bodyHeight)
	right := m.renderTunnelPane(rightWidth, bodyHeight)
	layout.addPanes(left, right, leftWidth, 2)
	return layout
}

func (m agentDashboardModel) renderTunnelsPane(width, height int) agentDashboardPane {
	var pane agentDashboardPane
	pane.addLine(agentDashboardSectionStyle.Render(agentDashboardFit("Tunnels", width)))
	pane.addButtons(width,
		agentDashboardButton{label: "Add Tunnel", action: agentDashboardClickAddTunnel},
		agentDashboardButton{label: "Delete Tunnel", action: agentDashboardClickDeleteTunnel, disabled: len(m.status.Tunnels) <= 1},
	)
	pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit(fmt.Sprintf("%d managed", len(m.status.Tunnels)), width)))
	pane.addLine(agentDashboardMutedStyle.Render(strings.Repeat("-", width)))

	if len(m.status.Tunnels) == 0 {
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("no managed tunnels", width)))
		pane.clip(height)
		return pane
	}

	detailLines := make([]string, 0, 5)
	if tunnel, ok := m.selectedTunnelStatus(); ok {
		detailLines = append(detailLines,
			"",
			agentDashboardSectionStyle.Render(agentDashboardFit("Selected Tunnel", width)),
			agentDashboardFit("State: "+valueOrDash(tunnel.State), width),
			agentDashboardFit("Target: "+valueOrDash(tunnel.TargetAddr), width),
			agentDashboardFit("Public: "+firstOrDash(tunnel.PublicURLs), width),
		)
		if strings.TrimSpace(tunnel.LastError) != "" {
			detailLines = append(detailLines, agentDashboardErrorStyle.Render(agentDashboardFit("Error: "+tunnel.LastError, width)))
		}
	}
	listLimit := height - len(pane.lines) - len(detailLines)
	if listLimit < 3 {
		listLimit = height - len(pane.lines)
	}
	for i, tunnel := range m.status.Tunnels {
		if i >= listLimit || len(pane.lines) >= height {
			pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit(fmt.Sprintf("+ %d more", len(m.status.Tunnels)-i), width)))
			break
		}
		name := tunnel.ID
		if strings.TrimSpace(tunnel.Name) != "" {
			name = tunnel.Name
		}
		nameWidth := max(8, width-11)
		line := fmt.Sprintf("%-10s %s", truncateDashboardValue(tunnel.State, 10), agentDashboardFit(name, nameWidth))
		pane.addClickRow(line, width, agentDashboardTunnelStyle(i == m.selectedTunnel, tunnel.State), agentDashboardClickTunnel, i, -1)
	}
	for _, line := range detailLines {
		if len(pane.lines) >= height {
			break
		}
		pane.addLine(line)
	}
	pane.clip(height)
	return pane
}

func (m agentDashboardModel) renderTunnelPane(width, height int) agentDashboardPane {
	var pane agentDashboardPane
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		pane.addLine(agentDashboardSectionStyle.Render(agentDashboardFit("Relays", width)))
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("select a managed tunnel", width)))
		pane.clip(height)
		return pane
	}

	relayLimit := max(4, (height-len(pane.lines)-6)/2)
	m.renderRelaysSection(&pane, width, relayLimit, tunnel)
	pane.addLine("")
	m.renderMultiHopSection(&pane, width, height, tunnel)
	pane.clip(height)
	return pane
}

func (m agentDashboardModel) renderRelaysSection(pane *agentDashboardPane, width, maxRows int, tunnel types.AgentTunnelStatus) {
	relay, hasRelay := m.selectedRelayStatus()
	attached := hasRelay && relayDashboardAttached(relay)
	attachDisabled := !hasRelay || attached || relay.Banned
	seedDisabled := !hasRelay || relay.Banned || (!attached && relay.Bootstrap)
	deleteDisabled := !hasRelay

	pane.addLine(agentDashboardSectionStyle.Render(agentDashboardFit("Relays", width)))
	pane.addButtons(width,
		agentDashboardButton{label: "Attach", action: agentDashboardClickAttachRelay, disabled: attachDisabled},
		agentDashboardButton{label: "Seed", action: agentDashboardClickSeedRelay, disabled: seedDisabled},
		agentDashboardButton{label: "Add URL", action: agentDashboardClickAddRelay},
		agentDashboardButton{label: "Remove", action: agentDashboardClickDeleteRelay, disabled: deleteDisabled},
	)
	if hasRelay {
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("Selected: "+relay.RelayURL, width)))
	}
	pane.addLine(agentDashboardMutedStyle.Render(agentDashboardRelayRow(width, "STATE", "ROLE", "CAPS", "RTT", "RELAY")))

	if len(tunnel.Relays) == 0 {
		pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit("no relays", width)))
		return
	}
	for i, relay := range tunnel.Relays {
		if i >= maxRows {
			pane.addLine(agentDashboardMutedStyle.Render(agentDashboardFit(fmt.Sprintf("+ %d more", len(tunnel.Relays)-i), width)))
			break
		}
		line := agentDashboardRelayRow(width,
			relayDashboardState(relay),
			relayDashboardRole(relay),
			relayDashboardCaps(relay),
			relayDashboardRTT(relay),
			relay.RelayURL,
		)
		pane.addClickRow(line, width, agentDashboardRelayStyle(i == m.selectedRelay, relay), agentDashboardClickRelay, m.selectedTunnel, i)
	}
}

func (m agentDashboardModel) renderMultiHopSection(pane *agentDashboardPane, width, height int, tunnel types.AgentTunnelStatus) {
	route := m.displayedMultiHop(tunnel)
	relay, hasRelay := m.selectedRelayStatus()
	inRoute := hasRelay && slices.Contains(route, relay.RelayURL)
	canAdd := hasRelay && relay.SupportsOverlay && !inRoute

	pane.addLine(agentDashboardSectionStyle.Render(agentDashboardFit("Multi-hop", width)))
	pane.addButtons(width,
		agentDashboardButton{label: "Add Hop", action: agentDashboardClickAddHop, disabled: !canAdd},
		agentDashboardButton{label: "Remove Hop", action: agentDashboardClickRemoveHop, disabled: !inRoute},
		agentDashboardButton{label: "Apply", action: agentDashboardClickApplyHop, disabled: len(route) < 2},
		agentDashboardButton{label: "Clear", action: agentDashboardClickClearHop, disabled: len(route) == 0},
	)

	routeLabel := "Route: none"
	if len(route) > 0 {
		routeLabel = "Route: " + strings.Join(route, " -> ")
		if m.draftTunnelID == tunnel.ID {
			routeLabel += " (draft)"
		}
	}
	for _, line := range agentDashboardWrap(routeLabel, width) {
		if len(pane.lines) >= height {
			return
		}
		pane.addLine(agentDashboardFit(line, width))
	}
}

func (l *agentDashboardLayout) addLine(line string) {
	l.lines = append(l.lines, line)
}

func (l *agentDashboardLayout) addButtons(width int, buttons ...agentDashboardButton) {
	lines, regions := agentDashboardRenderButtons(width, len(l.lines), 0, buttons...)
	l.lines = append(l.lines, lines...)
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

func (p *agentDashboardPane) addButtons(width int, buttons ...agentDashboardButton) {
	lines, regions := agentDashboardRenderButtons(width, len(p.lines), 0, buttons...)
	p.lines = append(p.lines, lines...)
	p.regions = append(p.regions, regions...)
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

func (p *agentDashboardPane) clip(height int) {
	if height <= 0 || len(p.lines) <= height {
		return
	}
	p.lines = p.lines[:height]
	regions := p.regions[:0]
	for _, region := range p.regions {
		if region.y < height {
			regions = append(regions, region)
		}
	}
	p.regions = regions
}

func agentDashboardRenderButtons(width, y, x int, buttons ...agentDashboardButton) ([]string, []agentDashboardClickRegion) {
	if width <= 0 {
		width = 1
	}
	var line strings.Builder
	var lines []string
	var regions []agentDashboardClickRegion
	lineY := y
	lineX := x
	for i, button := range buttons {
		plain := "[ " + button.label + " ]"
		if lipgloss.Width(plain) > width {
			plain = agentDashboardFit(plain, width)
		}
		plainWidth := lipgloss.Width(plain)
		space := 0
		if i > 0 && line.Len() > 0 {
			space = 1
		}
		if line.Len() > 0 && lineX+space+plainWidth > width {
			lines = append(lines, line.String())
			line.Reset()
			lineY++
			lineX = x
			space = 0
		}
		if space > 0 {
			line.WriteString(" ")
			lineX++
		}
		style := agentDashboardButtonStyle
		if button.disabled {
			style = agentDashboardDisabledStyle
		} else {
			regions = append(regions, agentDashboardClickRegion{
				x0:     lineX,
				x1:     min(lineX+plainWidth, width),
				y:      lineY,
				action: button.action,
			})
		}
		line.WriteString(style.Render(plain))
		lineX += plainWidth
	}
	if line.Len() > 0 || len(lines) == 0 {
		lines = append(lines, line.String())
	}
	return lines, regions
}

func agentDashboardPaneSizes(width, height int) (int, int, int) {
	if width <= 0 {
		width = 104
	}

	gutter := 2
	if width < 84 {
		leftWidth := min(max(width/2, 1), 40)
		if width >= 48 {
			leftWidth = max(leftWidth, 24)
		}
		rightWidth := width - leftWidth - gutter
		if rightWidth < 1 {
			rightWidth = 1
			leftWidth = max(1, width-gutter-rightWidth)
		}
		return leftWidth, rightWidth, defaultDashboardBodyHeight(height)
	}

	leftWidth := width / 3
	leftWidth = min(max(leftWidth, 40), 56)
	rightWidth := max(1, width-leftWidth-gutter)
	return leftWidth, rightWidth, defaultDashboardBodyHeight(height)
}

func defaultDashboardBodyHeight(height int) int {
	bodyHeight := height - 8
	if height <= 0 {
		bodyHeight = 22
	}
	return max(bodyHeight, 1)
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
	case "starting":
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
	return relay.Active || relay.Connected
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

func agentDashboardRelayRow(width int, state, role, caps, rtt, relayURL string) string {
	if width < 28 {
		return agentDashboardFit(state+" "+relayURL, width)
	}
	if width < 48 {
		stateW := 7
		return agentDashboardCell(state, stateW) + " " + agentDashboardFit(relayURL, width-stateW-1)
	}
	if width < 64 {
		stateW := 7
		roleW := 9
		rttW := 6
		relayW := max(1, width-stateW-roleW-rttW-3)
		return agentDashboardCell(state, stateW) + " " +
			agentDashboardCell(role, roleW) + " " +
			agentDashboardCell(rtt, rttW) + " " +
			agentDashboardFit(relayURL, relayW)
	}

	stateW := 8
	roleW := 9
	capsW := 8
	rttW := 6
	relayW := max(1, width-stateW-roleW-capsW-rttW-4)
	return agentDashboardCell(state, stateW) + " " +
		agentDashboardCell(role, roleW) + " " +
		agentDashboardCell(caps, capsW) + " " +
		agentDashboardCell(rtt, rttW) + " " +
		agentDashboardFit(relayURL, relayW)
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
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "~"
	}
	var out strings.Builder
	used := 0
	for _, r := range value {
		cellWidth := lipgloss.Width(string(r))
		if used+cellWidth > width-1 {
			break
		}
		out.WriteRune(r)
		used += cellWidth
	}
	return out.String() + "~"
}

func agentDashboardCell(value string, width int) string {
	value = agentDashboardFit(value, width)
	if lipgloss.Width(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-lipgloss.Width(value))
}

func agentDashboardWrap(value string, width int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var lines []string
	remaining := value
	for len([]rune(remaining)) > width && width > 1 {
		lines = append(lines, agentDashboardFit(remaining, width))
		runes := []rune(remaining)
		remaining = string(runes[min(width-1, len(runes)):])
	}
	if strings.TrimSpace(remaining) != "" {
		lines = append(lines, remaining)
	}
	return lines
}

func agentDashboardPadStyled(value string, width int) string {
	if lipgloss.Width(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-lipgloss.Width(value))
}
