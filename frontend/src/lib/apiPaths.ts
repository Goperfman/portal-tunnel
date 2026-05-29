export const API_PATHS = {
  public: {
    snapshot: "/api/public/snapshot",
  },
  admin: {
    snapshot: "/admin/snapshot",
    authChallenge: "/admin/auth/challenge",
    authLogin: "/admin/auth/login",
    logout: "/admin/logout",
    authStatus: "/admin/auth/status",

    approvalMode: "/admin/settings/approval-mode",
    landingPage: "/admin/settings/landing-page",
    udpSettings: "/admin/settings/udp",
    tcpPortSettings: "/admin/settings/tcp-port",
  },
  sdk: {
    domain: "/sdk/domain",
  },
  tunnel: {
    status: "/tunnel/status",
  },
  agent: {
    authChallenge: "/v1/agent/auth/challenge",
    authLogin: "/v1/agent/auth/login",
    authLogout: "/v1/agent/auth/logout",
    authStatus: "/v1/agent/auth/status",
  },
  discovery: "/discovery",
  install: {
    shell: "/install.sh",
    powershell: "/install.ps1",
  },
} as const;

export const ROUTE_PATHS = {
  home: "/",
  serverDetail: "/server/:id",
  admin: "/admin",
} as const;

const ADMIN_LEASES_PATH = "/admin/leases";
const ADMIN_IPS_PATH = "/admin/ips";

function encodePathPart(value: string): string {
  return btoa(value).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function adminLeasePath(
  name: string,
  address: string,
  action: "ban" | "bps" | "approve" | "deny"
): string {
  const encodedName = encodePathPart(name);
  const encodedAddress = encodePathPart(address);
  return `${ADMIN_LEASES_PATH}/${encodeURIComponent(encodedName)}/${encodeURIComponent(encodedAddress)}/${action}`;
}

export function adminIPBanPath(ip: string): string {
  return `${ADMIN_IPS_PATH}/${encodeURIComponent(ip.trim())}/ban`;
}
