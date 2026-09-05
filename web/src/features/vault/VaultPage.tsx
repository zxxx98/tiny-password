import { useCallback, useEffect, useState } from "react";
import { request } from "../../app/api";
import { Link, navigate, useRouteParams } from "../../app/router";
import { useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { ConfirmDialog } from "../../design-system/Dialog";
import { ErrorSummary, Loading } from "../../design-system/Status";
import { HistoryPage } from "./HistoryPage";
import { ItemDetail } from "./ItemDetail";
import { ItemEditor } from "./ItemEditor";
import { TrashPage } from "./TrashPage";
import { ITEM_TYPES, TYPE_LABELS, errorText, type HealthReport, type ItemDetail as ItemDetailData, type ItemMeta } from "./types";

type Page = { items: ItemMeta[]; next_cursor: string | null };

function prefersWideDetail(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia?.("(min-width: 1024px)").matches;
}

/**
 * VaultPage: the personal vault workspace — newspaper grid with the list
 * column and the detail pane, search (POST, query stays out of URLs),
 * filters, health summary, trash and history entry points.
 */
export function VaultPage({ section }: { section?: "trash" }) {
  const { principal, csrfToken } = useSession();
  const routeParams = useRouteParams();

  const [items, setItems] = useState<ItemMeta[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [favoriteOnly, setFavoriteOnly] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [health, setHealth] = useState<HealthReport | null>(null);

  const [selected, setSelected] = useState<ItemDetailData | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [editing, setEditing] = useState(false);
  const [creating, setCreating] = useState(false);
  const [historyFor, setHistoryFor] = useState<string | null>(null);
  const [confirmTrash, setConfirmTrash] = useState(false);

  const fail = useCallback((err: unknown) => {
    const info = errorText(err);
    setError(info.message);
    setRequestId(info.requestId);
  }, []);

  const loadPage = useCallback(
    async (nextCursor: string | null, append: boolean) => {
      try {
        const params = new URLSearchParams();
        if (typeFilter) params.set("type", typeFilter);
        if (favoriteOnly) params.set("favorite", "true");
        if (nextCursor) params.set("cursor", nextCursor);
        let page: Page;
        if (query.trim()) {
          page = await request<Page>(
            "POST",
            "/api/v1/items/search",
            {
              query: query.trim(),
              type: typeFilter || undefined,
              tag: undefined,
              cursor: nextCursor ?? undefined,
            },
            { csrfToken },
          );
        } else {
          page = await request<Page>("GET", `/api/v1/items?${params.toString()}`, undefined, { csrfToken });
        }
        setItems((prev) => (append && prev ? [...prev, ...page.items] : page.items));
        setCursor(page.next_cursor);
        setError(null);
      } catch (err) {
        fail(err);
      }
    },
    [csrfToken, typeFilter, favoriteOnly, query, fail],
  );

  useEffect(() => {
    void loadPage(null, false);
  }, [loadPage]);

  useEffect(() => {
    request<HealthReport>("GET", "/api/v1/items/health", undefined, { csrfToken })
      .then(setHealth)
      .catch(() => setHealth(null));
  }, [csrfToken]);

  const openItem = useCallback(
    async (id: string) => {
      setDetailLoading(true);
      setEditing(false);
      setHistoryFor(null);
      try {
        const detail = await request<ItemDetailData>("GET", `/api/v1/items/${id}`, undefined, { csrfToken });
        setSelected(detail);
        // Tablet and phone: the detail is its own page (plan T14).
        if (!prefersWideDetail() && !section) {
          navigate(`/vault/${id}`);
        }
      } catch (err) {
        fail(err);
      } finally {
        setDetailLoading(false);
      }
    },
    [csrfToken, fail, section],
  );

  // Route /vault/:itemId renders the detail full-page (tablet/mobile).
  const routeItemId = routeParams["itemId"];
  useEffect(() => {
    if (routeItemId && (!selected || selected.id !== routeItemId)) {
      void openItem(routeItemId);
    }
  }, [routeItemId, selected, openItem]);

  const refreshAfterChange = useCallback(
    async (detail: ItemDetailData) => {
      setSelected(detail);
      setEditing(false);
      setCreating(false);
      setHistoryFor(null);
      await loadPage(null, false);
    },
    [loadPage],
  );

  const trashSelected = useCallback(async () => {
    if (!selected) {
      return;
    }
    try {
      await request("DELETE", `/api/v1/items/${selected.id}`, undefined, { csrfToken });
      setConfirmTrash(false);
      setSelected(null);
      await loadPage(null, false);
    } catch (err) {
      fail(err);
    }
  }, [selected, csrfToken, loadPage, fail]);

  const toggleFavorite = useCallback(async () => {
    if (!selected) {
      return;
    }
    try {
      await request("PUT", `/api/v1/items/${selected.id}/favorite`, { favorite: !selected.favorite }, { csrfToken });
      const detail = await request<ItemDetailData>("GET", `/api/v1/items/${selected.id}`, undefined, { csrfToken });
      setSelected(detail);
      await loadPage(null, false);
    } catch (err) {
      fail(err);
    }
  }, [selected, csrfToken, loadPage, fail]);

  const canManage = (meta: ItemMeta): boolean => {
    if (!principal) {
      return false;
    }
    return meta.vault_scope === "personal" ? meta.owner_id === principal.user_id : meta.creator_id === principal.user_id;
  };

  const detailPane = () => {
    if (historyFor) {
      return <HistoryPage itemId={historyFor} csrfToken={csrfToken} onRestored={(detail) => { setHistoryFor(null); void refreshAfterChange(detail); }} onClose={() => setHistoryFor(null)} />;
    }
    if (creating) {
      return (
        <ItemEditor
          csrfToken={csrfToken}
          onCancel={() => setCreating(false)}
          onSaved={(detail) => void refreshAfterChange(detail)}
        />
      );
    }
    if (editing && selected) {
      return (
        <ItemEditor
          csrfToken={csrfToken}
          initial={selected}
          onCancel={() => setEditing(false)}
          onSaved={(detail) => void refreshAfterChange(detail)}
        />
      );
    }
    if (detailLoading) {
      return <Loading label="正在解密条目…" />;
    }
    if (selected) {
      return (
        <ItemDetail
          detail={selected}
          csrfToken={csrfToken}
          canManage={canManage(selected)}
          onEdit={() => setEditing(true)}
          onShowHistory={() => setHistoryFor(selected.id)}
          onToggleFavorite={() => void toggleFavorite()}
          onTrash={() => setConfirmTrash(true)}
        />
      );
    }
    return (
      <div className="flex h-full min-h-[200px] items-center justify-center p-8">
        <p className="font-body text-sm text-neutral-500">选择左侧条目查看详情。</p>
      </div>
    );
  };

  if (section === "trash") {
    return (
      <AppFrame>
        <TrashPage csrfToken={csrfToken} onChanged={() => void loadPage(null, false)} />
      </AppFrame>
    );
  }

  return (
    <AppFrame>
      <div className="mx-auto grid w-full max-w-screen-xl grid-cols-1 lg:grid-cols-12">
        <div className="lg:col-span-4 lg:border-r lg:border-ink">
          <div className="space-y-4 p-4">
            <form
              className="flex gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                void loadPage(null, false);
              }}
            >
              <input
                type="search"
                aria-label="搜索条目"
                placeholder="搜索标题、用户名、网址、标签、备注"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                className="min-h-[44px] flex-1 border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm focus-visible:bg-neutral-100 focus-visible:outline-none"
              />
              <Button type="submit">搜索</Button>
            </form>

            <div className="flex flex-wrap gap-2">
              <select
                aria-label="按类型筛选"
                value={typeFilter}
                onChange={(e) => setTypeFilter(e.target.value)}
                className="min-h-[44px] border border-ink bg-paper px-2 font-mono text-xs"
              >
                <option value="">全部类型</option>
                {ITEM_TYPES.map((t) => (
                  <option key={t} value={t}>{TYPE_LABELS[t]}</option>
                ))}
              </select>
              <Button variant={favoriteOnly ? "primary" : "secondary"} onClick={() => setFavoriteOnly((v) => !v)}>
                收藏
              </Button>
              <Link to="/vault/trash" className="flex min-h-[44px] items-center px-3 font-mono text-xs uppercase tracking-widest underline-offset-4 hover:underline">
                回收站
              </Link>
            </div>

            {health && (health.weak > 0 || health.reused > 0 || health.expired > 0) && (
              <p className="border border-ink px-3 py-2 font-body text-xs" role="status">
                密码健康：弱密码 {health.weak} · 重复 {health.reused} · 已过期 {health.expired}
              </p>
            )}

            <Button onClick={() => { setCreating(true); setSelected(null); setHistoryFor(null); }}>新建条目</Button>

            <ErrorSummary message={error ?? ""} requestId={requestId} onDismiss={() => setError(null)} />
          </div>

          {items === null ? (
            <div className="px-4"><Loading label="正在加载条目…" /></div>
          ) : items.length === 0 ? (
            <p className="px-4 pb-8 font-body text-sm text-neutral-600">
              {query ? "没有匹配的条目。" : "保险库还是空的——创建第一个条目。"}
            </p>
          ) : (
            <ul className="divide-y divide-divider border-y border-ink">
              {items.map((item) => (
                <li key={item.id}>
                  <button
                    type="button"
                    onClick={() => void openItem(item.id)}
                    className={`block w-full px-4 py-3 text-left hover:bg-neutral-100 ${
                      selected?.id === item.id ? "border-l-4 border-accent" : ""
                    }`}
                  >
                    <p className="font-body font-semibold">
                      {item.favorite && <span aria-label="已收藏">★ </span>}
                      {item.title || "（无标题）"}
                    </p>
                    <p className="mt-1 font-mono text-xs text-neutral-500">
                      {TYPE_LABELS[item.item_type]} ·{" "}
                      {item.vault_scope === "shared"
                        ? `共享 · 创建者 ${item.creator_name ?? "成员"}`
                        : "个人"}
                    </p>
                    {item.creator_name && item.vault_scope === "shared" && item.creator_name !== principal?.username && (
                      <p className="mt-0.5 font-body text-xs text-neutral-500">来自 {item.creator_name} 的共享条目</p>
                    )}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {cursor && (
            <div className="p-4">
              <Button variant="secondary" onClick={() => void loadPage(cursor, true)}>加载更多</Button>
            </div>
          )}
        </div>

        {/* Desktop detail pane; below lg the detail renders as its own page. */}
        <div className="hidden lg:col-span-8 lg:block">{detailPane()}</div>

        {/* Route-driven full-page detail for tablet/mobile viewports. */}
        {routeItemId && (
          <div className="lg:hidden" data-testid="mobile-detail">
            <div className="p-4">
              <Button variant="ghost" onClick={() => { setSelected(null); navigate("/vault"); }}>
                ← 返回列表
              </Button>
            </div>
            {detailPane()}
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmTrash}
        danger
        title="移入回收站？"
        description="条目将从列表与详情中隐藏；30 天内可在回收站恢复。"
        confirmLabel="移入回收站"
        onCancel={() => setConfirmTrash(false)}
        onConfirm={() => void trashSelected()}
      />
    </AppFrame>
  );
}

function AppFrame({ children }: { children: React.ReactNode }) {
  return <div className="py-6">{children}</div>;
}
