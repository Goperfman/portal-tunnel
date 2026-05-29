import { useEffect, useMemo, useState } from "react";
import { useList, type BaseServer } from "@/hooks/useList";
import type { BanFilter } from "@/types/filters";
import { API_PATHS } from "@/lib/apiPaths";
import { APIClientError, apiClient } from "@/lib/apiClient";
import { parseLeaseMetadata } from "@/lib/metadata";
import type {
  AdminIPPolicy,
  AdminLease,
  AdminLeasePolicy,
  AdminPortSettings,
  AdminSettings,
  AdminStateResponse,
  ApprovalMode,
} from "@/types/api";

export type { ApprovalMode } from "@/types/api";

type LeaseAction = "approve" | "deny" | "ban";

export interface AdminServer extends BaseServer {
  identityKey: string;
  address: string;
  isBanned: boolean;
  bps: number;
  isApproved: boolean;
  isDenied: boolean;
  ip: string;
  displayIP: string;
  isIPBanned: boolean;
}

export interface UDPSettings {
  enabled: boolean;
  maxLeases: number;
}

export interface TCPPortSettings {
  enabled: boolean;
  maxLeases: number;
}

const DEFAULT_ADMIN_SETTINGS: AdminSettings = {
  approval_mode: "auto",
  landing_page_enabled: true,
  udp: { enabled: false, max_leases: 0 },
  tcp_port: { enabled: false, max_leases: 0 },
};

const ADMIN_ERROR_MESSAGE_BY_CODE: Record<string, string> = {
  invalid_mode: "Invalid approval mode. Choose auto or manual and retry.",
  invalid_address: "Selected address is invalid. Refresh and try again.",
  invalid_request: "Selected lease is invalid. Refresh and try again.",
  lease_rejected: "Request was rejected by policy. Review conflicts and retry.",
  ip_banned: "Request denied because the source IP is banned.",
  unauthorized: "Admin authorization failed. Sign in again and retry.",
  method_not_allowed: "This action is not supported by the current server version.",
};

function toAdminErrorMessage(error: unknown, fallback: string): string {
  if (error instanceof APIClientError) {
    const mappedMessage = ADMIN_ERROR_MESSAGE_BY_CODE[error.code];
    if (mappedMessage) {
      return mappedMessage;
    }

    if (error.status === 401 || error.status === 403) {
      return "Admin authorization failed. Sign in again and retry.";
    }
    if (error.status === 409) {
      return "Request was rejected by policy. Refresh and retry.";
    }

    const message = error.message.trim();
    return message || fallback;
  }

  if (error instanceof Error) {
    const message = error.message.trim();
    return message || fallback;
  }

  return fallback;
}

function toAdminServer(
  row: AdminLease,
): AdminServer {
  const metadata = parseLeaseMetadata(row.metadata);
  const hostname = row.hostname || "";
  const serviceName = row.name || "";
  const address = row.address.trim();

  return {
    id: hostname,
    name: serviceName || hostname || "(unnamed)",
    description: metadata.description,
    tags: metadata.tags,
    thumbnail: metadata.thumbnail,
    owner: metadata.owner,
    online: (row.ready || 0) > 0,
    dns: hostname,
    link: hostname ? `https://${hostname}/` : "",
    lastUpdated: row.last_seen_at || undefined,
    firstSeen: row.first_seen_at || undefined,
    identityKey: row.identity_key.trim(),
    address,
    isBanned: row.is_banned,
    bps: row.bps,
    isApproved: row.is_approved,
    isDenied: row.is_denied,
    ip: row.client_ip,
    displayIP: row.reported_ip || row.client_ip,
    isIPBanned: row.is_ip_banned,
  };
}

function normalizeApprovalMode(value: string | undefined): ApprovalMode {
  return value === "manual" ? "manual" : "auto";
}

function normalizeAdminSettings(settings: AdminSettings | undefined): AdminSettings {
  return {
    approval_mode: normalizeApprovalMode(settings?.approval_mode),
    landing_page_enabled:
      settings?.landing_page_enabled ?? DEFAULT_ADMIN_SETTINGS.landing_page_enabled,
    udp: {
      enabled: settings?.udp?.enabled ?? DEFAULT_ADMIN_SETTINGS.udp.enabled,
      max_leases: settings?.udp?.max_leases ?? DEFAULT_ADMIN_SETTINGS.udp.max_leases,
    },
    tcp_port: {
      enabled: settings?.tcp_port?.enabled ?? DEFAULT_ADMIN_SETTINGS.tcp_port.enabled,
      max_leases: settings?.tcp_port?.max_leases ?? DEFAULT_ADMIN_SETTINGS.tcp_port.max_leases,
    },
  };
}

interface AdminState {
  serverData: AdminLease[];
  settings: AdminSettings;
}

async function loadAdminState(): Promise<AdminState> {
  const state = await apiClient.get<AdminStateResponse>(API_PATHS.admin.state);
  const normalizedLeases = Array.isArray(state?.leases) ? state.leases : [];

  return {
    serverData: normalizedLeases,
    settings: normalizeAdminSettings(state?.settings),
  };
}

export function useAdmin(enabled = true) {
  const [serverData, setServerData] = useState<AdminLease[]>([]);
  const [adminSettings, setAdminSettings] = useState<AdminSettings>(DEFAULT_ADMIN_SETTINGS);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const [banFilter, setBanFilter] = useState<BanFilter>("all");

  const applyAdminState = (state: AdminState) => {
    setServerData(state.serverData);
    setAdminSettings(state.settings);
  };

  const fetchData = async () => {
    setError("");

    try {
      applyAdminState(await loadAdminState());
    } catch (err: unknown) {
      setError(toAdminErrorMessage(err, "Failed to load admin data"));
    }
  };

  useEffect(() => {
    let mounted = true;
    if (!enabled) {
      setError("");
      setLoading(false);
      return () => {
        mounted = false;
      };
    }

    const loadInitialData = async () => {
      setError("");
      setLoading(true);
      try {
        const state = await loadAdminState();
        if (!mounted) {
          return;
        }
        applyAdminState(state);
      } catch (err: unknown) {
        if (!mounted) {
          return;
        }
        setError(toAdminErrorMessage(err, "Failed to load admin data"));
      } finally {
        if (mounted) {
          setLoading(false);
        }
      }
    };

    void loadInitialData();
    return () => {
      mounted = false;
    };
  }, [enabled]);

  const servers: AdminServer[] = useMemo(() => {
    return serverData.map((row) => toAdminServer(row));
  }, [serverData]);

  const additionalFilter = (server: AdminServer) => {
    switch (banFilter) {
      case "banned":
        return server.isBanned;
      case "active":
        return !server.isBanned;
      default:
        return true;
    }
  };

  const listState = useList({
    servers,
    storageKey: "adminFavorites",
    additionalFilter,
  });

  const runAdminAction = async (action: () => Promise<void>) => {
    setError("");
    try {
      await action();
      await fetchData();
    } catch (err: unknown) {
      const message = toAdminErrorMessage(err, "Action failed");
      console.error(err);
      setError(message);
      throw err;
    }
  };

  const postAdminSettings = async (settings: AdminSettings) => {
    const response = await apiClient.post<AdminSettings>(API_PATHS.admin.settings, settings);
    setAdminSettings(normalizeAdminSettings(response));
  };

  const currentAdminSettings = (overrides: Partial<AdminSettings> = {}): AdminSettings => ({
    ...adminSettings,
    ...overrides,
  });

  const updateLeasePolicy = async (
    identityKey: string,
    policy: Omit<AdminLeasePolicy, "identity_key">,
  ) => {
    if (!identityKey) {
      throw new Error("Missing lease identity");
    }
    await apiClient.post<unknown>(API_PATHS.admin.leasePolicy, {
      identity_key: identityKey,
      ...policy,
    } satisfies AdminLeasePolicy);
  };

  const handleBanFilterChange = (value: BanFilter) => {
    setBanFilter(value);
  };

  const handleBanStatus = (identityKey: string, isBan: boolean) =>
    runAdminAction(() => updateLeasePolicy(identityKey, { is_banned: isBan }));

  const handleBPSChange = async (identityKey: string, bps: number) => {
    if (!identityKey) {
      throw new Error("Missing lease identity");
    }

    const normalizedBPS = Math.max(0, Math.trunc(bps));
    const previousBPS =
      serverData.find((row) => row.identity_key.trim() === identityKey)?.bps ?? 0;

    setServerData((prev) =>
      prev.map((row) =>
        row.identity_key.trim() === identityKey
          ? { ...row, bps: normalizedBPS }
          : row
      )
    );

    try {
      await runAdminAction(async () => {
        if (!Number.isFinite(normalizedBPS) || normalizedBPS <= 0) {
          await updateLeasePolicy(identityKey, { bps: 0 });
          return;
        }
        await updateLeasePolicy(identityKey, { bps: normalizedBPS });
      });
    } catch (err) {
      setServerData((prev) =>
        prev.map((row) =>
          row.identity_key.trim() === identityKey
            ? { ...row, bps: previousBPS }
            : row
        )
      );
      throw err;
    }
  };

  const handleApprovalModeChange = async (mode: ApprovalMode) => {
    await runAdminAction(async () => {
      await postAdminSettings(currentAdminSettings({ approval_mode: mode }));
    });
  };

  const handleSettingsChange = (key: "udp" | "tcp_port") =>
    async (settings: { enabled: boolean; maxLeases: number }) => {
      await runAdminAction(async () => {
        const nextPortSettings: AdminPortSettings = {
          enabled: settings.enabled,
          max_leases: settings.maxLeases,
        };
        const nextSettings =
          key === "udp"
            ? currentAdminSettings({ udp: nextPortSettings })
            : currentAdminSettings({ tcp_port: nextPortSettings });
        await postAdminSettings(nextSettings);
      });
    };

  const handleUDPSettingsChange = handleSettingsChange("udp");
  const handleTCPPortSettingsChange = handleSettingsChange("tcp_port");

  const handleLandingPageEnabledChange = async (enabled: boolean) => {
    await runAdminAction(async () => {
      await postAdminSettings(currentAdminSettings({ landing_page_enabled: enabled }));
    });
  };

  const handleApproveStatus = (identityKey: string, approve: boolean) =>
    runAdminAction(() => updateLeasePolicy(identityKey, { is_approved: approve }));

  const handleDenyStatus = (identityKey: string, deny: boolean) =>
    runAdminAction(() => updateLeasePolicy(identityKey, { is_denied: deny }));

  const handleIPBanStatus = (ip: string, isBan: boolean) =>
    runAdminAction(async () => {
      const normalizedIP = ip.trim();
      if (!normalizedIP) {
        throw new Error("Missing IP address");
      }
      await apiClient.post<unknown>(API_PATHS.admin.ipPolicy, {
        ip: normalizedIP,
        is_banned: isBan,
      } satisfies AdminIPPolicy);
    });

  const runBulkLeaseAction = async (identityKeys: string[], action: LeaseAction) => {
    const normalizedIdentityKeys = [...new Set(
      identityKeys.filter((identityKey) => identityKey.length > 0)
    )];
    if (normalizedIdentityKeys.length === 0) {
      throw new Error("No valid leases selected");
    }

    const results = await Promise.allSettled(
      normalizedIdentityKeys.map((identityKey) => {
        const policy: AdminLeasePolicy =
          action === "approve"
            ? { identity_key: identityKey, is_approved: true }
            : action === "deny"
              ? { identity_key: identityKey, is_denied: true }
              : { identity_key: identityKey, is_banned: true };
        return apiClient.post<unknown>(API_PATHS.admin.leasePolicy, policy);
      })
    );

    const failed = results.find(
      (
        result
      ): result is PromiseRejectedResult =>
        result.status === "rejected"
    );
    if (failed) {
      throw failed.reason instanceof Error
        ? failed.reason
        : new Error(String(failed.reason));
    }
  };

  const handleBulkAction = (identityKeys: string[], action: LeaseAction) =>
    runAdminAction(() => runBulkLeaseAction(identityKeys, action));

  const handleBulkApprove = (identityKeys: string[]) => handleBulkAction(identityKeys, "approve");

  const handleBulkDeny = (identityKeys: string[]) => handleBulkAction(identityKeys, "deny");

  const handleBulkBan = (identityKeys: string[]) => handleBulkAction(identityKeys, "ban");

  const approvalMode = normalizeApprovalMode(adminSettings.approval_mode);
  const landingPageEnabled = adminSettings.landing_page_enabled;
  const udpSettings: UDPSettings = {
    enabled: adminSettings.udp.enabled,
    maxLeases: adminSettings.udp.max_leases,
  };
  const tcpPortSettings: TCPPortSettings = {
    enabled: adminSettings.tcp_port.enabled,
    maxLeases: adminSettings.tcp_port.max_leases,
  };

  return {
    servers,
    ...listState,
    banFilter,
    approvalMode,
    landingPageEnabled,
    udpSettings,
    tcpPortSettings,
    loading,
    error,
    handleBanFilterChange,
    handleBanStatus,
    handleBPSChange,
    handleApprovalModeChange,
    handleLandingPageEnabledChange,
    handleUDPSettingsChange,
    handleTCPPortSettingsChange,
    handleApproveStatus,
    handleDenyStatus,
    handleIPBanStatus,
    handleBulkApprove,
    handleBulkDeny,
    handleBulkBan,
  };
}
