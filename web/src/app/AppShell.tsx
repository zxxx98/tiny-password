import { useSession } from "./session";
import { Link, usePath } from "./router";
import type { ReactNode } from "react";

/**
 * AppShell: the responsive Newsprint frame. Desktop ≥768px uses a masthead
 * with an inline main nav; below 768px the nav collapses to a fixed bottom
 * bar. Workspace content grids onto the 12-column layout via WorkspaceGrid.
 */

export type NavItem = { to: string; label: string };

const baseNavItems: NavItem[] = [
  { to: "/vault", label: "保险库" },
  { to: "/generator", label: "生成器" },
  { to: "/account", label: "账户" },
];

const adminNavItems: NavItem[] = [{ to: "/admin/users", label: "管理" }];

export function navItemsFor(role: "admin" | "member" | null): NavItem[] {
  return role === "admin" ? [...baseNavItems, ...adminNavItems] : [...baseNavItems];
}

function NavLink({ item, active }: { item: NavItem; active: boolean }) {
  return (
    <Link
      to={item.to}
      ariaCurrent={active ? "page" : undefined}
      className={`flex min-h-[44px] items-center px-3 py-2 font-mono text-xs uppercase tracking-widest underline-offset-4 hover:bg-neutral-100 ${
        active ? "border-b-2 border-accent text-ink" : "text-neutral-600"
      }`}
    >
      {item.label}
    </Link>
  );
}

export function AppShell({ children }: { children: ReactNode }) {
  const { principal } = useSession();
  const items = navItemsFor(principal?.role ?? null);
  const path = usePath();
  return (
    <div className="flex min-h-screen flex-col">
      <header className="border-b-4 border-ink">
        <div className="mx-auto flex w-full max-w-screen-xl items-center justify-between px-4 py-3">
          <Link to="/vault" ariaLabel="Tiny Password 保险库首页">
            <p className="font-mono text-xs uppercase tracking-widest text-neutral-500">
              Vol. 1 · Self-hosted
            </p>
            <h1 className="font-display text-2xl font-bold tracking-tight">Tiny Password</h1>
          </Link>
          <nav aria-label="主导航" className="hidden md:block">
            <ul className="flex items-center gap-1">
              {items.map((item) => (
                <li key={item.to}>
                  <NavLink item={item} active={path.startsWith(item.to)} />
                </li>
              ))}
            </ul>
          </nav>
        </div>
      </header>

      <main className="mx-auto w-full max-w-screen-xl flex-1">{children}</main>

      <footer className="border-t border-ink px-4 pb-24 pt-3 md:pb-3">
        <div className="mx-auto max-w-screen-xl">
          <p className="font-mono text-xs uppercase tracking-widest text-neutral-500">
            Edition: V1 · 本地自托管 · 所有数据仅存于内存
          </p>
        </div>
      </footer>

      <nav
        aria-label="移动端主导航"
        className="fixed inset-x-0 bottom-0 z-40 border-t-2 border-ink bg-paper md:hidden"
      >
        <ul className="grid grid-flow-col auto-cols-fr">
          {items.map((item) => (
            <li key={item.to}>
              <Link
                to={item.to}
                ariaCurrent={path.startsWith(item.to) ? "page" : undefined}
                className={`flex min-h-[56px] flex-col items-center justify-center gap-0.5 font-mono text-[11px] uppercase tracking-widest ${
                  path.startsWith(item.to) ? "text-ink" : "text-neutral-500"
                }`}
              >
                <span aria-hidden={true} className="text-base leading-none">
                  ▪
                </span>
                {item.label}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
    </div>
  );
}

/**
 * WorkspaceGrid: the 12-column newspaper grid — list column with a collapsed
 * vertical border, detail column beside it. Below md the detail pane hides
 * and navigation moves to a full-page detail route instead (T16).
 */
export function WorkspaceGrid({ list, detail }: { list: ReactNode; detail?: ReactNode }) {
  return (
    <div className="mx-auto grid w-full max-w-screen-xl grid-cols-1 md:grid-cols-12">
      <div className="md:col-span-4 md:border-r md:border-ink" data-testid="workspace-list">
        {list}
      </div>
      <div className="hidden md:col-span-8 md:block" data-testid="workspace-detail">
        {detail ?? (
          <div className="flex h-full min-h-[200px] items-center justify-center p-8">
            <p className="font-body text-sm text-neutral-500">选择左侧条目查看详情。</p>
          </div>
        )}
      </div>
    </div>
  );
}
