export const API_PATHS = {
  public: {
    state: "/state",
  },
  admin: {
    state: "/admin/state",
    authChallenge: "/admin/auth/challenge",
    authLogin: "/admin/auth/login",
    logout: "/admin/auth/logout",
    authStatus: "/admin/auth/status",
    settings: "/admin/settings",
    leasePolicy: "/admin/lease-policy",
    ipPolicy: "/admin/ip-policy",
  },
  sdk: {
    domain: "/sdk/domain",
  },
  service: {
    status: "/service/status",
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
