import { useEffect, useState } from "react";

/**
 * Minimal path router (no dependency): popstate-driven, with `:param`
 * segment patterns. Navigation state stays in the history stack — nothing
 * session-related is ever stored here.
 */
export function usePath(): string {
  const [path, setPath] = useState(() => window.location.pathname);
  useEffect(() => {
    const onPopState = () => setPath(window.location.pathname);
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  return path;
}

export function navigate(path: string): void {
  navigateWithState(path);
}

let transientNavigationState: unknown = null;

/**
 * Navigate with a one-shot in-memory state handoff. It intentionally never
 * enters history.state, localStorage, or sessionStorage: it is only for
 * moving sensitive generator output into an editor in the same tab.
 */
export function navigateWithState(path: string, state?: unknown): void {
  transientNavigationState = state ?? null;
  if (window.location.pathname === path) {
    window.dispatchEvent(new PopStateEvent("popstate"));
    return;
  }
  window.history.pushState({}, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

export function consumeNavigationState<T>(): T | null {
  const state = transientNavigationState as T | null;
  transientNavigationState = null;
  return state;
}

export function clearNavigationState(): void {
  transientNavigationState = null;
}

/** Matches "/vault/:itemId" against a concrete path; returns captured params. */
export function matchPath(pattern: string, path: string): Record<string, string> | null {
  const patternParts = pattern.split("/").filter(Boolean);
  const pathParts = path.split("/").filter(Boolean);
  if (patternParts.length !== pathParts.length) {
    return null;
  }
  const params: Record<string, string> = {};
  for (let i = 0; i < patternParts.length; i++) {
    const expected = patternParts[i];
    const actual = pathParts[i];
    if (expected.startsWith(":")) {
      params[expected.slice(1)] = decodeURIComponent(actual);
    } else if (expected !== actual) {
      return null;
    }
  }
  return params;
}

type RouteDef = {
  path: string;
  element: React.ReactNode;
};

const paramsContext = { current: {} as Record<string, string> };

/**
 * Params are exposed through a module-level holder: routes render one at a
 * time, so there is never ambiguity about which params are current. Hooks
 * reading it must be children of the matched route element.
 */
export function Router({ routes }: { routes: RouteDef[] }) {
  const path = usePath();
  for (const route of routes) {
    const params = matchPath(route.path, path);
    if (params) {
      paramsContext.current = params;
      return <>{route.element}</>;
    }
  }
  return (
    <div className="mx-auto max-w-screen-xl px-4 py-16">
      <h1 className="font-display text-4xl font-bold">404</h1>
      <p className="mt-3 font-body">这一页不存在。</p>
    </div>
  );
}

/** Reads the params of the currently rendered route. */
export function useRouteParams(): Record<string, string> {
  return paramsContext.current;
}

export type LinkProps = {
  to: string;
  children: React.ReactNode;
  className?: string;
  ariaLabel?: string;
  ariaCurrent?: "page" | true | undefined;
};

/** Same-origin link that routes through pushState instead of a full load. */
export function Link({ to, children, className, ariaLabel, ariaCurrent }: LinkProps) {
  return (
    <a
      href={to}
      aria-label={ariaLabel}
      aria-current={ariaCurrent}
      className={className}
      onClick={(event) => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) {
          return;
        }
        event.preventDefault();
        navigate(to);
      }}
    >
      {children}
    </a>
  );
}
