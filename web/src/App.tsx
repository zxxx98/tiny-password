export function App() {
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
          One-time initialization will appear here once the instance is started.
        </p>
      </main>
    </div>
  );
}
