export const API_PATHS = {
  public: {
    state: "/state",
  },
  admin: {
    authChallenge: "/admin/auth/challenge",
    authLogin: "/admin/auth/login",
    logout: "/admin/auth/logout",
    authStatus: "/admin/auth/status",
  },
  policy: {
    root: "/policy",
    state: "/policy/state",
    leases: "/policy/leases",
    ips: "/policy/ips",
  },
  sdk: {
    domain: "/sdk/domain",
  },
  service: {
    status: "/service/status",
  },
  thumbnail: {
    prefix: "/thumbnail/",
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
