import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import {
  createServer,
  request as httpRequest,
  type IncomingMessage,
  type RequestOptions as HTTPRequestOptions,
  type ServerResponse,
} from "node:http";
import { request as httpsRequest, type RequestOptions as HTTPSRequestOptions } from "node:https";
import { dirname } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { URL } from "node:url";
import { API_PATHS } from "../src/lib/apiPaths.js";
import { parseLeaseMetadata } from "../src/lib/metadata.js";

const PORT = parseIntegerEnv("PORT", 8081);
const PORTAL_API_BASE_URL = normalizeBaseURL(
  process.env.PORTAL_API_BASE_URL || "https://portal:4017"
);
const HEADLESS_SHELL_URL = (process.env.HEADLESS_SHELL_URL || "").trim();
const FRONTEND_STATE_PATH = (process.env.PORTAL_FRONTEND_STATE_PATH || "").trim();
const DEFAULT_LANDING_PAGE_ENABLED = parseBooleanEnv("LANDING_PAGE_ENABLED", false);

const VIEWPORT_WIDTH = 1280;
const VIEWPORT_HEIGHT = 720;
const JPEG_QUALITY = 80;
const MAX_BYTES = 256 << 10;
const BODY_LIMIT = 1 << 16;
const COOLDOWN_MS = 30_000;
const PAGE_TIMEOUT_MS = 15_000;
const CDP_TIMEOUT_MS = 5_000;
const HTTP_TIMEOUT_MS = 5_000;
const JSON_LIMIT = 1 << 20;
const THUMBNAIL_CONTENT_TYPE = "image/jpeg";
const CORS_ALLOW_HEADERS = "Accept, Authorization, Content-Type, X-Portal-Access-Token";
const CORS_ALLOW_METHODS = "GET, HEAD, POST, DELETE, OPTIONS";

type APIEnvelope<T> =
  | { ok: true; data: T }
  | { ok: false; error?: { code?: string; message?: string }; data?: unknown };

interface RelayPublicStateResponse {
  leases?: Lease[];
}

interface FrontendPublicStateResponse extends RelayPublicStateResponse {
  landing_page_enabled: boolean;
}

interface Lease {
  hostname?: string;
  metadata?: unknown;
  ready?: number;
}

interface PolicyPortSettings {
  enabled: boolean;
  max_leases: number;
}

interface RelayPolicySettings {
  approval_mode?: string;
  udp?: PolicyPortSettings;
  tcp_port?: PolicyPortSettings;
}

interface FrontendPolicySettings extends RelayPolicySettings {
  landing_page_enabled: boolean;
  udp: PolicyPortSettings;
  tcp_port: PolicyPortSettings;
}

interface RelayPolicyStateResponse {
  policy?: RelayPolicySettings;
  leases?: unknown[];
}

interface FrontendPolicyStateResponse {
  policy: FrontendPolicySettings;
  leases?: unknown[];
}

interface ServiceStatusResponse {
  hostname: string;
  registered: boolean;
  service_alive: boolean;
}

interface FrontendState {
  landing_page_enabled: boolean;
}

interface ThumbnailEntry {
  data?: Buffer;
  fetchedAt: number;
}

interface CDPReply {
  id?: number;
  sessionId?: string;
  method?: string;
  params?: unknown;
  result?: Record<string, unknown>;
  error?: { message?: string };
}

interface CDPWaiter {
  method: string;
  sessionId?: string;
  resolve: (message: CDPReply) => void;
}

const thumbnailCache = new Map<string, ThumbnailEntry>();
const pendingThumbnails = new Map<string, Promise<Buffer>>();
let captureChain: Promise<unknown> = Promise.resolve();
let frontendState = loadFrontendState();

function parseIntegerEnv(name: string, fallback: number): number {
  const parsed = Number.parseInt(process.env[name] || "", 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

function parseBooleanEnv(name: string, fallback: boolean): boolean {
  const raw = (process.env[name] || "").trim().toLowerCase();
  if (["1", "true", "yes", "on"].includes(raw)) {
    return true;
  }
  if (["0", "false", "no", "off"].includes(raw)) {
    return false;
  }
  return fallback;
}

function normalizeBaseURL(raw: string): string {
  const trimmed = raw.trim();
  return trimmed.endsWith("/") ? trimmed.slice(0, -1) : trimmed;
}

function normalizeHostname(raw: string): string {
  return raw.trim().toLowerCase().replace(/\.$/, "");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function loadFrontendState(): FrontendState {
  if (!FRONTEND_STATE_PATH) {
    return { landing_page_enabled: DEFAULT_LANDING_PAGE_ENABLED };
  }
  try {
    const parsed = JSON.parse(readFileSync(FRONTEND_STATE_PATH, "utf8")) as unknown;
    if (isRecord(parsed) && typeof parsed.landing_page_enabled === "boolean") {
      return { landing_page_enabled: parsed.landing_page_enabled };
    }
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
      console.warn("failed to read frontend state", error);
    }
  }
  return { landing_page_enabled: DEFAULT_LANDING_PAGE_ENABLED };
}

function saveFrontendState(): void {
  if (!FRONTEND_STATE_PATH) {
    return;
  }
  try {
    mkdirSync(dirname(FRONTEND_STATE_PATH), { recursive: true });
    writeFileSync(FRONTEND_STATE_PATH, `${JSON.stringify(frontendState, null, 2)}\n`, {
      mode: 0o600,
    });
  } catch (error) {
    console.warn("failed to write frontend state", error);
  }
}

function hostnameMatchesPattern(pattern: string, hostname: string): boolean {
  const normalizedPattern = normalizeHostname(pattern);
  const normalizedHostname = normalizeHostname(hostname);
  if (!normalizedPattern || !normalizedHostname) {
    return false;
  }
  if (normalizedPattern === normalizedHostname) {
    return true;
  }
  if (!normalizedPattern.startsWith("*.")) {
    return false;
  }
  const suffix = normalizedPattern.slice(2);
  if (!suffix.includes(".")) {
    return false;
  }
  const dotIndex = normalizedHostname.indexOf(".");
  return dotIndex > 0 && normalizedHostname.slice(dotIndex + 1) === suffix;
}

function requestJSON<T>(
  rawURL: string,
  options: { hostHeader?: string } = {}
): Promise<T> {
  return new Promise((resolve, reject) => {
    const parsed = new URL(rawURL);
    const requestOptions: HTTPRequestOptions = {
      method: "GET",
      hostname: parsed.hostname,
      port: parsed.port,
      path: `${parsed.pathname}${parsed.search}`,
      headers: options.hostHeader ? { Host: options.hostHeader } : undefined,
      timeout: HTTP_TIMEOUT_MS,
    };

    const handleResponse = (res: IncomingMessage) => {
      collectResponseBody(res, JSON_LIMIT)
        .then((body) => {
          if ((res.statusCode || 0) < 200 || (res.statusCode || 0) >= 300) {
            reject(new Error(`${rawURL} status ${res.statusCode}: ${body}`));
            return;
          }
          resolve(JSON.parse(body.toString("utf8")) as T);
        })
        .catch(reject);
    };

    const req =
      parsed.protocol === "https:"
        ? httpsRequest(requestOptions as HTTPSRequestOptions, handleResponse)
        : httpRequest(requestOptions, handleResponse);

    req.on("timeout", () => req.destroy(new Error(`${rawURL} timed out`)));
    req.on("error", reject);
    req.end();
  });
}

function collectResponseBody(res: IncomingMessage, limit: number): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let size = 0;
    res.on("data", (chunk: Buffer) => {
      size += chunk.length;
      if (size > limit) {
        res.destroy(new Error("response too large"));
        return;
      }
      chunks.push(chunk);
    });
    res.on("error", reject);
    res.on("end", () => resolve(Buffer.concat(chunks)));
  });
}

function readRequestBody(req: IncomingMessage, limit: number): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let size = 0;
    req.on("data", (chunk: Buffer) => {
      size += chunk.length;
      if (size > limit) {
        req.destroy(new Error("request body too large"));
        return;
      }
      chunks.push(chunk);
    });
    req.on("error", reject);
    req.on("end", () => resolve(Buffer.concat(chunks)));
  });
}

function readJSONRequest(req: IncomingMessage): Promise<Record<string, unknown>> {
  return readRequestBody(req, BODY_LIMIT).then((body) => {
    const parsed = JSON.parse(body.toString("utf8")) as unknown;
    return isRecord(parsed) ? parsed : {};
  });
}

function relayURL(path: string): string {
  return `${PORTAL_API_BASE_URL}${path}`;
}

function requestRelay<T>(
  path: string,
  options: { method?: string; body?: unknown; authorization?: string } = {}
): Promise<{ statusCode: number; envelope: APIEnvelope<T> }> {
  return new Promise((resolve, reject) => {
    const parsed = new URL(relayURL(path));
    const body =
      options.body === undefined ? undefined : Buffer.from(JSON.stringify(options.body));
    const headers: Record<string, string> = {};
    if (options.authorization) {
      headers.Authorization = options.authorization;
    }
    if (body) {
      headers["Content-Type"] = "application/json";
      headers["Content-Length"] = String(body.length);
    }

    const requestOptions: HTTPRequestOptions = {
      method: options.method || "GET",
      hostname: parsed.hostname,
      port: parsed.port,
      path: `${parsed.pathname}${parsed.search}`,
      headers,
      timeout: HTTP_TIMEOUT_MS,
    };

    const handleResponse = (relayRes: IncomingMessage) => {
      collectResponseBody(relayRes, JSON_LIMIT)
        .then((responseBody) => {
          try {
            resolve({
              statusCode: relayRes.statusCode || 500,
              envelope: JSON.parse(responseBody.toString("utf8")) as APIEnvelope<T>,
            });
          } catch (error) {
            reject(error);
          }
        })
        .catch(reject);
    };

    const req =
      parsed.protocol === "https:"
        ? httpsRequest(
            {
              ...requestOptions,
              rejectUnauthorized: false,
            } satisfies HTTPSRequestOptions,
            handleResponse
          )
        : httpRequest(requestOptions, handleResponse);

    req.on("timeout", () => req.destroy(new Error(`${path} timed out`)));
    req.on("error", reject);
    if (body) {
      req.write(body);
    }
    req.end();
  });
}

function mergePublicState(state: RelayPublicStateResponse): FrontendPublicStateResponse {
  return {
    ...state,
    landing_page_enabled: frontendState.landing_page_enabled,
  };
}

function mergePolicySettings(settings: RelayPolicySettings = {}): FrontendPolicySettings {
  return {
    approval_mode: settings.approval_mode || "auto",
    udp: settings.udp || { enabled: false, max_leases: 0 },
    tcp_port: settings.tcp_port || { enabled: false, max_leases: 0 },
    landing_page_enabled: frontendState.landing_page_enabled,
  };
}

function writeEnvelope<T>(res: ServerResponse, status: number, envelope: APIEnvelope<T>): void {
  const body = Buffer.from(JSON.stringify(envelope));
  res.writeHead(status, {
    "Content-Type": "application/json",
    "Content-Length": String(body.length),
  });
  res.end(body);
}

function writeData<T>(res: ServerResponse, status: number, data: T): void {
  writeEnvelope(res, status, { ok: true, data });
}

function writeError(res: ServerResponse, status: number, code: string, message: string): void {
  writeEnvelope(res, status, { ok: false, error: { code, message } });
}

function writeRelayEnvelope<T>(
  res: ServerResponse,
  relayResponse: { statusCode: number; envelope: APIEnvelope<T> }
): void {
  writeEnvelope(res, relayResponse.statusCode, relayResponse.envelope);
}

function authorizationHeader(req: IncomingMessage): string {
  const authorization = req.headers.authorization;
  return typeof authorization === "string" ? authorization : "";
}

async function servePublicState(res: ServerResponse): Promise<void> {
  const relayResponse = await requestRelay<RelayPublicStateResponse>(API_PATHS.public.state);
  if (!relayResponse.envelope.ok) {
    writeRelayEnvelope(res, relayResponse);
    return;
  }
  writeData(res, relayResponse.statusCode, mergePublicState(relayResponse.envelope.data));
}

async function publicLeases(): Promise<Lease[]> {
  const relayResponse = await requestRelay<RelayPublicStateResponse>(API_PATHS.public.state);
  if (!relayResponse.envelope.ok) {
    return [];
  }
  return Array.isArray(relayResponse.envelope.data.leases)
    ? relayResponse.envelope.data.leases
    : [];
}

async function publicLeaseByHostname(hostname: string): Promise<Lease | undefined> {
  const normalizedHostname = normalizeHostname(hostname);
  if (!normalizedHostname) {
    return undefined;
  }
  const leases = await publicLeases();
  return leases.find((lease) => {
    const leaseHostname = typeof lease.hostname === "string" ? lease.hostname : "";
    return hostnameMatchesPattern(leaseHostname, normalizedHostname);
  });
}

async function serveServiceStatus(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const url = new URL(req.url || "/", "http://api.local");
  const hostname = normalizeHostname(url.searchParams.get("hostname") || "");
  if (!hostname) {
    writeError(res, 400, "invalid_request", "hostname is required");
    return;
  }

  const lease = await publicLeaseByHostname(hostname);
  writeData<ServiceStatusResponse>(res, 200, {
    hostname: typeof lease?.hostname === "string" ? lease.hostname : hostname,
    registered: Boolean(lease),
    service_alive: Boolean(lease && typeof lease.ready === "number" && lease.ready > 0),
  });
}

async function servePolicyState(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const relayResponse = await requestRelay<RelayPolicyStateResponse>(API_PATHS.policy.state, {
    authorization: authorizationHeader(req),
  });
  if (!relayResponse.envelope.ok) {
    writeRelayEnvelope(res, relayResponse);
    return;
  }
  writeData<FrontendPolicyStateResponse>(res, relayResponse.statusCode, {
    leases: relayResponse.envelope.data.leases,
    policy: mergePolicySettings(relayResponse.envelope.data.policy),
  });
}

async function servePolicy(req: IncomingMessage, res: ServerResponse): Promise<void> {
  if (req.method === "GET") {
    const relayResponse = await requestRelay<RelayPolicySettings>(API_PATHS.policy.root, {
      authorization: authorizationHeader(req),
    });
    if (!relayResponse.envelope.ok) {
      writeRelayEnvelope(res, relayResponse);
      return;
    }
    writeData(res, relayResponse.statusCode, mergePolicySettings(relayResponse.envelope.data));
    return;
  }

  let body: Record<string, unknown>;
  try {
    body = await readJSONRequest(req);
  } catch {
    writeError(res, 400, "invalid_json", "invalid request body");
    return;
  }

  const nextLandingPageEnabled =
    typeof body.landing_page_enabled === "boolean"
      ? body.landing_page_enabled
      : frontendState.landing_page_enabled;
  let relayBody = { ...body };
  delete relayBody.landing_page_enabled;
  if (!("approval_mode" in relayBody) || !("udp" in relayBody) || !("tcp_port" in relayBody)) {
    const current = await requestRelay<RelayPolicyStateResponse>(API_PATHS.policy.state, {
      authorization: authorizationHeader(req),
    });
    if (!current.envelope.ok) {
      writeRelayEnvelope(res, current);
      return;
    }
    const currentSettings = mergePolicySettings(current.envelope.data.policy);
    relayBody = {
      approval_mode: relayBody.approval_mode ?? currentSettings.approval_mode,
      udp: relayBody.udp ?? currentSettings.udp,
      tcp_port: relayBody.tcp_port ?? currentSettings.tcp_port,
    };
  }
  const relayResponse = await requestRelay<RelayPolicySettings>(API_PATHS.policy.root, {
    method: "POST",
    body: relayBody,
    authorization: authorizationHeader(req),
  });
  if (!relayResponse.envelope.ok) {
    writeRelayEnvelope(res, relayResponse);
    return;
  }

  frontendState = { landing_page_enabled: nextLandingPageEnabled };
  saveFrontendState();
  writeData(res, relayResponse.statusCode, mergePolicySettings(relayResponse.envelope.data));
}

async function forwardPolicyUpdate(
  req: IncomingMessage,
  res: ServerResponse,
  path: string
): Promise<void> {
  let body: Record<string, unknown>;
  try {
    body = await readJSONRequest(req);
  } catch {
    writeError(res, 400, "invalid_json", "invalid request body");
    return;
  }

  writeRelayEnvelope(
    res,
    await requestRelay<unknown>(path, {
      method: "POST",
      body,
      authorization: authorizationHeader(req),
    })
  );
}

async function leaseAllowsThumbnail(hostname: string): Promise<boolean> {
  const lease = await publicLeaseByHostname(hostname);
  return Boolean(lease && parseLeaseMetadata(lease.metadata).thumbnail.trim() === "");
}

async function resolveCDPWebSocketURL(): Promise<string> {
  if (!HEADLESS_SHELL_URL) {
    throw new Error("HEADLESS_SHELL_URL is not configured");
  }

  const parsed = new URL(HEADLESS_SHELL_URL);
  const versionURL = `http://${parsed.host}/json/version`;
  const info = await requestJSON<{ webSocketDebuggerUrl?: string }>(versionURL, {
    hostHeader: "127.0.0.1",
  });
  if (!info.webSocketDebuggerUrl) {
    throw new Error("/json/version did not return webSocketDebuggerUrl");
  }

  const wsURL = new URL(info.webSocketDebuggerUrl);
  wsURL.host = parsed.host;
  return wsURL.toString();
}

class CDPClient {
  private nextID = 1;
  private readonly pendingReplies = new Map<
    number,
    { resolve: (result: Record<string, unknown>) => void; reject: (error: Error) => void }
  >();
  private readonly waiters: CDPWaiter[] = [];
  private readonly socket: WebSocket;

  constructor(url: string) {
    this.socket = new WebSocket(url);
    this.socket.addEventListener("message", (event) => this.handleMessage(event));
  }

  connect(): Promise<void> {
    if (this.socket.readyState === WebSocket.OPEN) {
      return Promise.resolve();
    }
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        reject(new Error("connect to headless shell timed out"));
      }, CDP_TIMEOUT_MS);

      this.socket.addEventListener(
        "open",
        () => {
          clearTimeout(timeout);
          resolve();
        },
        { once: true }
      );
      this.socket.addEventListener(
        "error",
        () => {
          clearTimeout(timeout);
          reject(new Error("connect to headless shell failed"));
        },
        { once: true }
      );
    });
  }

  close(): void {
    this.socket.close();
  }

  send(
    method: string,
    params: Record<string, unknown> = {},
    sessionId = "",
    timeoutMs = CDP_TIMEOUT_MS
  ): Promise<Record<string, unknown>> {
    const id = this.nextID++;
    const payload: Record<string, unknown> = { id, method, params };
    if (sessionId) {
      payload.sessionId = sessionId;
    }

    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        this.pendingReplies.delete(id);
        reject(new Error(`${method} timed out`));
      }, timeoutMs);
      this.pendingReplies.set(id, {
        resolve: (result) => {
          clearTimeout(timeout);
          resolve(result);
        },
        reject: (error) => {
          clearTimeout(timeout);
          reject(error);
        },
      });
      this.socket.send(JSON.stringify(payload));
    });
  }

  waitForEvent(method: string, sessionId: string, timeoutMs: number): Promise<CDPReply> {
    return new Promise((resolve, reject) => {
      const waiter: CDPWaiter = { method, sessionId, resolve };
      this.waiters.push(waiter);
      const timeout = setTimeout(() => {
        const index = this.waiters.indexOf(waiter);
        if (index >= 0) {
          this.waiters.splice(index, 1);
        }
        reject(new Error(`${method} timed out`));
      }, timeoutMs);

      waiter.resolve = (message) => {
        clearTimeout(timeout);
        resolve(message);
      };
    });
  }

  private handleMessage(event: MessageEvent): void {
    const message = JSON.parse(String(event.data)) as CDPReply;
    if (typeof message.id === "number") {
      const pendingReply = this.pendingReplies.get(message.id);
      if (!pendingReply) {
        return;
      }
      this.pendingReplies.delete(message.id);
      if (message.error) {
        pendingReply.reject(new Error(message.error.message || "CDP command failed"));
        return;
      }
      pendingReply.resolve(message.result || {});
      return;
    }

    const waiter = this.waiters.find(
      (candidate) =>
        candidate.method === message.method &&
        (!candidate.sessionId || candidate.sessionId === message.sessionId)
    );
    if (!waiter) {
      return;
    }
    this.waiters.splice(this.waiters.indexOf(waiter), 1);
    waiter.resolve(message);
  }
}

function stringResultField(result: Record<string, unknown>, field: string): string {
  const value = result[field];
  return typeof value === "string" ? value : "";
}

async function captureScreenshot(hostname: string): Promise<Buffer> {
  const cdpURL = await resolveCDPWebSocketURL();
  const client = new CDPClient(cdpURL);

  let browserContextID = "";
  let targetID = "";

  try {
    await client.connect();
    await client.send("Security.setIgnoreCertificateErrors", { ignore: true }).catch(() => ({}));

    const context = await client.send("Target.createBrowserContext", {
      disposeOnDetach: true,
    });
    browserContextID = stringResultField(context, "browserContextId");

    const target = await client.send("Target.createTarget", {
      url: "about:blank",
      browserContextId: browserContextID,
    });
    targetID = stringResultField(target, "targetId");

    const attached = await client.send("Target.attachToTarget", {
      targetId: targetID,
      flatten: true,
    });
    const sessionID = stringResultField(attached, "sessionId");
    if (!sessionID) {
      throw new Error("CDP target attach did not return sessionId");
    }

    await client.send("Page.enable", {}, sessionID);
    await client.send(
      "Emulation.setDeviceMetricsOverride",
      {
        width: VIEWPORT_WIDTH,
        height: VIEWPORT_HEIGHT,
        deviceScaleFactor: 1,
        mobile: false,
      },
      sessionID
    );

    const loadEvent = client.waitForEvent("Page.loadEventFired", sessionID, PAGE_TIMEOUT_MS);
    await client.send("Page.navigate", { url: `https://${hostname}` }, sessionID);
    await loadEvent;
    await delay(1_000);

    const screenshot = await client.send(
      "Page.captureScreenshot",
      { format: "jpeg", quality: JPEG_QUALITY, fromSurface: true },
      sessionID
    );
    const data = stringResultField(screenshot, "data");
    if (!data) {
      throw new Error("CDP screenshot returned empty data");
    }
    return Buffer.from(data, "base64");
  } finally {
    if (targetID) {
      await client.send("Target.closeTarget", { targetId: targetID }).catch(() => ({}));
    }
    if (browserContextID) {
      await client
        .send("Target.disposeBrowserContext", { browserContextId: browserContextID })
        .catch(() => ({}));
    }
    client.close();
  }
}

async function captureAndStore(hostname: string): Promise<Buffer> {
  try {
    const data = await captureScreenshot(hostname);
    if (data.length > MAX_BYTES) {
      throw new Error(`thumbnail too large: ${data.length} bytes`);
    }
    thumbnailCache.set(hostname, { data, fetchedAt: Date.now() });
    console.log(`thumbnail captured hostname=${hostname} size=${data.length}`);
    return data;
  } catch (error) {
    thumbnailCache.set(hostname, { fetchedAt: Date.now() });
    console.warn(`thumbnail capture failed hostname=${hostname}`, error);
    throw error;
  }
}

function loadThumbnail(hostname: string): Promise<Buffer> {
  const entry = thumbnailCache.get(hostname);
  if (entry) {
    if (entry.data && entry.data.length > 0) {
      return Promise.resolve(entry.data);
    }
    if (Date.now() - entry.fetchedAt < COOLDOWN_MS) {
      return Promise.reject(new Error("thumbnail capture is cooling down"));
    }
  }

  const existing = pendingThumbnails.get(hostname);
  if (existing) {
    return existing;
  }

  const capture = captureChain.then(() => captureAndStore(hostname));
  captureChain = capture.catch(() => undefined);
  pendingThumbnails.set(hostname, capture);
  capture.then(
    () => pendingThumbnails.delete(hostname),
    () => pendingThumbnails.delete(hostname)
  );
  return capture;
}

function requestedThumbnailHostname(req: IncomingMessage): string {
  const url = new URL(req.url || "/", "http://api.local");
  if (!url.pathname.startsWith(API_PATHS.thumbnail.prefix)) {
    return "";
  }
  try {
    const hostname = normalizeHostname(
      decodeURIComponent(url.pathname.slice(API_PATHS.thumbnail.prefix.length))
    );
    return hostname.includes("*") ? "" : hostname;
  } catch {
    return "";
  }
}

function writeNotFound(res: ServerResponse): void {
  res.writeHead(404, { "Cache-Control": "no-store" });
  res.end();
}

function writeMethodNotAllowed(res: ServerResponse, allow = "GET"): void {
  res.setHeader("Allow", allow);
  writeError(res, 405, "method_not_allowed", "method not allowed");
}

async function serveThumbnail(req: IncomingMessage, res: ServerResponse): Promise<void> {
  if (req.method !== "GET") {
    writeMethodNotAllowed(res);
    return;
  }
  const hostname = requestedThumbnailHostname(req);
  if (!hostname || !HEADLESS_SHELL_URL) {
    writeNotFound(res);
    return;
  }
  if (!(await leaseAllowsThumbnail(hostname))) {
    writeNotFound(res);
    return;
  }

  const data = await loadThumbnail(hostname);
  res.writeHead(200, {
    "Content-Type": THUMBNAIL_CONTENT_TYPE,
    "Cache-Control": "public, max-age=300",
    "Content-Length": String(data.length),
  });
  res.end(data);
}

const server = createServer((req, res) => {
  void (async () => {
    res.setHeader("Access-Control-Allow-Origin", "*");
    res.setHeader("Access-Control-Allow-Methods", CORS_ALLOW_METHODS);
    res.setHeader("Access-Control-Allow-Headers", CORS_ALLOW_HEADERS);
    res.setHeader("Access-Control-Max-Age", "600");
    if (req.method === "OPTIONS") {
      res.writeHead(204);
      res.end();
      return;
    }

    const url = new URL(req.url || "/", "http://api.local");
    if (url.pathname === "/healthz") {
      writeData(res, 200, { status: "ok" });
      return;
    }
    if (url.pathname === API_PATHS.public.state) {
      if (req.method !== "GET") {
        writeMethodNotAllowed(res);
        return;
      }
      await servePublicState(res);
      return;
    }
    if (url.pathname === API_PATHS.service.status) {
      if (req.method !== "GET") {
        writeMethodNotAllowed(res);
        return;
      }
      await serveServiceStatus(req, res);
      return;
    }
    if (url.pathname === API_PATHS.policy.state) {
      if (req.method !== "GET") {
        writeMethodNotAllowed(res);
        return;
      }
      await servePolicyState(req, res);
      return;
    }
    if (url.pathname === API_PATHS.policy.root) {
      if (req.method !== "GET" && req.method !== "POST") {
        writeMethodNotAllowed(res, "GET, POST");
        return;
      }
      await servePolicy(req, res);
      return;
    }
    if (url.pathname === API_PATHS.policy.leases || url.pathname === API_PATHS.policy.ips) {
      if (req.method !== "POST") {
        writeMethodNotAllowed(res, "POST");
        return;
      }
      await forwardPolicyUpdate(req, res, url.pathname);
      return;
    }
    if (url.pathname.startsWith(API_PATHS.thumbnail.prefix)) {
      await serveThumbnail(req, res);
      return;
    }
    writeNotFound(res);
  })().catch((error) => {
    console.warn("portal api request failed", error);
    const url = new URL(req.url || "/", "http://api.local");
    if (url.pathname.startsWith(API_PATHS.thumbnail.prefix)) {
      writeNotFound(res);
      return;
    }
    writeError(res, 502, "upstream_error", "upstream request failed");
  });
});

server.listen(PORT, () => {
  console.log(
    `portal api listening on :${PORT} headless=${HEADLESS_SHELL_URL ? "enabled" : "disabled"} landing_page=${frontendState.landing_page_enabled}`
  );
});
