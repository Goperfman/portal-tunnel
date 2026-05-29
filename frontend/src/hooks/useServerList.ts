import { useEffect, useMemo, useState } from "react";
import { useList, type BaseServer } from "@/hooks/useList";
import { apiClient } from "@/lib/apiClient";
import { API_PATHS } from "@/lib/apiPaths";
import { parseLeaseMetadata } from "@/lib/metadata";
import type { PublicLeaseData, PublicSnapshotResponse } from "@/types/lease";

type PublicSnapshot = {
  leases: PublicLeaseData[];
  landingPageEnabled: boolean;
};

function convertPublicLeasesToServers(leases: PublicLeaseData[]): BaseServer[] {
  return leases.map((row) => {
    const metadata = parseLeaseMetadata(row.Metadata);
    const hostname = row.Hostname || "";
    const serviceName = row.name || "";

    return {
      id: hostname,
      name: serviceName || hostname || "(unnamed)",
      description: metadata.description || "",
      tags: metadata.tags,
      thumbnail: metadata.thumbnail || "",
      owner: metadata.owner || "",
      online: (row.Ready || 0) > 0,
      dns: hostname,
      link: hostname ? `https://${hostname}/` : "",
      lastUpdated: row.LastSeenAt || undefined,
      firstSeen: row.FirstSeenAt || undefined,
    };
  });
}

export function useServerList() {
  const [snapshot, setSnapshot] = useState<PublicSnapshot>({
    leases: [],
    landingPageEnabled: true,
  });

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      try {
        const data = await apiClient.get<PublicSnapshotResponse>(
          API_PATHS.public.snapshot
        );
        if (cancelled) {
          return;
        }
        setSnapshot({
          leases: Array.isArray(data?.leases) ? data.leases : [],
          landingPageEnabled: data?.landing_page_enabled ?? true,
        });
      } catch (error) {
        console.error("Failed to load public relay snapshot", error);
        if (!cancelled) {
          setSnapshot({ leases: [], landingPageEnabled: true });
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  const servers: BaseServer[] = useMemo(
    () => convertPublicLeasesToServers(snapshot.leases),
    [snapshot.leases]
  );

  const list = useList({
    servers,
    storageKey: "serverFavorites",
  });

  return {
    ...list,
    landingPageEnabled: snapshot.landingPageEnabled,
  };
}
