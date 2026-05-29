export interface Metadata {
  description: string;
  tags: string[];
  thumbnail: string;
  owner: string;
}

export interface PublicLeaseData {
  FirstSeenAt: string;
  LastSeenAt: string;
  name?: string;
  Hostname: string;
  Metadata: unknown;
  Ready: number;
}

export interface AdminLeaseData extends PublicLeaseData {
  identity_key: string;
  address: string;
  BPS: number;
  ClientIP: string;
  ReportedIP: string;
  IsApproved: boolean;
  IsBanned: boolean;
  IsDenied: boolean;
  IsIPBanned: boolean;
}

export interface PublicSnapshotResponse {
  leases?: PublicLeaseData[];
  landing_page_enabled?: boolean;
}
