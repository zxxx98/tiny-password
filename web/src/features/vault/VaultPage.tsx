import { useCallback, useEffect, useRef, useState } from "react";
import { request } from "../../app/api";
import {
  consumeNavigationState,
  Link,
  matchPath,
  navigate,
  replace,
  setNavigationGuard,
  usePath,
  type NavigationIntent,
  type NavigationMeta,
} from "../../app/router";
import { useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { ConfirmDialog } from "../../design-system/Dialog";
import { ErrorSummary, Loading } from "../../design-system/Status";
import { ItemDialog, type ItemDialogMode } from "./ItemDialog";
import { TrashPage } from "./TrashPage";
import {
  ITEM_TYPES,
  TYPE_LABELS,
  errorText,
  type HealthReport,
  type ItemDetail as ItemDetailData,
  type ItemMeta,
} from "./types";

type Page = { items: ItemMeta[]; next_cursor: string | null };
type ModalState = { kind: "closed" } | ItemDialogMode;
type ModalSource = "list" | "direct" | null;
type VaultScope = "personal" | "shared";
type MoveConfirmation = { from: VaultScope; to: VaultScope };
type PendingLeave =
  | { kind: "discard-edit"; detail: ItemDetailData }
  | { kind: "push"; path: string; meta?: NavigationMeta }
  | { kind: "pop"; delta: number };

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

/** The vault list stays mounted while one route-driven item dialog is open. */
export function VaultPage({ section }: { section?: "trash" }) {
  const { principal, csrfToken } = useSession();
  const path = usePath();
  const routeItemId = section ? undefined : matchPath("/vault/:itemId", path)?.itemId;

  const [items, setItems] = useState<ItemMeta[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [favoriteOnly, setFavoriteOnly] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const [listRequestId, setListRequestId] = useState<string | undefined>();
  const [health, setHealth] = useState<HealthReport | null>(null);
  const [modal, setModal] = useState<ModalState>({ kind: "closed" });
  const [dirty, setDirty] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [confirmTrash, setConfirmTrash] = useState(false);
  const [trashBusy, setTrashBusy] = useState(false);
  const [trashError, setTrashError] = useState<{ message: string; requestId?: string } | null>(null);
  const [moveConfirmation, setMoveConfirmation] = useState<MoveConfirmation | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);

  const dirtyRef = useRef(false);
  const modalSourceRef = useRef<ModalSource>(null);
  const initialRouteRef = useRef(true);
  const consumedNavigationState = useRef(false);
  const closingRef = useRef(false);
  const pendingLeaveRef = useRef<PendingLeave | null>(null);
  const restoringPopRef = useRef(false);
  const editorBusyRef = useRef(false);
  const mountedRef = useRef(true);
  const refreshSequenceRef = useRef(0);
  const listRegionRef = useRef<HTMLDivElement>(null);
  const focusListAfterDeleteRef = useRef(false);
  const listRequestRef = useRef<{ controller: AbortController; sequence: number } | null>(null);
  const listSequenceRef = useRef(0);
  const detailRequestRef = useRef<{ controller: AbortController; sequence: number } | null>(null);
  const detailSequenceRef = useRef(0);
  const favoriteRequestRef = useRef<{ controller: AbortController; sequence: number } | null>(null);
  const favoriteSequenceRef = useRef(0);
  const favoriteBusyRef = useRef(false);
  const trashRequestRef = useRef<{ controller: AbortController; sequence: number } | null>(null);
  const trashSequenceRef = useRef(0);
  const moveResolverRef = useRef<((confirmed: boolean) => void) | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      moveResolverRef.current?.(false);
      moveResolverRef.current = null;
    };
  }, []);

  const setDirtyState = useCallback((value: boolean) => {
    dirtyRef.current = value;
    setDirty(value);
  }, []);

  const setEditorBusy = useCallback((value: boolean) => {
    editorBusyRef.current = value;
  }, []);

  const requestMoveConfirmation = useCallback((from: VaultScope, to: VaultScope): Promise<boolean> => {
    moveResolverRef.current?.(false);
    setMoveConfirmation({ from, to });
    return new Promise<boolean>((resolve) => {
      moveResolverRef.current = resolve;
    });
  }, []);

  const resolveMoveConfirmation = useCallback((confirmed: boolean) => {
    const resolve = moveResolverRef.current;
    moveResolverRef.current = null;
    setMoveConfirmation(null);
    resolve?.(confirmed);
  }, []);

  const failList = useCallback((err: unknown) => {
    const info = errorText(err);
    setListError(info.message);
    setListRequestId(info.requestId);
  }, []);

  const abortListRequest = useCallback(() => {
    listRequestRef.current?.controller.abort();
    listRequestRef.current = null;
  }, []);

  const loadPage = useCallback(
    async (nextCursor: string | null, append: boolean) => {
      abortListRequest();
      const controller = new AbortController();
      const sequence = ++listSequenceRef.current;
      listRequestRef.current = { controller, sequence };
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
            { csrfToken, signal: controller.signal },
          );
        } else {
          page = await request<Page>("GET", `/api/v1/items?${params.toString()}`, undefined, {
            csrfToken,
            signal: controller.signal,
          });
        }
        if (controller.signal.aborted || listRequestRef.current?.sequence !== sequence) return;
        setItems((prev) => (append && prev ? [...prev, ...page.items] : page.items));
        setCursor(page.next_cursor);
        setListError(null);
        setListRequestId(undefined);
      } catch (err) {
        if (controller.signal.aborted || isAbortError(err) || listRequestRef.current?.sequence !== sequence) return;
        failList(err);
      } finally {
        if (listRequestRef.current?.sequence === sequence) listRequestRef.current = null;
      }
    },
    [abortListRequest, csrfToken, favoriteOnly, failList, query, typeFilter],
  );

  useEffect(() => {
    void loadPage(null, false);
  }, [loadPage]);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    request<HealthReport>("GET", "/api/v1/items/health", undefined, { csrfToken, signal: controller.signal })
      .then((report) => {
        if (active && !controller.signal.aborted) setHealth(report);
      })
      .catch(() => {
        if (active && !controller.signal.aborted) setHealth(null);
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [csrfToken]);

  const abortDetailRequest = useCallback(() => {
    detailRequestRef.current?.controller.abort();
    detailRequestRef.current = null;
  }, []);

  const abortFavoriteRequest = useCallback(() => {
    favoriteRequestRef.current?.controller.abort();
    favoriteRequestRef.current = null;
    favoriteBusyRef.current = false;
  }, []);

  const abortTrashRequest = useCallback(() => {
    trashRequestRef.current?.controller.abort();
    trashRequestRef.current = null;
    if (mountedRef.current) setTrashBusy(false);
  }, []);

  useEffect(() => {
    return () => {
      abortListRequest();
      abortDetailRequest();
      abortFavoriteRequest();
      abortTrashRequest();
      listSequenceRef.current += 1;
      detailSequenceRef.current += 1;
      favoriteSequenceRef.current += 1;
      trashSequenceRef.current += 1;
    };
  }, [abortDetailRequest, abortFavoriteRequest, abortListRequest, abortTrashRequest]);

  const loadDetail = useCallback(
    async (id: string) => {
      refreshSequenceRef.current += 1;
      abortFavoriteRequest();
      abortTrashRequest();
      abortDetailRequest();
      const controller = new AbortController();
      const sequence = ++detailSequenceRef.current;
      detailRequestRef.current = { controller, sequence };
      setModal({ kind: "loading", itemId: id });
      try {
        const detail = await request<ItemDetailData>("GET", `/api/v1/items/${id}`, undefined, {
          csrfToken,
          signal: controller.signal,
        });
        if (controller.signal.aborted || detailRequestRef.current?.sequence !== sequence || routeItemId !== id) {
          return;
        }
        setModal({ kind: "detail", detail });
      } catch (err) {
        if (controller.signal.aborted || isAbortError(err) || detailRequestRef.current?.sequence !== sequence) {
          return;
        }
        const info = errorText(err);
        setModal({ kind: "error", itemId: id, message: info.message, requestId: info.requestId });
      } finally {
        if (detailRequestRef.current?.sequence === sequence) {
          detailRequestRef.current = null;
        }
      }
    },
    [abortDetailRequest, abortFavoriteRequest, abortTrashRequest, csrfToken, routeItemId],
  );

  useEffect(() => {
    if (!routeItemId) {
      initialRouteRef.current = false;
      if (closingRef.current) {
        closingRef.current = false;
        refreshSequenceRef.current += 1;
        abortFavoriteRequest();
        abortTrashRequest();
        setModal({ kind: "closed" });
        modalSourceRef.current = null;
        return;
      }
      if (modal.kind !== "closed" && modal.kind !== "create") {
        refreshSequenceRef.current += 1;
        abortDetailRequest();
        abortFavoriteRequest();
        abortTrashRequest();
        setModal({ kind: "closed" });
        modalSourceRef.current = null;
        setDirtyState(false);
      }
      return;
    }
    if (closingRef.current) return;
    const sameItem =
      (modal.kind === "loading" && modal.itemId === routeItemId) ||
      (modal.kind === "error" && modal.itemId === routeItemId) ||
      ((modal.kind === "detail" || modal.kind === "edit" || modal.kind === "history") && modal.detail.id === routeItemId);
    if (sameItem) return;

    const source: ModalSource = !initialRouteRef.current && window.history.state?.source === "list" ? "list" : "direct";
    initialRouteRef.current = false;
    modalSourceRef.current = source;
    void loadDetail(routeItemId);
  }, [abortDetailRequest, abortFavoriteRequest, abortTrashRequest, loadDetail, modal, routeItemId, setDirtyState]);

  useEffect(() => {
    if (consumedNavigationState.current || routeItemId) return;
    consumedNavigationState.current = true;
    const state = consumeNavigationState<{ kind?: string; password?: string }>();
    if (state?.kind === "new-login" && state.password) {
      setModal({ kind: "create", initialLoginDraft: { password: state.password } });
      setDirtyState(false);
    }
  }, [routeItemId, setDirtyState]);

  useEffect(() => {
    if (modal.kind === "closed" && focusListAfterDeleteRef.current) {
      focusListAfterDeleteRef.current = false;
      listRegionRef.current?.focus();
    }
  }, [modal.kind]);

  const finishClose = useCallback(() => {
    refreshSequenceRef.current += 1;
    abortDetailRequest();
    abortFavoriteRequest();
    abortTrashRequest();
    setConfirmTrash(false);
    setTrashError(null);
    editorBusyRef.current = false;
    setDirtyState(false);
    setModal({ kind: "closed" });
    if (!routeItemId) {
      modalSourceRef.current = null;
      closingRef.current = false;
      return;
    }
    closingRef.current = true;
    if (modalSourceRef.current === "list") {
      window.history.back();
    } else {
      replace("/vault", { source: "direct" });
    }
  }, [abortDetailRequest, abortFavoriteRequest, abortTrashRequest, routeItemId, setDirtyState]);

  const requestClose = useCallback(() => {
    if (editorBusyRef.current) return;
    if ((modal.kind === "edit" || modal.kind === "create") && dirtyRef.current) {
      setConfirmDiscard(true);
      return;
    }
    finishClose();
  }, [finishClose, modal.kind]);

  useEffect(() => {
    const release = setNavigationGuard((intent: NavigationIntent) => {
      if (editorBusyRef.current) return false;
      if (!dirtyRef.current) return true;
      pendingLeaveRef.current = { kind: "push", path: intent.path, meta: intent.meta };
      setConfirmDiscard(true);
      return false;
    });
    return release;
  }, []);

  useEffect(() => {
    if (!dirty) return;
    const onPopState = (event: PopStateEvent) => {
      if (!dirtyRef.current) return;
      if (restoringPopRef.current) {
        restoringPopRef.current = false;
        return;
      }
      event.stopImmediatePropagation();
      const targetHasItem = matchPath("/vault/:itemId", window.location.pathname) !== null;
      // `popstate` fires after the browser has already traversed. Keep the
      // actual traversal delta so restoring the old URL and confirming the
      // pending leave use opposite directions consistently.
      const delta = !routeItemId && targetHasItem ? 1 : -1;
      pendingLeaveRef.current = { kind: "pop", delta };
      restoringPopRef.current = true;
      window.history.go(-delta);
      if (!editorBusyRef.current) setConfirmDiscard(true);
    };
    window.addEventListener("popstate", onPopState, true);
    return () => window.removeEventListener("popstate", onPopState, true);
  }, [dirty, routeItemId]);

  useEffect(() => {
    if (!dirty) return;
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [dirty]);

  const confirmDiscardChanges = useCallback(() => {
    const pending = pendingLeaveRef.current;
    pendingLeaveRef.current = null;
    setConfirmDiscard(false);
    setDirtyState(false);
    if (!pending) {
      finishClose();
      return;
    }
    if (pending.kind === "discard-edit") {
      editorBusyRef.current = false;
      setModal({ kind: "detail", detail: pending.detail });
      return;
    }
    if (pending.kind === "push") {
      navigate(pending.path, pending.meta);
      return;
    }
    restoringPopRef.current = true;
    window.history.go(pending.delta);
  }, [finishClose, setDirtyState]);

  const refreshAfterChange = useCallback(
    async (detail: ItemDetailData, sourceScope?: VaultScope) => {
      if (!mountedRef.current) return;
      const sequence = ++refreshSequenceRef.current;
      setModal({ kind: "detail", detail });
      setDirtyState(false);
      if (sourceScope && sourceScope !== detail.vault_scope) {
        setStatusMessage(detail.vault_scope === "shared" ? "已移至共享" : "已移至个人保险库");
      }
      if (path !== `/vault/${detail.id}`) {
        // Establish the route before yielding to the list refresh. Otherwise
        // the route synchronizer sees a newly saved detail on `/vault` and
        // treats it as a stale modal that should be closed.
        navigate(`/vault/${detail.id}`, { source: "list", modal: "item" });
      }
      await loadPage(null, false);
      if (!mountedRef.current || refreshSequenceRef.current !== sequence) return;
    },
    [loadPage, path, setDirtyState],
  );

  const openCreate = useCallback(() => {
    refreshSequenceRef.current += 1;
    abortDetailRequest();
    abortFavoriteRequest();
    editorBusyRef.current = false;
    setModal({ kind: "create" });
    modalSourceRef.current = null;
    setStatusMessage(null);
    setDirtyState(false);
  }, [abortDetailRequest, abortFavoriteRequest, setDirtyState]);

  const openEdit = useCallback(() => {
    if (modal.kind !== "detail") return;
    abortFavoriteRequest();
    editorBusyRef.current = false;
    setModal({ kind: "edit", detail: modal.detail });
    setStatusMessage(null);
    setDirtyState(false);
  }, [abortFavoriteRequest, modal, setDirtyState]);

  const openHistory = useCallback(() => {
    if (modal.kind !== "detail" && modal.kind !== "edit") return;
    abortFavoriteRequest();
    editorBusyRef.current = false;
    setModal({ kind: "history", detail: modal.detail });
    setDirtyState(false);
  }, [abortFavoriteRequest, modal, setDirtyState]);

  const toggleFavorite = useCallback(async () => {
    if (modal.kind !== "detail" || favoriteBusyRef.current) return;
    const itemId = modal.detail.id;
    const controller = new AbortController();
    const sequence = ++favoriteSequenceRef.current;
    favoriteRequestRef.current = { controller, sequence };
    favoriteBusyRef.current = true;
    try {
      await request("PUT", `/api/v1/items/${itemId}/favorite`, { favorite: !modal.detail.favorite }, { csrfToken, signal: controller.signal });
      if (controller.signal.aborted || favoriteRequestRef.current?.sequence !== sequence || routeItemId !== itemId) return;
      const detail = await request<ItemDetailData>("GET", `/api/v1/items/${itemId}`, undefined, { csrfToken, signal: controller.signal });
      if (controller.signal.aborted || favoriteRequestRef.current?.sequence !== sequence || routeItemId !== itemId) return;
      setModal({ kind: "detail", detail });
      await loadPage(null, false);
    } catch (err) {
      if (controller.signal.aborted || isAbortError(err) || favoriteRequestRef.current?.sequence !== sequence) return;
      const info = errorText(err);
      setModal({ kind: "error", itemId, message: info.message, requestId: info.requestId });
    } finally {
      if (favoriteRequestRef.current?.sequence === sequence) {
        favoriteRequestRef.current = null;
        favoriteBusyRef.current = false;
      }
    }
  }, [csrfToken, loadPage, modal, routeItemId]);

  const trashSelected = useCallback(async () => {
    if (modal.kind !== "detail" || trashBusy) return;
    const controller = new AbortController();
    const sequence = ++trashSequenceRef.current;
    trashRequestRef.current = { controller, sequence };
    setTrashBusy(true);
    setTrashError(null);
    try {
      await request("DELETE", `/api/v1/items/${modal.detail.id}`, undefined, { csrfToken, signal: controller.signal });
      if (!mountedRef.current || controller.signal.aborted || trashRequestRef.current?.sequence !== sequence) return;
      setConfirmTrash(false);
      focusListAfterDeleteRef.current = true;
      setModal({ kind: "closed" });
      setDirtyState(false);
      closingRef.current = true;
      replace("/vault", { source: "direct" });
      await loadPage(null, false);
    } catch (err) {
      if (!mountedRef.current || controller.signal.aborted || trashRequestRef.current?.sequence !== sequence) return;
      const info = errorText(err);
      setTrashError({ message: info.message, requestId: info.requestId });
    } finally {
      if (trashRequestRef.current?.sequence === sequence) {
        trashRequestRef.current = null;
        if (mountedRef.current) setTrashBusy(false);
      }
    }
  }, [csrfToken, loadPage, modal, setDirtyState, trashBusy]);

  const canManage = useCallback(
    (meta: ItemMeta): boolean => {
      if (!principal) return false;
      return meta.vault_scope === "personal" ? meta.owner_id === principal.user_id : meta.creator_id === principal.user_id;
    },
    [principal],
  );

  if (section === "trash") {
    return (
      <AppFrame>
        <TrashPage csrfToken={csrfToken} onChanged={() => void loadPage(null, false)} />
      </AppFrame>
    );
  }

  return (
    <AppFrame>
      <div ref={listRegionRef} tabIndex={-1} className="mx-auto w-full max-w-screen-xl outline-none focus-visible:outline-2 focus-visible:outline-ink">
        <div className="space-y-4 p-4">
          <form
            className="flex min-w-0 flex-wrap gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              void loadPage(null, false);
            }}
          >
            <input
              type="search"
              aria-label="搜索条目"
              placeholder="搜索标题、用户名、网址、标签、备注"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              className="min-h-[44px] min-w-0 flex-1 basis-48 border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm focus-visible:bg-neutral-100 focus-visible:outline-none"
            />
            <Button type="submit">搜索</Button>
          </form>

          <div className="flex flex-wrap gap-2">
            <select
              aria-label="按类型筛选"
              value={typeFilter}
              onChange={(event) => setTypeFilter(event.target.value)}
              className="min-h-[44px] border border-ink bg-paper px-2 font-mono text-xs"
            >
              <option value="">全部类型</option>
              {ITEM_TYPES.map((type) => <option key={type} value={type}>{TYPE_LABELS[type]}</option>)}
            </select>
            <Button variant={favoriteOnly ? "primary" : "secondary"} onClick={() => setFavoriteOnly((value) => !value)}>
              收藏
            </Button>
            <Link to="/vault/trash" className="flex min-h-[44px] items-center px-3 font-mono text-xs uppercase tracking-widest underline-offset-4 hover:underline">
              回收站
            </Link>
            <Button className="ml-auto" onClick={openCreate}>新建条目</Button>
          </div>

          {health && (health.weak > 0 || health.reused > 0 || health.expired > 0) && (
            <p className="border border-ink px-3 py-2 font-body text-xs" role="status">
              密码健康：弱密码 {health.weak} · 重复 {health.reused} · 已过期 {health.expired}
            </p>
          )}
          {statusMessage && <p className="border border-ink px-3 py-2 font-body text-sm" role="status">{statusMessage}</p>}
          <ErrorSummary message={listError ?? ""} requestId={listRequestId} onDismiss={() => setListError(null)} />
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
                  ref={(element) => {
                    if (element && modal.kind !== "detail") element.dataset.vaultItem = item.id;
                  }}
                  type="button"
                  onClick={() => {
                    modalSourceRef.current = "list";
                    navigate(`/vault/${item.id}`, { source: "list", modal: "item" });
                  }}
                  className={`grid w-full min-w-0 grid-cols-1 gap-1 px-4 py-3 text-left hover:bg-neutral-100 md:grid-cols-[minmax(0,1fr)_8rem_12rem_2rem] md:items-center md:gap-4 ${routeItemId === item.id ? "border-l-4 border-accent" : ""}`}
                >
                  <span className="min-w-0">
                    <span className="block truncate font-body font-semibold md:whitespace-normal">
                      {item.favorite && <span aria-label="已收藏">★ </span>}
                      {item.title || "（无标题）"}
                    </span>
                    {item.creator_name && item.vault_scope === "shared" && item.creator_name !== principal?.username && (
                      <span className="mt-1 block truncate font-body text-xs text-neutral-500">来自 {item.creator_name} 的共享条目</span>
                    )}
                  </span>
                  <span className="font-mono text-xs text-neutral-500">{TYPE_LABELS[item.item_type]}</span>
                  <span className="font-mono text-xs text-neutral-500">
                    {item.vault_scope === "shared" ? `共享 · 创建者 ${item.creator_name ?? "成员"}` : "个人"}
                  </span>
                  <span className="hidden justify-self-end font-mono text-xs md:block" aria-hidden="true">↗</span>
                </button>
              </li>
            ))}
          </ul>
        )}
        {cursor && <div className="p-4"><Button variant="secondary" onClick={() => void loadPage(cursor, true)}>加载更多</Button></div>}
      </div>

      {modal.kind !== "closed" && (
        <ItemDialog
          mode={modal}
          csrfToken={csrfToken}
          canManage={modal.kind === "detail" || modal.kind === "edit" || modal.kind === "history" ? canManage(modal.detail) : false}
          onClose={requestClose}
          onEdit={openEdit}
          onShowHistory={openHistory}
          onToggleFavorite={() => void toggleFavorite()}
          onTrash={() => { setTrashError(null); setConfirmTrash(true); }}
          onSaved={(detail) => {
            editorBusyRef.current = false;
            const sourceScope = modal.kind === "edit" ? modal.detail.vault_scope : undefined;
            void refreshAfterChange(detail, sourceScope);
          }}
          onCancelEdit={() => {
            editorBusyRef.current = false;
            if (dirtyRef.current) {
              if (modal.kind === "edit") {
                pendingLeaveRef.current = { kind: "discard-edit", detail: modal.detail };
              }
              setConfirmDiscard(true);
            }
            else if (modal.kind === "edit") setModal({ kind: "detail", detail: modal.detail });
            else finishClose();
          }}
          onDirtyChange={setDirtyState}
          onBusyChange={setEditorBusy}
          onMoveConfirm={requestMoveConfirmation}
          onRestored={(detail) => void refreshAfterChange(detail)}
          onHistoryClose={() => {
            if (modal.kind === "history") setModal({ kind: "detail", detail: modal.detail });
          }}
          onRetry={() => {
            if (modal.kind === "error" && modal.itemId) void loadDetail(modal.itemId);
          }}
        />
      )}

      <ConfirmDialog
        open={confirmDiscard}
        title="放弃未保存修改？"
        description="当前编辑内容尚未保存，关闭后这些内容会丢失。"
        confirmLabel="放弃修改"
        onCancel={() => {
          setConfirmDiscard(false);
          pendingLeaveRef.current = null;
        }}
        onConfirm={confirmDiscardChanges}
      />
      <ConfirmDialog
        open={moveConfirmation !== null}
        danger
        title="移动条目？"
        description={moveConfirmation?.to === "shared"
          ? "移动后将创建一个新的共享条目并删除当前条目，历史记录不会保留。仍要继续吗？"
          : "移动后将创建一个新的个人条目并删除当前条目，历史记录不会保留。仍要继续吗？"}
        confirmLabel="确认移动"
        onCancel={() => resolveMoveConfirmation(false)}
        onConfirm={() => resolveMoveConfirmation(true)}
      />
      <ConfirmDialog
        open={confirmTrash}
        danger
        title="移入回收站？"
        description={
          <>
            <p>条目将从列表与详情中隐藏；30 天内可在回收站恢复。</p>
            {trashError && <ErrorSummary message={trashError.message} requestId={trashError.requestId} />}
          </>
        }
        confirmLabel={trashBusy ? "移入中…" : "移入回收站"}
        confirmDisabled={trashBusy}
        onCancel={() => { if (!trashBusy) setConfirmTrash(false); }}
        onConfirm={() => void trashSelected()}
      />
    </AppFrame>
  );
}

function AppFrame({ children }: { children: React.ReactNode }) {
  return <div className="py-6">{children}</div>;
}
