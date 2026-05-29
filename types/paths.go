package types

const (
	PathRoot    = "/"
	PathV1Sign  = "/v1/sign"
	PathHealthz = "/healthz"
	PathState   = "/state"

	PathAdmin              = "/admin"
	PathAdminPrefix        = "/admin/"
	PathAdminAuthChallenge = "/admin/auth/challenge"
	PathAdminAuthLogin     = "/admin/auth/login"
	PathAdminLogout        = "/admin/auth/logout"
	PathAdminAuthStatus    = "/admin/auth/status"

	PathPolicy       = "/policy"
	PathPolicyPrefix = "/policy/"
	PathPolicyState  = PathPolicy + "/state"
	PathPolicyLeases = PathPolicy + "/leases"
	PathPolicyIPs    = PathPolicy + "/ips"

	PathInstallShell      = "/install.sh"
	PathInstallPowerShell = "/install.ps1"
	PathInstallBinPrefix  = "/install/bin/"

	PathSDKDomain            = "/sdk/domain"
	PathSDKRegisterChallenge = "/sdk/register/challenge"
	PathSDKRegister          = "/sdk/register"
	PathSDKRenew             = "/sdk/renew"
	PathSDKUnregister        = "/sdk/unregister"
	PathSDKHop               = "/sdk/hop"
	PathSDKConnect           = "/sdk/connect"

	PathDiscovery         = "/discovery"
	PathDiscoveryAnnounce = "/discovery/announce"

	PathX402Facilitator = "/x402"
	X402SupportedPath   = PathX402Facilitator + "/supported"
	X402VerifyPath      = PathX402Facilitator + "/verify"
	X402SettlePath      = PathX402Facilitator + "/settle"
)

const (
	PathAgentPrefix        = "/agent"
	PathAgentStatus        = PathAgentPrefix + "/status"
	PathAgentShutdown      = PathAgentPrefix + "/shutdown"
	PathAgentTunnels       = PathAgentPrefix + "/tunnels"
	PathAgentTunnelsPrefix = PathAgentPrefix + "/tunnels/"
	PathAgentAuthChallenge = PathAgentPrefix + "/auth/challenge"
	PathAgentAuthLogin     = PathAgentPrefix + "/auth/login"
	PathAgentAuthLogout    = PathAgentPrefix + "/auth/logout"
	PathAgentAuthStatus    = PathAgentPrefix + "/auth/status"
)
