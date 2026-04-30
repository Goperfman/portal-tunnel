package types

import "time"

type AgentStatusResponse struct {
	ReleaseVersion string              `json:"release_version"`
	StartedAt      time.Time           `json:"started_at"`
	ControlAddr    string              `json:"control_addr"`
	Tunnels        []AgentTunnelStatus `json:"tunnels,omitempty"`
	Logs           []AgentLogEntry     `json:"logs,omitempty"`
	Summary        AgentMetricsSummary `json:"summary"`
}

type AgentMetricsSummary struct {
	TunnelCount  int `json:"tunnel_count"`
	RunningCount int `json:"running_count"`
	ErrorCount   int `json:"error_count"`
}

type AgentTunnelStatus struct {
	ID         string             `json:"id"`
	Name       string             `json:"name,omitempty"`
	State      string             `json:"state"`
	TargetAddr string             `json:"target_addr,omitempty"`
	UDPAddr    string             `json:"udp_addr,omitempty"`
	LastError  string             `json:"last_error,omitempty"`
	StartedAt  time.Time          `json:"started_at,omitempty"`
	UpdatedAt  time.Time          `json:"updated_at,omitempty"`
	Restarts   int                `json:"restarts,omitempty"`
	MultiHop   []string           `json:"multi_hop,omitempty"`
	Relays     []AgentRelayStatus `json:"relays,omitempty"`
	PublicURLs []string           `json:"public_urls,omitempty"`
}

type AgentRelayStatus struct {
	RelayURL            string    `json:"relay_url"`
	Address             string    `json:"address,omitempty"`
	Hostname            string    `json:"hostname,omitempty"`
	PublicURL           string    `json:"public_url,omitempty"`
	UDPAddr             string    `json:"udp_addr,omitempty"`
	TCPAddr             string    `json:"tcp_addr,omitempty"`
	ExpiresAt           time.Time `json:"expires_at,omitempty"`
	DescriptorExpiresAt time.Time `json:"descriptor_expires_at,omitempty"`
	LastSeenAt          time.Time `json:"last_seen_at,omitempty"`
	DiscoveryRTTMillis  int64     `json:"discovery_rtt_millis,omitempty"`
	MultiHop            []string  `json:"multi_hop,omitempty"`
	Active              bool      `json:"active"`
	Bootstrap           bool      `json:"bootstrap"`
	Confirmed           bool      `json:"confirmed"`
	Banned              bool      `json:"banned"`
	SupportsOverlay     bool      `json:"supports_overlay"`
	SupportsUDP         bool      `json:"supports_udp"`
	SupportsTCP         bool      `json:"supports_tcp"`
	Connected           bool      `json:"connected"`
}

type AgentLogEntry struct {
	Time     time.Time `json:"time"`
	TunnelID string    `json:"tunnel_id,omitempty"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
}

type AgentRelayRequest struct {
	RelayURL string `json:"relay_url"`
}

type AgentMultiHopRequest struct {
	Relays []string `json:"relays"`
}
