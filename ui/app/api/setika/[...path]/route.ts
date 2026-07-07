import { NextRequest } from "next/server";

const DEFAULT_TARGET = process.env.SETIKA_API_BASE ?? process.env.NEXT_PUBLIC_SETIKA_API_BASE ?? "http://localhost:8087";

type RouteContext = {
  params: {
    path: string[];
  };
};

export async function GET(request: NextRequest, context: RouteContext) {
  return proxySetikaRequest(request, context);
}

export async function POST(request: NextRequest, context: RouteContext) {
  return proxySetikaRequest(request, context);
}

export async function PUT(request: NextRequest, context: RouteContext) {
  return proxySetikaRequest(request, context);
}

export async function PATCH(request: NextRequest, context: RouteContext) {
  return proxySetikaRequest(request, context);
}

export async function DELETE(request: NextRequest, context: RouteContext) {
  return proxySetikaRequest(request, context);
}

async function proxySetikaRequest(request: NextRequest, context: RouteContext) {
  const targetBase = getSafeTargetBase(request.headers.get("X-Setika-Api-Base"));
  if (!targetBase) {
    return Response.json({ error: "Only localhost API targets are allowed from the dev proxy." }, { status: 400 });
  }

  const targetPath = `/${context.params.path.join("/")}`;
  const targetUrl = new URL(targetPath, targetBase);
  targetUrl.search = request.nextUrl.search;

  const headers = new Headers();
  const authorization = request.headers.get("authorization");
  const contentType = request.headers.get("content-type");
  if (authorization) {
    headers.set("authorization", authorization);
  }
  if (contentType) {
    headers.set("content-type", contentType);
  }

  const response = await fetch(targetUrl, {
    method: request.method,
    headers,
    body: ["GET", "HEAD"].includes(request.method) ? undefined : await request.text(),
    cache: "no-store"
  });

  const responseHeaders = new Headers();
  const responseContentType = response.headers.get("content-type");
  if (responseContentType) {
    responseHeaders.set("content-type", responseContentType);
  }

  return new Response(await response.arrayBuffer(), {
    status: response.status,
    statusText: response.statusText,
    headers: responseHeaders
  });
}

function getSafeTargetBase(value: string | null) {
  const candidate = value?.trim() || DEFAULT_TARGET;

  try {
    const url = new URL(candidate);
    const allowedHosts = new Set(["localhost", "127.0.0.1", "::1"]);
    if ((url.protocol === "http:" || url.protocol === "https:") && allowedHosts.has(url.hostname)) {
      return `${url.protocol}//${url.host}`;
    }
  } catch {
    return null;
  }

  return null;
}
