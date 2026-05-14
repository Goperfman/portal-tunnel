package agent

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

const (
	agentDashboardPollInterval        = 2 * time.Second
	agentDashboardMinRelayRows        = 5
	agentDashboardTunnelInputMaxWidth = 80
)

type agentDashboardAction int

const (
	agentDashboardActionSelectTunnel agentDashboardAction = iota + 1
	agentDashboardActionSelectPane
	agentDashboardActionAddTunnel
	agentDashboardActionCancelAddTunnel
	agentDashboardActionDeleteTunnel
	agentDashboardActionConnectRelay
	agentDashboardActionDisconnectRelay
	agentDashboardActionAddHop
	agentDashboardActionApplyHop
	agentDashboardActionClearHop
	agentDashboardActionApplySettings
	agentDashboardActionFocusSettingsField
	agentDashboardActionOpenTunnelURL
)

type agentDashboardPane int

const (
	agentDashboardPaneTunnels agentDashboardPane = iota
	agentDashboardPaneRelays
	agentDashboardPaneSettings
	agentDashboardPaneMultiHop
	agentDashboardPaneCount
)

const (
	agentDashboardSettingsFieldMaxActiveRelays = iota
	agentDashboardSettingsFieldDescription
	agentDashboardSettingsFieldTags
	agentDashboardSettingsFieldOwner
	agentDashboardSettingsFieldThumbnail
	agentDashboardSettingsFieldHide
	agentDashboardSettingsFieldCount
)

type agentDashboardModel struct {
	configPath string
	stateDir   string

	status types.AgentStatusResponse
	err    error

	width  int
	height int

	selectedTunnelID string
	selectedRelayURL string
	activePane       agentDashboardPane

	routeDraft    []string
	draftTunnelID string

	addingTunnel bool
	input        textinput.Model

	settingsEditTunnelID string
	settingsFocus        int
	settingsMaxRelays    textinput.Model
	metadataDescription  textinput.Model
	metadataTags         textinput.Model
	metadataOwner        textinput.Model
	metadataThumbnail    textinput.Model
	metadataHide         textinput.Model
}

type agentDashboardStatusMsg struct {
	status types.AgentStatusResponse
	err    error
}

type agentDashboardActionMsg struct {
	err error
}

type agentDashboardTickMsg struct{}

type agentDashboardRegion struct {
	x0     int
	x1     int
	y      int
	action agentDashboardAction
	tunnel string
	relay  string
	field  int
	pane   agentDashboardPane
}

type agentDashboardButton struct {
	label    string
	action   agentDashboardAction
	disabled bool
}

type agentDashboardView struct {
	lines   []string
	regions []agentDashboardRegion
}

var (
	agentDashboardTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	agentDashboardSectionStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	agentDashboardMutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	agentDashboardSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("25"))
	agentDashboardButtonStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("238"))
	agentDashboardDisabledStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	agentDashboardErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	agentDashboardOKStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("120"))
	agentDashboardInputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("120"))
)

func RunDashboard(configPath, stateDir string) error {
	input := newAgentDashboardTextInput()
	input.Prompt = "Name port: "
	input.Placeholder = "myname 3000"
	input.Width = agentDashboardTunnelInputMaxWidth

	model := agentDashboardModel{
		configPath:          configPath,
		stateDir:            stateDir,
		input:               input,
		settingsMaxRelays:   newAgentDashboardInlineInput("3"),
		metadataDescription: newAgentDashboardInlineInput("description"),
		metadataTags:        newAgentDashboardInlineInput("api,staging"),
		metadataOwner:       newAgentDashboardInlineInput("owner"),
		metadataThumbnail:   newAgentDashboardInlineInput("https://..."),
		metadataHide:        newAgentDashboardInlineInput("true or false"),
	}
	model.resizeInputs(0)

	_, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func newAgentDashboardTextInput() textinput.Model {
	input := textinput.New()
	input.CharLimit = 512
	input.PromptStyle = agentDashboardSectionStyle
	input.TextStyle = agentDashboardInputStyle
	input.PlaceholderStyle = agentDashboardMutedStyle
	return input
}

func newAgentDashboardInlineInput(placeholder string) textinput.Model {
	input := newAgentDashboardTextInput()
	input.Placeholder = placeholder
	return input
}

func (m agentDashboardModel) Init() tea.Cmd {
	return tea.Batch(agentDashboardFetchStatus(m.stateDir), agentDashboardTick(), textinput.Blink)
}

func (m agentDashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeInputs(msg.Width)
		return m, nil
	case agentDashboardTickMsg:
		return m, tea.Batch(agentDashboardFetchStatus(m.stateDir), agentDashboardTick())
	case agentDashboardStatusMsg:
		m.err = msg.err
		if msg.err == nil {
			m.status = msg.status
			if strings.TrimSpace(msg.status.ConfigPath) != "" {
				m.configPath = msg.status.ConfigPath
			}
			m.clampSelection()
			m.ensureSelectedSettingsDraft()
		}
		return m, nil
	case agentDashboardActionMsg:
		m.err = msg.err
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
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.err = nil
		if m.activePane == agentDashboardPaneTunnels {
			if m.addingTunnel {
				m.cancelTunnelInput()
			} else {
				m.input.Reset()
			}
		} else {
			m.setActivePane(agentDashboardPaneTunnels)
		}
		return m, nil
	case "left":
		m.setActivePane(m.activePane - 1)
		return m, nil
	case "right":
		m.setActivePane(m.activePane + 1)
		return m, nil
	}

	switch m.activePane {
	case agentDashboardPaneTunnels:
		return m.updateTunnelKeys(msg)
	case agentDashboardPaneRelays:
		return m.updateRelayKeys(msg)
	case agentDashboardPaneSettings:
		return m.updateSettingsKeys(msg)
	case agentDashboardPaneMultiHop:
		return m.updateMultiHopKeys(msg)
	default:
		m.setActivePane(agentDashboardPaneTunnels)
		return m, nil
	}
}

func (m agentDashboardModel) updateTunnelKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		m.selectTunnelOffset(-1)
		return m, nil
	case "down":
		m.selectTunnelOffset(1)
		return m, nil
	case "delete":
		if !m.addingTunnel {
			return m.deleteTunnel("")
		}
	case "enter":
		if m.addingTunnel {
			return m.addTunnelFromInput()
		}
	}
	if !m.addingTunnel {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m agentDashboardModel) updateRelayKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		m.selectRelayOffset(-1)
	case "down":
		m.selectRelayOffset(1)
	case "enter", "c":
		return m.connectSelectedRelay()
	case "d", "delete":
		return m.disconnectSelectedRelay()
	case "o":
		return m.openRelayTunnelURL("", "")
	}
	return m, nil
}

func (m agentDashboardModel) updateMultiHopKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		m.selectRelayOffset(-1)
	case "down":
		m.selectRelayOffset(1)
	case "enter", "a":
		return m.addSelectedHop()
	case "p":
		return m.applyRoute()
	case "c", "delete":
		return m.clearRoute()
	}
	return m, nil
}

func (m agentDashboardModel) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	event := tea.MouseEvent(msg)
	switch event.Button {
	case tea.MouseButtonWheelUp:
		return m.scrollActivePane(-1)
	case tea.MouseButtonWheelDown:
		return m.scrollActivePane(1)
	}
	if event.Action != tea.MouseActionPress || event.Button != tea.MouseButtonLeft {
		return m, nil
	}
	for _, region := range m.layout().regions {
		if event.Y == region.y && event.X >= region.x0 && event.X < region.x1 {
			if region.action == agentDashboardActionSelectPane {
				m.setActivePane(region.pane)
				return m, nil
			}
			if region.action == agentDashboardActionFocusSettingsField {
				m.setActivePane(agentDashboardPaneSettings)
				m.focusSettingsField(region.field)
				return m, nil
			}
			return m.runAction(region.action, region.tunnel, region.relay)
		}
	}
	return m, nil
}

func (m agentDashboardModel) runAction(action agentDashboardAction, tunnelID, relayURL string) (tea.Model, tea.Cmd) {
	switch action {
	case agentDashboardActionSelectTunnel:
		if tunnelID != "" {
			m.selectTunnel(tunnelID)
		}
	case agentDashboardActionAddTunnel:
		return m.startOrAddTunnel()
	case agentDashboardActionCancelAddTunnel:
		m.cancelTunnelInput()
	case agentDashboardActionDeleteTunnel:
		return m.deleteTunnel(tunnelID)
	case agentDashboardActionConnectRelay:
		return m.connectSelectedRelay()
	case agentDashboardActionDisconnectRelay:
		return m.disconnectSelectedRelay()
	case agentDashboardActionAddHop:
		return m.addSelectedHop()
	case agentDashboardActionApplyHop:
		return m.applyRoute()
	case agentDashboardActionClearHop:
		return m.clearRoute()
	case agentDashboardActionApplySettings:
		return m.applySettingsEdit()
	case agentDashboardActionOpenTunnelURL:
		return m.openRelayTunnelURL(tunnelID, relayURL)
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

func (m agentDashboardModel) selectedTunnelIndex() int {
	if len(m.status.Tunnels) == 0 {
		return -1
	}
	for i, tunnel := range m.status.Tunnels {
		if tunnel.ID == m.selectedTunnelID {
			return i
		}
	}
	return 0
}

func (m *agentDashboardModel) selectTunnel(id string) {
	for i, tunnel := range m.status.Tunnels {
		if tunnel.ID == id {
			m.selectTunnelIndex(i)
			return
		}
	}
}

func (m *agentDashboardModel) selectTunnelIndex(index int) {
	if index < 0 || index >= len(m.status.Tunnels) {
		return
	}
	tunnelID := m.status.Tunnels[index].ID
	if m.selectedTunnelID != tunnelID {
		m.routeDraft = nil
		m.draftTunnelID = ""
	}
	m.selectedTunnelID = tunnelID
	m.selectedRelayURL = ""
	if len(m.status.Tunnels[index].Relays) > 0 {
		m.selectedRelayURL = m.status.Tunnels[index].Relays[0].RelayURL
	}
	m.loadSettingsDraft(m.status.Tunnels[index])
}

func (m *agentDashboardModel) selectTunnelOffset(delta int) {
	index := m.selectedTunnelIndex()
	next := index + delta
	if next >= 0 && next < len(m.status.Tunnels) {
		m.selectTunnelIndex(next)
	}
}

func (m *agentDashboardModel) selectRelay(relayURL string) {
	m.selectedRelayURL = relayURL
}

func (m *agentDashboardModel) selectRelayOffset(delta int) {
	tunnel, ok := m.selectedTunnelStatus()
	index := m.selectedRelayIndex(tunnel)
	next := index + delta
	if !ok || next < 0 || next >= len(tunnel.Relays) {
		return
	}
	m.selectRelay(tunnel.Relays[next].RelayURL)
}

func (m *agentDashboardModel) setActivePane(pane agentDashboardPane) {
	if pane < 0 {
		pane = agentDashboardPaneCount - 1
	}
	if pane >= agentDashboardPaneCount {
		pane = 0
	}
	m.activePane = pane
	m.input.Blur()
	m.blurSettingsInputs()
	switch pane {
	case agentDashboardPaneTunnels:
		if m.addingTunnel {
			_ = m.input.Focus()
		}
	case agentDashboardPaneSettings:
		m.ensureSelectedSettingsDraft()
		if input := m.focusedSettingsInput(); input != nil {
			_ = input.Focus()
		}
	}
}

func (m agentDashboardModel) scrollActivePane(delta int) (tea.Model, tea.Cmd) {
	switch m.activePane {
	case agentDashboardPaneTunnels:
		m.selectTunnelOffset(delta)
	case agentDashboardPaneRelays, agentDashboardPaneMultiHop:
		m.selectRelayOffset(delta)
	case agentDashboardPaneSettings:
		m.focusSettingsField(m.settingsFocus + delta)
	}
	return m, nil
}

func (m *agentDashboardModel) clampSelection() {
	if len(m.status.Tunnels) == 0 {
		m.selectedTunnelID = ""
		m.selectedRelayURL = ""
		m.routeDraft = nil
		m.draftTunnelID = ""
		m.clearSettingsDraft()
		return
	}

	tunnelIndex := m.selectedTunnelIndex()
	tunnelID := m.status.Tunnels[tunnelIndex].ID
	if m.selectedTunnelID != tunnelID {
		m.routeDraft = nil
		m.draftTunnelID = ""
	}
	m.selectedTunnelID = tunnelID
	m.ensureSettingsDraft(m.status.Tunnels[tunnelIndex])

	relays := m.status.Tunnels[tunnelIndex].Relays
	if len(relays) == 0 {
		m.selectedRelayURL = ""
		return
	}
	if m.selectedRelayURL != "" {
		for _, relay := range relays {
			if relay.RelayURL == m.selectedRelayURL {
				return
			}
		}
	}
	m.selectedRelayURL = relays[0].RelayURL
}

func (m agentDashboardModel) selectedTunnelStatus() (types.AgentTunnelStatus, bool) {
	index := m.selectedTunnelIndex()
	if index < 0 {
		return types.AgentTunnelStatus{}, false
	}
	return m.status.Tunnels[index], true
}

func (m agentDashboardModel) selectedRelayIndex(tunnel types.AgentTunnelStatus) int {
	if len(tunnel.Relays) == 0 {
		return -1
	}
	for i, relay := range tunnel.Relays {
		if relay.RelayURL == m.selectedRelayURL {
			return i
		}
	}
	return 0
}

func (m agentDashboardModel) selectedRelayStatus() (types.AgentRelayStatus, bool) {
	tunnel, ok := m.selectedTunnelStatus()
	index := m.selectedRelayIndex(tunnel)
	if !ok || index < 0 {
		return types.AgentRelayStatus{}, false
	}
	return tunnel.Relays[index], true
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

func (m agentDashboardModel) updateSettingsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab", "down":
		m.focusSettingsField(m.settingsFocus + 1)
		return m, nil
	case "shift+tab", "up":
		m.focusSettingsField(m.settingsFocus - 1)
		return m, nil
	case "enter":
		return m.applySettingsEdit()
	}

	input := m.focusedSettingsInput()
	if input == nil {
		return m, nil
	}
	var cmd tea.Cmd
	*input, cmd = input.Update(msg)
	return m, cmd
}

func (m *agentDashboardModel) focusSettingsField(field int) {
	if field < 0 {
		field = agentDashboardSettingsFieldCount - 1
	}
	if field >= agentDashboardSettingsFieldCount {
		field = 0
	}
	m.input.Blur()
	m.settingsFocus = field
	for _, input := range []*textinput.Model{
		&m.settingsMaxRelays,
		&m.metadataDescription,
		&m.metadataTags,
		&m.metadataOwner,
		&m.metadataThumbnail,
		&m.metadataHide,
	} {
		input.Blur()
	}
	if input := m.focusedSettingsInput(); input != nil {
		_ = input.Focus()
	}
}

func (m *agentDashboardModel) focusedSettingsInput() *textinput.Model {
	switch m.settingsFocus {
	case agentDashboardSettingsFieldMaxActiveRelays:
		return &m.settingsMaxRelays
	case agentDashboardSettingsFieldDescription:
		return &m.metadataDescription
	case agentDashboardSettingsFieldTags:
		return &m.metadataTags
	case agentDashboardSettingsFieldOwner:
		return &m.metadataOwner
	case agentDashboardSettingsFieldThumbnail:
		return &m.metadataThumbnail
	case agentDashboardSettingsFieldHide:
		return &m.metadataHide
	default:
		return nil
	}
}

func (m *agentDashboardModel) blurSettingsInputs() {
	for _, input := range []*textinput.Model{
		&m.settingsMaxRelays,
		&m.metadataDescription,
		&m.metadataTags,
		&m.metadataOwner,
		&m.metadataThumbnail,
		&m.metadataHide,
	} {
		input.Blur()
	}
}

func (m *agentDashboardModel) clearSettingsDraft() {
	m.settingsEditTunnelID = ""
	for _, input := range []*textinput.Model{
		&m.settingsMaxRelays,
		&m.metadataDescription,
		&m.metadataTags,
		&m.metadataOwner,
		&m.metadataThumbnail,
		&m.metadataHide,
	} {
		input.Reset()
	}
	m.blurSettingsInputs()
	if m.activePane == agentDashboardPaneTunnels && m.addingTunnel {
		_ = m.input.Focus()
	}
}

func (m *agentDashboardModel) resizeInputs(width int) {
	if width <= 0 {
		width = 88
	}
	availableWidth := width - lipgloss.Width(m.input.Prompt)
	m.input.Width = max(1, min(agentDashboardTunnelInputMaxWidth, availableWidth))

	settingsWidth := max(1, min(agentDashboardTunnelInputMaxWidth, width-13))
	for _, input := range []*textinput.Model{
		&m.settingsMaxRelays,
		&m.metadataDescription,
		&m.metadataTags,
		&m.metadataOwner,
		&m.metadataThumbnail,
		&m.metadataHide,
	} {
		input.Width = settingsWidth
	}
}

func (m *agentDashboardModel) ensureSelectedSettingsDraft() {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		return
	}
	m.ensureSettingsDraft(tunnel)
}

func (m *agentDashboardModel) ensureSettingsDraft(tunnel types.AgentTunnelStatus) {
	if m.settingsEditTunnelID == tunnel.ID {
		return
	}
	m.loadSettingsDraft(tunnel)
}

func (m *agentDashboardModel) loadSettingsDraft(tunnel types.AgentTunnelStatus) {
	metadata := tunnel.Metadata
	m.settingsEditTunnelID = tunnel.ID
	m.settingsMaxRelays.SetValue(strconv.Itoa(tunnel.MaxActiveRelays))
	m.metadataDescription.SetValue(strings.TrimSpace(metadata.Description))
	m.metadataTags.SetValue(strings.Join(metadata.Tags, ","))
	m.metadataOwner.SetValue(strings.TrimSpace(metadata.Owner))
	m.metadataThumbnail.SetValue(strings.TrimSpace(metadata.Thumbnail))
	m.metadataHide.SetValue(strconv.FormatBool(metadata.Hide))
	m.settingsMaxRelays.CursorEnd()
	m.metadataDescription.CursorEnd()
	m.metadataTags.CursorEnd()
	m.metadataOwner.CursorEnd()
	m.metadataThumbnail.CursorEnd()
	m.metadataHide.CursorEnd()
}

func (m agentDashboardModel) addTunnelFromInput() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())
	if value == "" {
		m.addingTunnel = true
		_ = m.input.Focus()
		return m, nil
	}
	fields := strings.Fields(value)
	if len(fields) < 2 {
		m.err = fmt.Errorf("use: name port")
		return m, nil
	}
	name := strings.Join(fields[:len(fields)-1], " ")
	if agentTunnelID(name) == "" {
		m.err = fmt.Errorf("tunnel name is required")
		return m, nil
	}
	targetInput := fields[len(fields)-1]
	target, err := utils.NormalizeLoopbackTarget(targetInput)
	if err != nil || target == "" {
		m.err = fmt.Errorf("invalid target %q", targetInput)
		return m, nil
	}

	m.err = nil
	m.input.Reset()
	m.addingTunnel = false
	m.input.Blur()
	return m, agentDashboardRun(func(ctx context.Context) error {
		return AddTunnel(ctx, m.stateDir, types.AgentTunnelRequest{
			Name:       name,
			TargetAddr: target,
		})
	})
}

func (m agentDashboardModel) startOrAddTunnel() (tea.Model, tea.Cmd) {
	if !m.addingTunnel {
		m.addingTunnel = true
		m.setActivePane(agentDashboardPaneTunnels)
		_ = m.input.Focus()
		return m, nil
	}
	return m.addTunnelFromInput()
}

func (m *agentDashboardModel) cancelTunnelInput() {
	m.addingTunnel = false
	m.input.Reset()
	m.input.Blur()
}

func (m agentDashboardModel) applySettingsEdit() (tea.Model, tea.Cmd) {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		return m, nil
	}
	maxActiveRelays, err := strconv.Atoi(strings.TrimSpace(m.settingsMaxRelays.Value()))
	if err != nil || maxActiveRelays <= 0 {
		m.err = fmt.Errorf("max active relays must be a positive integer")
		return m, nil
	}

	hideRaw := strings.TrimSpace(m.metadataHide.Value())
	if hideRaw == "" {
		hideRaw = "false"
	}
	hide, err := strconv.ParseBool(hideRaw)
	if err != nil {
		m.err = fmt.Errorf("metadata hidden must be true or false")
		return m, nil
	}

	description := strings.TrimSpace(m.metadataDescription.Value())
	tags := utils.SplitCSV(m.metadataTags.Value())
	owner := strings.TrimSpace(m.metadataOwner.Value())
	thumbnail := strings.TrimSpace(m.metadataThumbnail.Value())
	metadata := types.AgentMetadataRequest{
		Description: &description,
		Tags:        &tags,
		Owner:       &owner,
		Thumbnail:   &thumbnail,
		Hide:        &hide,
	}
	req := types.AgentTunnelUpdateRequest{
		MaxActiveRelays: &maxActiveRelays,
		Metadata:        &metadata,
	}
	m.err = nil
	return m, agentDashboardRun(func(ctx context.Context) error {
		return UpdateTunnel(ctx, m.stateDir, tunnel.ID, req)
	})
}

func (m agentDashboardModel) deleteTunnel(tunnelID string) (tea.Model, tea.Cmd) {
	tunnelID = strings.TrimSpace(tunnelID)
	if tunnelID == "" {
		tunnel, ok := m.selectedTunnelStatus()
		if !ok {
			return m, nil
		}
		tunnelID = tunnel.ID
	}
	return m, agentDashboardRun(func(ctx context.Context) error {
		return DeleteTunnel(ctx, m.stateDir, tunnelID)
	})
}

func (m agentDashboardModel) connectSelectedRelay() (tea.Model, tea.Cmd) {
	tunnel, relay, ok := m.selectedTunnelRelay()
	if !ok {
		return m, nil
	}
	if relay.Banned || relayDashboardActive(tunnel, relay) {
		return m, nil
	}
	return m, agentDashboardRun(func(ctx context.Context) error {
		return ConnectRelay(ctx, m.stateDir, tunnel.ID, relay.RelayURL)
	})
}

func (m agentDashboardModel) disconnectSelectedRelay() (tea.Model, tea.Cmd) {
	tunnel, relay, ok := m.selectedTunnelRelay()
	if !ok {
		return m, nil
	}
	if relay.Banned || !relayDashboardActive(tunnel, relay) || slices.Contains(m.displayedRoute(tunnel), relay.RelayURL) {
		return m, nil
	}
	return m, agentDashboardRun(func(ctx context.Context) error {
		return DisconnectRelay(ctx, m.stateDir, tunnel.ID, relay.RelayURL)
	})
}

func (m agentDashboardModel) openRelayTunnelURL(tunnelID, relayURL string) (tea.Model, tea.Cmd) {
	if tunnelID != "" {
		m.selectTunnel(tunnelID)
	}
	if relayURL != "" {
		m.selectRelay(relayURL)
	}
	_, relay, ok := m.selectedTunnelRelay()
	if !ok || strings.TrimSpace(relay.PublicURL) == "" {
		return m, nil
	}
	return m, agentDashboardRun(func(context.Context) error {
		return openDashboardURL(relay.PublicURL)
	})
}

func (m agentDashboardModel) addSelectedHop() (tea.Model, tea.Cmd) {
	tunnel, relay, ok := m.selectedTunnelRelay()
	if !ok {
		return m, nil
	}
	if !relay.SupportsOverlay {
		return m, nil
	}
	m.ensureRouteDraft(tunnel)
	if slices.Contains(m.routeDraft, relay.RelayURL) {
		return m, nil
	}
	m.routeDraft = append(m.routeDraft, relay.RelayURL)
	return m, nil
}

func (m agentDashboardModel) applyRoute() (tea.Model, tea.Cmd) {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		return m, nil
	}
	route := m.displayedRoute(tunnel)
	if len(route) < 2 {
		return m, nil
	}
	m.routeDraft = nil
	m.draftTunnelID = ""
	return m, agentDashboardRun(func(ctx context.Context) error {
		return SetMultiHop(ctx, m.stateDir, tunnel.ID, route)
	})
}

func (m agentDashboardModel) clearRoute() (tea.Model, tea.Cmd) {
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		return m, nil
	}
	m.routeDraft = nil
	m.draftTunnelID = ""
	return m, agentDashboardRun(func(ctx context.Context) error {
		return SetMultiHop(ctx, m.stateDir, tunnel.ID, nil)
	})
}

func (m *agentDashboardModel) ensureRouteDraft(tunnel types.AgentTunnelStatus) {
	if m.draftTunnelID == tunnel.ID {
		return
	}
	m.draftTunnelID = tunnel.ID
	m.routeDraft = append([]string(nil), tunnel.MultiHop...)
}

func (m agentDashboardModel) displayedRoute(tunnel types.AgentTunnelStatus) []string {
	if m.draftTunnelID == tunnel.ID {
		return append([]string(nil), m.routeDraft...)
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
		return agentDashboardTickMsg{}
	})
}

func agentDashboardRun(run func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return agentDashboardActionMsg{err: run(ctx)}
	}
}

func (m agentDashboardModel) layout() agentDashboardView {
	width := m.width
	if width <= 0 {
		width = 88
	}
	bodyHeight := defaultDashboardBodyHeight(m.height)

	var layout agentDashboardView
	layout.addStyled(width, agentDashboardTitleStyle, "Portal Agent  "+types.ReleaseVersion)
	if configPath := strings.TrimSpace(m.status.ConfigPath); configPath != "" {
		layout.addStyled(width, agentDashboardMutedStyle, agentDashboardFit("Config: "+configPath, width))
	} else if configPath := strings.TrimSpace(m.configPath); configPath != "" {
		layout.addStyled(width, agentDashboardMutedStyle, agentDashboardFit("Config: "+configPath, width))
	}
	if controlAddr := strings.TrimSpace(m.status.ControlAddr); controlAddr != "" {
		layout.addStyled(width, agentDashboardMutedStyle, agentDashboardFit("Control: "+controlAddr, width))
	}
	layout.addLine(agentDashboardMutedStyle.Render(strings.Repeat("-", min(width, 120))))

	if m.err != nil && m.status.ControlAddr == "" {
		layout.addStyled(width, agentDashboardErrorStyle, fmt.Sprintf("Agent unavailable: %v", m.err))
		layout.addLine("")
		layout.addText(width, "Start managed service: portal agent run --config "+m.configPath)
		layout.addText(width, "No service manager:   portal agent run --foreground --config "+m.configPath)
		return layout
	}

	if m.err != nil {
		layout.addStyled(width, agentDashboardErrorStyle, fmt.Sprintf("Error: %v", m.err))
	}
	layout.addLine("")
	if m.height > 0 {
		bodyHeight = max(1, m.height-len(layout.lines))
	}

	tunnels := m.renderTunnelsSection(width, m.tunnelsSectionHeight(bodyHeight))
	layout.addView(tunnels)
	layout.addLine("")
	if m.height > 0 {
		bodyHeight = max(1, m.height-len(layout.lines))
	}
	body := m.renderTunnelPane(width, bodyHeight)
	layout.addView(body)
	return layout
}

func (m agentDashboardModel) tunnelsSectionHeight(bodyHeight int) int {
	if bodyHeight <= 0 {
		return bodyHeight
	}
	if _, ok := m.selectedTunnelStatus(); !ok {
		return bodyHeight
	}
	minRelaySectionHeight := agentDashboardMinRelayRows + 3
	if bodyHeight <= minRelaySectionHeight {
		return 1
	}
	return max(1, bodyHeight-minRelaySectionHeight-1)
}

func (m agentDashboardModel) renderTunnelsSection(width, height int) agentDashboardView {
	var pane agentDashboardView
	pane.addSectionTitle(width, agentDashboardPaneTunnels, "Tunnels", m.activePane == agentDashboardPaneTunnels)
	addLabel := "Add Tunnel"
	addDisabled := false
	if m.addingTunnel {
		addLabel = "Create"
		addDisabled = strings.TrimSpace(m.input.Value()) == ""
	}
	buttons := []agentDashboardButton{
		{label: addLabel, action: agentDashboardActionAddTunnel, disabled: addDisabled},
	}
	if m.addingTunnel {
		buttons = append(buttons, agentDashboardButton{label: "Cancel", action: agentDashboardActionCancelAddTunnel})
	}
	buttons = append(buttons,
		agentDashboardButton{label: "Delete", action: agentDashboardActionDeleteTunnel, disabled: len(m.status.Tunnels) == 0},
	)
	pane.addButtons(width, buttons...)
	if m.addingTunnel {
		pane.addLine(m.input.View())
	}
	tunnelRowWidth := agentDashboardTunnelTableWidth(width, m.status.Tunnels)
	pane.addLine(agentDashboardMutedStyle.Render(agentDashboardTunnelRow(tunnelRowWidth, "STATUS", "TARGET", "TUNNEL")))

	if len(m.status.Tunnels) == 0 {
		pane.addStyled(width, agentDashboardMutedStyle, "no tunnels")
		return pane
	}

	selectedTunnelID := m.selectedTunnelID
	if selectedTunnelID == "" && len(m.status.Tunnels) > 0 {
		selectedTunnelID = m.status.Tunnels[0].ID
	}
	maxRows := len(m.status.Tunnels)
	if height > 0 {
		maxRows = max(1, height-len(pane.lines)-2)
	}
	selectedIndex := m.selectedTunnelIndex()
	start, end := agentDashboardRelayWindow(selectedIndex, len(m.status.Tunnels), maxRows)
	rowWidth := tunnelRowWidth
	for i := start; i < end; i++ {
		tunnel := m.status.Tunnels[i]
		pane.addTunnelRow(width, rowWidth, tunnel, tunnel.ID == selectedTunnelID)
	}

	if tunnel, ok := m.selectedTunnelStatus(); ok && strings.TrimSpace(tunnel.LastError) != "" {
		pane.addStyled(width, agentDashboardErrorStyle, "Error: "+tunnel.LastError)
	}
	return pane
}

func (m agentDashboardModel) renderTunnelPane(width, height int) agentDashboardView {
	var pane agentDashboardView
	tunnel, ok := m.selectedTunnelStatus()
	if !ok {
		pane.addSectionTitle(width, agentDashboardPaneRelays, "Relays", m.activePane == agentDashboardPaneRelays)
		pane.addStyled(width, agentDashboardMutedStyle, "select a tunnel")
		pane.clip(height)
		return pane
	}

	relayLimit := m.relayRowsForHeight(tunnel, height)
	m.renderRelaysSection(&pane, width, relayLimit, tunnel)
	pane.addLine("")
	m.renderSettingsSection(&pane, width, max(1, height-len(pane.lines)), tunnel)
	pane.addLine("")
	m.renderRouteSection(&pane, width, max(1, height-len(pane.lines)), tunnel)
	pane.clip(height)
	return pane
}

func (m agentDashboardModel) relayRowsForHeight(tunnel types.AgentTunnelStatus, height int) int {
	if len(tunnel.Relays) == 0 {
		return 0
	}
	routeRows := len(m.displayedRoute(tunnel))
	routeReserve := min(max(5, routeRows+4), 9)
	settingsReserve := 9
	relayRows := height - routeReserve - settingsReserve - 4
	if relayRows < agentDashboardMinRelayRows {
		relayRows = min(agentDashboardMinRelayRows, len(tunnel.Relays))
	}
	return min(relayRows, len(tunnel.Relays))
}

func (m agentDashboardModel) renderRelaysSection(pane *agentDashboardView, width, maxRows int, tunnel types.AgentTunnelStatus) {
	relay, hasRelay := m.selectedRelayStatus()
	connectDisabled := !hasRelay || relay.Banned || relayDashboardActive(tunnel, relay)
	disconnectDisabled := !hasRelay || relay.Banned || !relayDashboardActive(tunnel, relay) || slices.Contains(m.displayedRoute(tunnel), relay.RelayURL)

	pane.addSectionTitle(width, agentDashboardPaneRelays, "Relays", m.activePane == agentDashboardPaneRelays)
	pane.addButtons(width,
		agentDashboardButton{label: "Connect", action: agentDashboardActionConnectRelay, disabled: connectDisabled},
		agentDashboardButton{label: "Disconnect", action: agentDashboardActionDisconnectRelay, disabled: disconnectDisabled},
	)
	pane.addLine(agentDashboardMutedStyle.Render(agentDashboardRelayRow(width, "STATUS", "FEATURES", "TUNNEL URL")))

	if len(tunnel.Relays) == 0 {
		pane.addStyled(width, agentDashboardMutedStyle, "no relays")
		return
	}
	selectedRelayURL := m.selectedRelayURL
	if selectedRelayURL == "" && len(tunnel.Relays) > 0 {
		selectedRelayURL = tunnel.Relays[0].RelayURL
	}
	selectedRelayIndex := m.selectedRelayIndex(tunnel)
	start, end := agentDashboardRelayWindow(selectedRelayIndex, len(tunnel.Relays), maxRows)
	rowWidth := width
	for i := start; i < end; i++ {
		relay := tunnel.Relays[i]
		line := agentDashboardRelayRow(rowWidth,
			m.relayDashboardMode(tunnel, relay),
			relayDashboardFeatures(relay),
			relayDashboardURL(relay),
		)
		pane.addClickRow(line, width, agentDashboardRelayStyle(relay.RelayURL == selectedRelayURL, tunnel, relay), agentDashboardActionOpenTunnelURL, tunnel.ID, relay.RelayURL)
	}
}

func (m agentDashboardModel) renderSettingsSection(pane *agentDashboardView, width, height int, tunnel types.AgentTunnelStatus) {
	if height <= 0 {
		return
	}
	startLine := len(pane.lines)
	pane.addSectionTitle(width, agentDashboardPaneSettings, "Settings", m.activePane == agentDashboardPaneSettings)
	pane.addButtons(width,
		agentDashboardButton{label: "Apply", action: agentDashboardActionApplySettings, disabled: m.settingsEditTunnelID != tunnel.ID},
	)
	if len(pane.lines)-startLine >= height {
		return
	}

	m.renderSettingsInputRows(pane, width, height, startLine)
}

func (m agentDashboardModel) renderSettingsInputRows(pane *agentDashboardView, width, height, startLine int) {
	rows := []struct {
		label string
		input textinput.Model
		field int
	}{
		{label: "Max Relays", input: m.settingsMaxRelays, field: agentDashboardSettingsFieldMaxActiveRelays},
		{label: "Description", input: m.metadataDescription, field: agentDashboardSettingsFieldDescription},
		{label: "Tags", input: m.metadataTags, field: agentDashboardSettingsFieldTags},
		{label: "Owner", input: m.metadataOwner, field: agentDashboardSettingsFieldOwner},
		{label: "Thumbnail", input: m.metadataThumbnail, field: agentDashboardSettingsFieldThumbnail},
		{label: "Hidden", input: m.metadataHide, field: agentDashboardSettingsFieldHide},
	}
	for _, row := range rows {
		if len(pane.lines)-startLine >= height {
			return
		}
		pane.addSettingsInputRow(width, row.label, row.input, row.field, m.settingsFocus == row.field)
	}
}

func (m agentDashboardModel) renderRouteSection(pane *agentDashboardView, width, height int, tunnel types.AgentTunnelStatus) {
	if height <= 0 {
		return
	}
	route := m.displayedRoute(tunnel)
	relay, hasRelay := m.selectedRelayStatus()
	inRoute := hasRelay && slices.Contains(route, relay.RelayURL)
	canAdd := hasRelay && relay.SupportsOverlay && !inRoute

	startLine := len(pane.lines)
	pane.addSectionTitle(width, agentDashboardPaneMultiHop, "Multi-hop", m.activePane == agentDashboardPaneMultiHop)
	pane.addButtons(width,
		agentDashboardButton{label: "Add Hop", action: agentDashboardActionAddHop, disabled: !canAdd},
		agentDashboardButton{label: "Apply", action: agentDashboardActionApplyHop, disabled: len(route) < 2},
		agentDashboardButton{label: "Clear", action: agentDashboardActionClearHop, disabled: len(route) == 0},
	)

	if hasRelay {
		pane.addText(width, "Selected relay: "+relayDashboardURL(relay))
	} else {
		pane.addStyled(width, agentDashboardMutedStyle, "no relays")
	}

	routeLabel := "Multi-hop:"
	if m.draftTunnelID == tunnel.ID {
		routeLabel += " draft"
	}
	if len(route) == 0 {
		routeLabel = "Multi-hop: none"
	}
	pane.addText(width, routeLabel)
	for i, relayURL := range route {
		if len(pane.lines)-startLine >= height {
			return
		}
		pane.addText(width, fmt.Sprintf("%d. %s", i+1, relayURL))
	}
}

func (v *agentDashboardView) addLine(line string) {
	v.lines = append(v.lines, line)
}

func (v *agentDashboardView) addText(width int, text string) {
	v.addLine(agentDashboardFit(text, width))
}

func (v *agentDashboardView) addStyled(width int, style lipgloss.Style, text string) {
	v.addLine(style.Render(agentDashboardFit(text, width)))
}

func (v *agentDashboardView) addSectionTitle(width int, pane agentDashboardPane, title string, active bool) {
	if width <= 0 {
		width = 1
	}
	style := agentDashboardSectionStyle
	if active {
		style = agentDashboardSelectedStyle
	}
	line := agentDashboardFit(title, width)
	y := len(v.lines)
	v.lines = append(v.lines, style.Render(line))
	v.regions = append(v.regions, agentDashboardRegion{
		x0:     0,
		x1:     min(lipgloss.Width(line), width),
		y:      y,
		action: agentDashboardActionSelectPane,
		pane:   pane,
	})
}

func (v *agentDashboardView) addButtons(width int, buttons ...agentDashboardButton) {
	lines, regions := agentDashboardRenderButtons(width, len(v.lines), 0, buttons...)
	v.lines = append(v.lines, lines...)
	v.regions = append(v.regions, regions...)
}

func (v *agentDashboardView) addView(child agentDashboardView) {
	startY := len(v.lines)
	v.lines = append(v.lines, child.lines...)
	for _, region := range child.regions {
		region.y += startY
		v.regions = append(v.regions, region)
	}
}

func (v *agentDashboardView) addTunnelRow(width, rowWidth int, tunnel types.AgentTunnelStatus, selected bool) {
	if width <= 0 {
		width = 1
	}
	rowWidth = min(rowWidth, max(1, width))
	line := agentDashboardTunnelRow(rowWidth, tunnel.State, tunnel.TargetAddr, tunnelDashboardName(tunnel))
	style := agentDashboardTunnelStyle(selected, tunnel.State)
	y := len(v.lines)
	v.lines = append(v.lines, style.Width(rowWidth).Render(agentDashboardFit(line, rowWidth)))
	v.regions = append(v.regions, agentDashboardRegion{
		x0:     0,
		x1:     rowWidth,
		y:      y,
		action: agentDashboardActionSelectTunnel,
		tunnel: tunnel.ID,
	})
}

func (v *agentDashboardView) addClickRow(line string, width int, style lipgloss.Style, action agentDashboardAction, tunnel, relay string) {
	plain := agentDashboardFit(line, width)
	y := len(v.lines)
	v.lines = append(v.lines, style.Width(width).Render(plain))
	v.regions = append(v.regions, agentDashboardRegion{
		x0:     0,
		x1:     width,
		y:      y,
		action: action,
		tunnel: tunnel,
		relay:  relay,
	})
}

func (v *agentDashboardView) addSettingsInputRow(width int, label string, input textinput.Model, field int, focused bool) {
	if width <= 0 {
		width = 1
	}
	labelStyle := agentDashboardMutedStyle
	if focused {
		labelStyle = agentDashboardInputStyle
	}
	labelText := agentDashboardCell(label+":", 12)
	y := len(v.lines)
	v.lines = append(v.lines, labelStyle.Render(labelText)+" "+input.View())
	v.regions = append(v.regions, agentDashboardRegion{
		x0:     0,
		x1:     width,
		y:      y,
		action: agentDashboardActionFocusSettingsField,
		field:  field,
	})
}

func (v *agentDashboardView) clip(height int) {
	if height <= 0 || len(v.lines) <= height {
		return
	}
	v.lines = v.lines[:height]
	regions := v.regions[:0]
	for _, region := range v.regions {
		if region.y < height {
			regions = append(regions, region)
		}
	}
	v.regions = regions
}

func agentDashboardRenderButtons(width, y, x int, buttons ...agentDashboardButton) ([]string, []agentDashboardRegion) {
	if width <= 0 {
		width = 1
	}
	var line strings.Builder
	var lines []string
	var regions []agentDashboardRegion
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
			regions = append(regions, agentDashboardRegion{
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
	default:
		return agentDashboardMutedStyle
	}
}

func agentDashboardRelayStyle(selected bool, tunnel types.AgentTunnelStatus, relay types.AgentRelayStatus) lipgloss.Style {
	if selected {
		return agentDashboardSelectedStyle
	}
	if relay.Banned {
		return agentDashboardErrorStyle
	}
	if relayDashboardConnected(tunnel, relay) {
		return agentDashboardOKStyle
	}
	return agentDashboardMutedStyle
}

func relayDashboardActive(tunnel types.AgentTunnelStatus, relay types.AgentRelayStatus) bool {
	return relayDashboardConnected(tunnel, relay) || relay.Connecting
}

func relayDashboardConnected(tunnel types.AgentTunnelStatus, relay types.AgentRelayStatus) bool {
	return relay.PublicURL != "" || slices.Contains(tunnel.MultiHop, relay.RelayURL)
}

func relayDashboardFeatures(relay types.AgentRelayStatus) string {
	var features []string
	if relay.SupportsOverlay {
		features = append(features, "overlay")
	}
	if relay.SupportsUDP {
		features = append(features, "udp")
	}
	if relay.SupportsTCP {
		features = append(features, "tcp")
	}
	if len(features) == 0 {
		return "-"
	}
	return strings.Join(features, ",")
}

func relayDashboardURL(relay types.AgentRelayStatus) string {
	if publicURL := strings.TrimSpace(relay.PublicURL); publicURL != "" {
		return publicURL
	}
	return relay.RelayURL
}

func (m agentDashboardModel) relayDashboardMode(tunnel types.AgentTunnelStatus, relay types.AgentRelayStatus) string {
	var modes []string
	if relay.PublicURL != "" || relay.Connecting {
		modes = append(modes, "direct")
	}
	for i, relayURL := range tunnel.MultiHop {
		if relayURL != relay.RelayURL {
			continue
		}
		if i == 0 {
			modes = append(modes, "hop-entry")
		} else {
			modes = append(modes, "hop-relay")
		}
		break
	}
	if len(modes) > 0 {
		return strings.Join(modes, ",")
	}
	return "-"
}

func tunnelDashboardName(tunnel types.AgentTunnelStatus) string {
	if strings.TrimSpace(tunnel.Name) != "" {
		return tunnel.Name
	}
	return tunnel.ID
}

func agentDashboardTunnelTableWidth(width int, tunnels []types.AgentTunnelStatus) int {
	tableWidth := 56
	for _, tunnel := range tunnels {
		nameWidth := max(lipgloss.Width(tunnelDashboardName(tunnel)), lipgloss.Width("TUNNEL"))
		tableWidth = max(tableWidth, 11+1+22+1+nameWidth)
	}
	return max(1, min(tableWidth, width))
}

func agentDashboardTunnelRow(width int, state, target, name string) string {
	if width < 28 {
		return agentDashboardFit(state+" "+name, width)
	}
	if width < 56 {
		stateW := 11
		return agentDashboardCell(state, stateW) + " " +
			agentDashboardFit(name, width-stateW-1)
	}
	stateW := 11
	targetW := 22
	nameW := max(1, width-stateW-targetW-2)
	return agentDashboardCell(state, stateW) + " " +
		agentDashboardCell(target, targetW) + " " +
		agentDashboardFit(name, nameW)
}

func agentDashboardRelayWindow(selected, total, rows int) (int, int) {
	if total <= 0 || rows <= 0 {
		return 0, 0
	}
	if rows >= total {
		return 0, total
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	start := selected - rows/2
	if start < 0 {
		start = 0
	}
	if start+rows > total {
		start = total - rows
	}
	return start, start + rows
}

func agentDashboardRelayRow(width int, mode, features, displayURL string) string {
	if width < 28 {
		return agentDashboardFit(mode+" "+displayURL, width)
	}
	if width < 56 {
		modeW := 16
		return agentDashboardCell(mode, modeW) + " " +
			agentDashboardFit(displayURL, width-modeW-1)
	}
	modeW := 16
	featuresW := 15
	relayW := max(1, width-modeW-featuresW-2)
	return agentDashboardCell(mode, modeW) + " " +
		agentDashboardCell(features, featuresW) + " " +
		agentDashboardFit(displayURL, relayW)
}

func openDashboardURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return fmt.Errorf("url host is required")
	}

	var cmd *exec.Cmd
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", rawURL)
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", rawURL)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", rawURL)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
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
