"use client";

import { useRouter } from "next/navigation";
import { FormEvent, useMemo, useState } from "react";
import { DEFAULT_API_BASE, LoginResponse, cleanBaseUrl, loginToSetika } from "@/lib/api";

type LoginState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "success"; data: LoginResponse }
  | { status: "error"; message: string };

export function LoginForm() {
  const router = useRouter();
  const [apiBase, setApiBase] = useState(DEFAULT_API_BASE);
  const [identifier, setIdentifier] = useState("");
  const [password, setPassword] = useState("");
  const [state, setState] = useState<LoginState>({ status: "idle" });

  const canSubmit = useMemo(() => identifier.trim().length > 0 && password.length > 0 && state.status !== "loading", [identifier, password, state.status]);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canSubmit) {
      return;
    }

    setState({ status: "loading" });
    try {
      const data = await loginToSetika(apiBase, identifier, password);
      window.localStorage.setItem("setika_api_base", cleanBaseUrl(apiBase));
      window.localStorage.setItem("setika_access_token", data.access_token);
      window.localStorage.setItem("setika_refresh_token", data.refresh_token);
      window.localStorage.setItem("setika_token_type", data.token_type || "Bearer");
      window.localStorage.setItem("setika_login_response", JSON.stringify(data, null, 2));
      setState({ status: "success", data });
      router.push("/dashboard");
    } catch (error) {
      setState({ status: "error", message: error instanceof Error ? error.message : "Unable to login" });
    }
  }

  return (
    <main className="min-h-screen bg-slate-100 px-4 py-8 sm:px-6 lg:px-8">
      <div className="mx-auto grid min-h-[calc(100vh-4rem)] max-w-6xl overflow-hidden rounded border border-slate-200 bg-white shadow-panel lg:grid-cols-[1fr_440px]">
        <section className="flex flex-col justify-between bg-slate-950 p-6 text-white sm:p-8 lg:p-10">
          <div>
            <div className="flex items-center gap-3">
              <span className="grid size-11 place-items-center rounded bg-orange-500 text-lg font-bold">S</span>
              <div>
                <p className="text-sm font-semibold uppercase tracking-[0.2em] text-orange-200">Setika Console</p>
                <h1 className="mt-1 text-2xl font-bold">API login and module discovery</h1>
              </div>
            </div>

            <div className="mt-10 grid gap-4 sm:grid-cols-2">
              {[
                ["HRMS", "Employees, attendance, payroll and projects"],
                ["CRM", "Contacts, leads and follow-up workflows"],
                ["University", "Admissions, students, classes and fees"],
                ["Courses", "Learning paths, batches and course delivery"]
              ].map(([title, helper]) => (
                <article key={title} className="rounded border border-white/10 bg-white/5 p-4">
                  <p className="text-base font-semibold">{title}</p>
                  <p className="mt-2 text-sm leading-6 text-slate-300">{helper}</p>
                </article>
              ))}
            </div>
          </div>

          <div className="mt-10 rounded border border-white/10 bg-white/5 p-4 text-sm leading-6 text-slate-300">
            Built from the Spur UI SmartHR conversion library. The dashboard stores the token locally in the browser and uses it only for API probing.
          </div>
        </section>

        <section className="p-6 sm:p-8">
          <div>
            <p className="text-xs font-semibold uppercase tracking-[0.2em] text-orange-600">Authentication</p>
            <h2 className="mt-2 text-2xl font-bold text-slate-950">Sign in to Setika</h2>
            <p className="mt-2 text-sm leading-6 text-slate-500">Use the backend credentials and API base to open the discovery dashboard.</p>
          </div>

          <form className="mt-8 space-y-5" onSubmit={handleSubmit}>
            <label className="block">
              <span className="text-sm font-semibold text-slate-700">API base URL</span>
              <input
                className="mt-2 w-full rounded border border-slate-200 px-3 py-3 text-sm outline-none transition focus:border-orange-500 focus:ring-4 focus:ring-orange-100"
                value={apiBase}
                onChange={(event) => setApiBase(event.target.value)}
                placeholder="http://localhost:8087"
              />
            </label>

            <label className="block">
              <span className="text-sm font-semibold text-slate-700">Email or identifier</span>
              <input
                className="mt-2 w-full rounded border border-slate-200 px-3 py-3 text-sm outline-none transition focus:border-orange-500 focus:ring-4 focus:ring-orange-100"
                value={identifier}
                onChange={(event) => setIdentifier(event.target.value)}
                autoComplete="username"
                placeholder="admin@example.com"
              />
            </label>

            <label className="block">
              <span className="text-sm font-semibold text-slate-700">Password</span>
              <input
                className="mt-2 w-full rounded border border-slate-200 px-3 py-3 text-sm outline-none transition focus:border-orange-500 focus:ring-4 focus:ring-orange-100"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                type="password"
                autoComplete="current-password"
                placeholder="Password"
              />
            </label>

            <button
              className="w-full rounded bg-orange-600 px-4 py-3 text-sm font-semibold text-white transition hover:bg-orange-700 disabled:cursor-not-allowed disabled:bg-slate-300"
              type="submit"
              disabled={!canSubmit}
            >
              {state.status === "loading" ? "Signing in..." : "Sign in and inspect API"}
            </button>
          </form>

          <div className="mt-6 rounded border border-slate-200 bg-slate-50 p-4">
            {state.status === "idle" && <p className="text-sm text-slate-500">Ready to send `POST /setika/auth/login`.</p>}
            {state.status === "loading" && <p className="text-sm text-slate-500">Contacting Setika API...</p>}
            {state.status === "error" && <p className="text-sm font-semibold text-rose-700">{state.message}</p>}
            {state.status === "success" && <p className="text-sm font-semibold text-emerald-700">Login succeeded. Opening dashboard...</p>}
          </div>
        </section>
      </div>
    </main>
  );
}
