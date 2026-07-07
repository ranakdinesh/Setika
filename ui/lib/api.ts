export const DEFAULT_API_BASE = process.env.NEXT_PUBLIC_SETIKA_API_BASE ?? "http://localhost:8087";

export type LoginResponse = {
  access_token: string;
  refresh_token: string;
  token_type: string;
  expires_in: number;
  password_migration_required?: boolean;
  password_migration_message?: string;
};

export type ApiProbe = {
  label: string;
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  path: string;
  auth: boolean;
  body?: unknown;
};

export type ApiProbeResult = ApiProbe & {
  status?: number;
  ok?: boolean;
  durationMs?: number;
  response?: unknown;
  error?: string;
};

export const defaultProbes: ApiProbe[] = [
  { label: "API health", method: "GET", path: "/healthz", auth: false },
  { label: "API readiness", method: "GET", path: "/readyz", auth: false },
  { label: "Admin tenants", method: "GET", path: "/admin/tenants", auth: true },
  { label: "HRMS employees", method: "GET", path: "/hrms/employees", auth: true },
  { label: "HRMS attendance", method: "GET", path: "/hrms/attendance", auth: true },
  { label: "HRMS payroll", method: "GET", path: "/hrms/payroll", auth: true },
  { label: "HRMS projects", method: "GET", path: "/hrms/projects", auth: true },
  { label: "CRM contacts", method: "GET", path: "/crm/contacts", auth: true },
  { label: "Courses", method: "GET", path: "/courses", auth: true }
];

export function cleanBaseUrl(value: string) {
  return value.trim().replace("://locahost", "://localhost").replace(/\/+$/, "");
}

export async function loginToSetika(apiBase: string, identifier: string, password: string) {
  const response = await fetch(toProxyUrl("/setika/auth/login"), {
    method: "POST",
    headers: proxyHeaders(apiBase, { "Content-Type": "application/json" }),
    body: JSON.stringify({ identifier, password })
  });

  const payload = await parseResponse(response);

  if (!response.ok) {
    throw new Error(toErrorMessage(payload, response.status));
  }

  return payload as LoginResponse;
}

export async function probeEndpoint(apiBase: string, probe: ApiProbe, token?: string): Promise<ApiProbeResult> {
  const startedAt = performance.now();

  try {
    const headers: Record<string, string> = proxyHeaders(apiBase);
    if (probe.auth && token) {
      headers.Authorization = `Bearer ${token}`;
    }
    if (probe.body !== undefined) {
      headers["Content-Type"] = "application/json";
    }

    const response = await fetch(toProxyUrl(probe.path), {
      method: probe.method,
      headers,
      body: probe.body === undefined ? undefined : JSON.stringify(probe.body)
    });

    return {
      ...probe,
      status: response.status,
      ok: response.ok,
      durationMs: Math.round(performance.now() - startedAt),
      response: await parseResponse(response)
    };
  } catch (error) {
    return {
      ...probe,
      ok: false,
      durationMs: Math.round(performance.now() - startedAt),
      error: error instanceof Error ? error.message : "Unknown request error"
    };
  }
}

function toProxyUrl(path: string) {
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  return `/api/setika${normalizedPath}`;
}

function proxyHeaders(apiBase: string, headers: Record<string, string> = {}) {
  return {
    ...headers,
    "X-Setika-Api-Base": cleanBaseUrl(apiBase)
  };
}

export function decodeJwtPayload(token: string) {
  const [, payload] = token.split(".");
  if (!payload) {
    return null;
  }

  try {
    const base64 = payload.replace(/-/g, "+").replace(/_/g, "/");
    const padded = base64.padEnd(base64.length + ((4 - (base64.length % 4)) % 4), "=");
    const json = decodeURIComponent(
      Array.from(atob(padded))
        .map((char) => `%${char.charCodeAt(0).toString(16).padStart(2, "0")}`)
        .join("")
    );
    return JSON.parse(json) as Record<string, unknown>;
  } catch {
    return null;
  }
}

async function parseResponse(response: Response) {
  const text = await response.text();
  if (!text) {
    return null;
  }

  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

function toErrorMessage(payload: unknown, status: number) {
  if (typeof payload === "string" && payload.trim()) {
    return payload;
  }
  if (payload && typeof payload === "object" && "error" in payload) {
    return String((payload as { error: unknown }).error);
  }
  if (payload && typeof payload === "object" && "message" in payload) {
    return String((payload as { message: unknown }).message);
  }
  return `Login failed with HTTP ${status}`;
}
