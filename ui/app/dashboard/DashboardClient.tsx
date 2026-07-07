"use client";

import Link from "next/link";
import { FormEvent, useEffect, useMemo, useState } from "react";
import { ApiProbe, ApiProbeResult, DEFAULT_API_BASE, cleanBaseUrl, decodeJwtPayload, defaultProbes, probeEndpoint } from "@/lib/api";

const modules = [
  { title: "HRMS", value: "Employees", helper: "Directory, attendance, payroll and project probes", accent: "bg-orange-100 text-orange-700" },
  { title: "CRM", value: "Contacts", helper: "Lead and account probes are ready to map", accent: "bg-blue-100 text-blue-700" },
  { title: "University", value: "Students", helper: "Admissions and class management space", accent: "bg-emerald-100 text-emerald-700" },
  { title: "Courses", value: "Learning", helper: "Course, batch and enrollment discovery", accent: "bg-violet-100 text-violet-700" }
];

const sampleEmployees = [
  { id: "EMP-001", name: "Anthony Lewis", role: "Finance Manager", department: "Finance", status: "Active" },
  { id: "EMP-002", name: "Loren Gatlin", role: "HR Manager", department: "Human Resources", status: "Active" },
  { id: "EMP-003", name: "Jeffery Lalor", role: "UI/UX Designer", department: "Design", status: "Inactive" }
];

export function DashboardClient() {
  const [apiBase, setApiBase] = useState(DEFAULT_API_BASE);
  const [token, setToken] = useState("");
  const [claims, setClaims] = useState<Record<string, unknown> | null>(null);
  const [results, setResults] = useState<ApiProbeResult[]>([]);
  const [isRunning, setIsRunning] = useState(false);
  const [customPath, setCustomPath] = useState("/admin/tenants");
  const [customMethod, setCustomMethod] = useState<ApiProbe["method"]>("GET");

  const tokenStatus = useMemo(() => {
    if (!token) {
      return "Missing token";
    }
    if (!claims?.exp || typeof claims.exp !== "number") {
      return "Token loaded";
    }
    const expiresAt = new Date(claims.exp * 1000);
    return `Expires ${expiresAt.toLocaleString()}`;
  }, [claims, token]);

  useEffect(() => {
    const storedBase = window.localStorage.getItem("setika_api_base") ?? DEFAULT_API_BASE;
    const storedToken = window.localStorage.getItem("setika_access_token") ?? "";
    setApiBase(storedBase);
    setToken(storedToken);
    setClaims(storedToken ? decodeJwtPayload(storedToken) : null);
  }, []);

  async function runProbes(probes = defaultProbes) {
    setIsRunning(true);
    const nextResults: ApiProbeResult[] = [];
    for (const probe of probes) {
      const result = await probeEndpoint(apiBase, probe, token);
      nextResults.push(result);
      setResults([...nextResults]);
    }
    setIsRunning(false);
  }

  async function handleCustomProbe(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const path = customPath.startsWith("/") ? customPath : `/${customPath}`;
    const result = await probeEndpoint(apiBase, {
      label: "Custom probe",
      method: customMethod,
      path,
      auth: true
    }, token);
    setResults((current) => [result, ...current]);
  }

  function logout() {
    window.localStorage.removeItem("setika_access_token");
    window.localStorage.removeItem("setika_refresh_token");
    window.localStorage.removeItem("setika_token_type");
    window.localStorage.removeItem("setika_login_response");
    window.location.href = "/";
  }

  return (
    <main className="min-h-screen bg-slate-100">
      <div className="grid min-h-screen lg:grid-cols-[260px_1fr]">
        <aside className="border-r border-slate-200 bg-white p-5">
          <div className="flex items-center gap-3">
            <span className="grid size-10 place-items-center rounded bg-orange-600 font-bold text-white">S</span>
            <div>
              <p className="text-sm font-semibold text-slate-950">Setika</p>
              <p className="text-xs text-slate-500">Discovery console</p>
            </div>
          </div>

          <nav className="mt-8 space-y-1 text-sm font-semibold">
            {["Dashboard", "Endpoint probes", "HRMS", "CRM", "University", "Courses"].map((item, index) => (
              <a key={item} className={index === 0 ? "block rounded bg-orange-50 px-3 py-2 text-orange-700" : "block rounded px-3 py-2 text-slate-600 hover:bg-slate-50"} href={`#${item.toLowerCase().replaceAll(" ", "-")}`}>
                {item}
              </a>
            ))}
          </nav>

          <div className="mt-8 rounded border border-slate-200 bg-slate-50 p-3">
            <p className="text-xs font-semibold uppercase tracking-[0.18em] text-slate-500">API base</p>
            <input
              className="mt-2 w-full rounded border border-slate-200 bg-white px-3 py-2 text-xs outline-none focus:border-orange-500"
              value={apiBase}
              onChange={(event) => {
                const nextBase = cleanBaseUrl(event.target.value);
                setApiBase(nextBase);
                window.localStorage.setItem("setika_api_base", nextBase);
              }}
            />
          </div>
        </aside>

        <section className="p-4 sm:p-6 lg:p-8">
          <header className="flex flex-col gap-4 border-b border-slate-200 pb-6 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <p className="text-xs font-semibold uppercase tracking-[0.2em] text-orange-600">SmartHR powered UI</p>
              <h1 className="mt-2 text-3xl font-bold tracking-tight text-slate-950">Setika dashboard</h1>
              <p className="mt-2 text-sm text-slate-500">{tokenStatus}</p>
            </div>
            <div className="flex flex-wrap gap-2">
              <Link className="rounded border border-slate-200 bg-white px-4 py-2 text-sm font-semibold text-slate-700" href="/">
                Login
              </Link>
              <button className="rounded bg-slate-950 px-4 py-2 text-sm font-semibold text-white" onClick={logout} type="button">
                Clear token
              </button>
            </div>
          </header>

          <section id="dashboard" className="mt-6 grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            {modules.map((module) => (
              <article key={module.title} className="rounded border border-slate-200 bg-white p-5 shadow-sm">
                <span className={`inline-flex rounded px-2 py-1 text-xs font-bold ${module.accent}`}>{module.title}</span>
                <p className="mt-4 text-2xl font-bold text-slate-950">{module.value}</p>
                <p className="mt-2 text-sm leading-6 text-slate-500">{module.helper}</p>
              </article>
            ))}
          </section>

          <section id="endpoint-probes" className="mt-6 grid gap-6 xl:grid-cols-[1fr_380px]">
            <article className="overflow-hidden rounded border border-slate-200 bg-white shadow-sm">
              <div className="flex flex-col gap-3 border-b border-slate-200 p-5 sm:flex-row sm:items-center sm:justify-between">
                <div>
                  <h2 className="text-lg font-semibold text-slate-950">Endpoint probes</h2>
                  <p className="mt-1 text-sm text-slate-500">Runs real requests against Setika and shows the response body.</p>
                </div>
                <button
                  className="rounded bg-orange-600 px-4 py-2 text-sm font-semibold text-white disabled:bg-slate-300"
                  disabled={isRunning}
                  onClick={() => runProbes()}
                  type="button"
                >
                  {isRunning ? "Running..." : "Run default probes"}
                </button>
              </div>

              <div className="overflow-x-auto">
                <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
                  <thead className="bg-slate-50 text-xs uppercase tracking-[0.14em] text-slate-500">
                    <tr>
                      {["Endpoint", "Auth", "Status", "Duration", "Response"].map((heading) => (
                        <th key={heading} className="px-5 py-3 font-semibold">{heading}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100">
                    {results.length === 0 && (
                      <tr>
                        <td className="px-5 py-8 text-center text-slate-500" colSpan={5}>No probes have run yet.</td>
                      </tr>
                    )}
                    {results.map((result, index) => (
                      <tr key={`${result.path}-${index}`} className="align-top">
                        <td className="px-5 py-4">
                          <p className="font-semibold text-slate-950">{result.label}</p>
                          <p className="mt-1 font-mono text-xs text-slate-500">{result.method} {result.path}</p>
                        </td>
                        <td className="px-5 py-4 text-slate-600">{result.auth ? "Bearer" : "Open"}</td>
                        <td className="px-5 py-4">
                          <span className={result.ok ? "rounded-full bg-emerald-50 px-3 py-1 text-xs font-semibold text-emerald-700" : "rounded-full bg-rose-50 px-3 py-1 text-xs font-semibold text-rose-700"}>
                            {result.status ?? "ERR"}
                          </span>
                        </td>
                        <td className="px-5 py-4 text-slate-500">{result.durationMs ?? 0} ms</td>
                        <td className="min-w-[280px] px-5 py-4">
                          <pre className="max-h-44 overflow-auto rounded bg-slate-950 p-3 text-xs leading-5 text-slate-100">{JSON.stringify(result.error ?? result.response, null, 2)}</pre>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </article>

            <aside className="space-y-6">
              <article className="rounded border border-slate-200 bg-white p-5 shadow-sm">
                <h2 className="text-lg font-semibold text-slate-950">Custom endpoint</h2>
                <form className="mt-4 space-y-3" onSubmit={handleCustomProbe}>
                  <select className="w-full rounded border border-slate-200 px-3 py-3 text-sm outline-none focus:border-orange-500" value={customMethod} onChange={(event) => setCustomMethod(event.target.value as ApiProbe["method"])}>
                    {["GET", "POST", "PUT", "PATCH", "DELETE"].map((method) => (
                      <option key={method}>{method}</option>
                    ))}
                  </select>
                  <input
                    className="w-full rounded border border-slate-200 px-3 py-3 text-sm outline-none focus:border-orange-500"
                    value={customPath}
                    onChange={(event) => setCustomPath(event.target.value)}
                    placeholder="/admin/tenants"
                  />
                  <button className="w-full rounded bg-slate-950 px-4 py-3 text-sm font-semibold text-white" type="submit">
                    Probe with token
                  </button>
                </form>
              </article>

              <article className="rounded border border-slate-200 bg-white p-5 shadow-sm">
                <h2 className="text-lg font-semibold text-slate-950">Token claims</h2>
                <pre className="mt-4 max-h-72 overflow-auto rounded bg-slate-950 p-3 text-xs leading-5 text-slate-100">{JSON.stringify(claims ?? { message: "Login first to load a JWT." }, null, 2)}</pre>
              </article>
            </aside>
          </section>

          <section id="hrms" className="mt-6 overflow-hidden rounded border border-slate-200 bg-white shadow-sm">
            <div className="flex flex-col gap-3 border-b border-slate-200 p-5 sm:flex-row sm:items-center sm:justify-between">
              <div>
                <p className="text-xs font-semibold uppercase tracking-[0.18em] text-orange-600">HRMS preview</p>
                <h2 className="mt-1 text-lg font-semibold text-slate-950">Employee directory surface</h2>
              </div>
              <button className="rounded bg-orange-600 px-4 py-2 text-sm font-semibold text-white" type="button">Add employee</button>
            </div>
            <div className="overflow-x-auto">
              <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
                <thead className="bg-slate-50 text-xs uppercase tracking-[0.14em] text-slate-500">
                  <tr>
                    {["Employee", "Employee ID", "Designation", "Department", "Status"].map((heading) => (
                      <th key={heading} className="px-5 py-3 font-semibold">{heading}</th>
                    ))}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100">
                  {sampleEmployees.map((employee) => (
                    <tr key={employee.id}>
                      <td className="px-5 py-4">
                        <div className="flex items-center gap-3">
                          <span className="grid size-9 place-items-center rounded-full bg-orange-100 text-sm font-bold text-orange-700">{employee.name.slice(0, 1)}</span>
                          <span className="font-semibold text-slate-950">{employee.name}</span>
                        </div>
                      </td>
                      <td className="px-5 py-4 text-slate-500">{employee.id}</td>
                      <td className="px-5 py-4 text-slate-600">{employee.role}</td>
                      <td className="px-5 py-4 text-slate-600">{employee.department}</td>
                      <td className="px-5 py-4">
                        <span className={employee.status === "Active" ? "rounded-full bg-emerald-50 px-3 py-1 text-xs font-semibold text-emerald-700" : "rounded-full bg-slate-100 px-3 py-1 text-xs font-semibold text-slate-600"}>
                          {employee.status}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </section>
      </div>
    </main>
  );
}
