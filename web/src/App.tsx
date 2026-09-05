import { SetupPage } from "./features/auth/SetupPage";

// Minimal path router; replaced by the full app shell in T14/T15.
export function App() {
  const path = window.location.pathname;
  if (path.startsWith("/setup")) {
    return (
      <div className="min-h-screen">
        <header className="border-b border-ink">
          <div className="mx-auto max-w-screen-xl px-4 py-3">
            <p className="font-mono text-xs uppercase tracking-widest text-neutral-500">
              Vol. 1 · Self-hosted
            </p>
            <h1 className="font-display text-3xl font-bold tracking-tight">Tiny Password</h1>
          </div>
        </header>
        <main className="mx-auto max-w-screen-xl px-4 py-16">
          <div className="max-w-xl">
            <h2 className="font-display text-2xl font-bold">首次初始化</h2>
            <p className="mb-6 mt-2 font-body text-neutral-600">
              创建实例的第一位管理员。此页面只能使用一次。
            </p>
            <SetupPage />
          </div>
        </main>
      </div>
    );
  }
  return (
    <div className="min-h-screen">
      <header className="border-b border-ink">
        <div className="mx-auto flex max-w-screen-xl items-center justify-between px-4 py-3">
          <div>
            <p className="font-mono text-xs uppercase tracking-widest text-neutral-500">
              Vol. 1 · Self-hosted
            </p>
            <h1 className="font-display text-3xl font-bold tracking-tight">Tiny Password</h1>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-screen-xl px-4 py-16">
        <p className="font-body text-lg leading-relaxed">
          请前往 <a className="underline decoration-accent decoration-2 underline-offset-4" href="/setup">初始化页面</a> 创建管理员。
        </p>
      </main>
    </div>
  );
}
