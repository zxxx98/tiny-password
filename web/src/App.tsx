import { AppShell } from "./app/AppShell";
import { Router } from "./app/router";
import { SetupPage } from "./features/auth/SetupPage";

// Route table; grows with T15 (auth/account/admin) and T16 (vault workspace).
const routes = [
  {
    path: "/setup",
    element: (
      <div className="mx-auto max-w-screen-xl px-4 py-16">
        <div className="max-w-xl">
          <h2 className="font-display text-2xl font-bold">首次初始化</h2>
          <p className="mb-6 mt-2 font-body text-neutral-600">
            创建实例的第一位管理员。此页面只能使用一次。
          </p>
          <SetupPage />
        </div>
      </div>
    ),
  },
];

export function App() {
  return (
    <AppShell>
      <Router routes={routes} />
    </AppShell>
  );
}
