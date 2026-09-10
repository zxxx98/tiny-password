import { Button } from "../../design-system/Button";
import { Dialog } from "../../design-system/Dialog";
import { ErrorSummary, Loading } from "../../design-system/Status";
import { HistoryPage } from "./HistoryPage";
import { ItemDetail } from "./ItemDetail";
import { ItemEditor } from "./ItemEditor";
import { describeItem, TYPE_LABELS, type ItemDetail as ItemDetailData, type SecretPayload } from "./types";

export type ItemDialogMode =
  | { kind: "loading"; itemId: string }
  | { kind: "detail"; detail: ItemDetailData }
  | { kind: "create"; initialLoginDraft?: { password: string } }
  | { kind: "edit"; detail: ItemDetailData }
  | { kind: "history"; detail: ItemDetailData }
  | { kind: "error"; itemId?: string; message: string; requestId?: string };

export type ItemDialogProps = {
  mode: ItemDialogMode;
  csrfToken: string;
  canManage?: boolean;
  onClose: () => void;
  onEdit?: () => void;
  onShowHistory?: () => void;
  onToggleFavorite?: () => void;
  onTrash?: () => void;
  onSaved?: (detail: ItemDetailData) => void;
  onCancelEdit?: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  onBusyChange?: (busy: boolean) => void;
  onMoveConfirm?: (from: "personal" | "shared", to: "personal" | "shared") => Promise<boolean>;
  onRestored?: (detail: ItemDetailData) => void;
  onHistoryClose?: () => void;
  onRetry?: () => void;
};

function updatedAt(detail: ItemDetailData) {
  return detail.updated_at.slice(0, 19).replace("T", " ") + "Z";
}

function metadata(detail: ItemDetailData) {
  return (
    <div className="space-y-1 font-mono text-xs text-neutral-500">
      <p>{describeItem(detail)}　/　版本 {detail.revision}</p>
      <p>更新于 {updatedAt(detail)}</p>
    </div>
  );
}

function titleFor(mode: ItemDialogMode) {
  switch (mode.kind) {
    case "detail":
    case "edit":
    case "history":
      return mode.detail.title || "（无标题）";
    case "create":
      return "新建条目";
    case "loading":
      return "正在加载条目";
    case "error":
      return "无法打开条目";
  }
}

function descriptionFor(mode: ItemDialogMode) {
  switch (mode.kind) {
    case "detail":
    case "edit":
    case "history":
      return (
        <>
          <p className="font-mono text-xs uppercase tracking-widest text-neutral-500">
            <span className="text-accent">■</span> 条目详情　/　{mode.detail.item_type === "secret"
              ? `${TYPE_LABELS.secret} · ${(mode.detail.payload as SecretPayload).entries.length} 个键值`
              : TYPE_LABELS[mode.detail.item_type]}
          </p>
          {metadata(mode.detail)}
        </>
      );
    case "loading":
      return "正在获取并解密条目内容。";
    case "error":
      return mode.itemId ? `条目 ${mode.itemId} 的读取失败。` : "读取条目失败。";
    case "create":
      return "创建一个新的保险库条目。";
  }
}

function detailFooter({
  detail,
  canManage,
  onEdit,
  onShowHistory,
  onToggleFavorite,
  onTrash,
}: {
  detail: ItemDetailData;
  canManage: boolean;
  onEdit?: () => void;
  onShowHistory?: () => void;
  onToggleFavorite?: () => void;
  onTrash?: () => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      {canManage && (
        <>
          <Button onClick={onEdit}>编辑条目</Button>
          <Button variant="secondary" onClick={onToggleFavorite}>
            {detail.favorite ? "☆ 取消收藏" : "☆ 收藏"}
          </Button>
        </>
      )}
      <Button variant="ghost" onClick={onShowHistory}>历史记录</Button>
      {canManage && (
        <Button variant="danger" className="ml-auto" onClick={onTrash}>
          移入回收站
        </Button>
      )}
    </div>
  );
}

/** The single modal shell for every vault item state. */
export function ItemDialog({
  mode,
  csrfToken,
  canManage = false,
  onClose,
  onEdit,
  onShowHistory,
  onToggleFavorite,
  onTrash,
  onSaved,
  onCancelEdit,
  onDirtyChange,
  onBusyChange,
  onMoveConfirm,
  onRestored,
  onHistoryClose,
  onRetry,
}: ItemDialogProps) {
  const detail = mode.kind === "detail" || mode.kind === "edit" || mode.kind === "history" ? mode.detail : null;

  return (
    <Dialog
      open
      title={titleFor(mode)}
      description={descriptionFor(mode)}
      onClose={onClose}
      footer={
        mode.kind === "detail" && detail
          ? detailFooter({ detail, canManage, onEdit, onShowHistory, onToggleFavorite, onTrash })
          : mode.kind === "error"
          ? (
              <div className="flex justify-end gap-2">
                {onRetry && <Button onClick={onRetry}>重试</Button>}
                <Button variant="secondary" onClick={onClose}>关闭</Button>
              </div>
            )
          : undefined
      }
    >
      <div className="min-w-0">
        {mode.kind === "loading" && <Loading label="正在解密条目…" />}
        {mode.kind === "error" && (
          <ErrorSummary message={mode.message} requestId={mode.requestId} />
        )}
        {mode.kind === "detail" && (
          <ItemDetail
            detail={mode.detail}
            csrfToken={csrfToken}
            canManage={canManage}
            showHeader={false}
            showActions={false}
            onEdit={onEdit ?? (() => {})}
            onShowHistory={onShowHistory ?? (() => {})}
            onToggleFavorite={onToggleFavorite ?? (() => {})}
            onTrash={onTrash ?? (() => {})}
          />
        )}
        {mode.kind === "create" && (
          <ItemEditor
            csrfToken={csrfToken}
            initialLoginDraft={mode.initialLoginDraft}
            onSaved={onSaved ?? (() => {})}
            onCancel={onCancelEdit ?? onClose}
            onDirtyChange={onDirtyChange}
            onBusyChange={onBusyChange}
            onMoveConfirm={onMoveConfirm}
          />
        )}
        {mode.kind === "edit" && (
          <ItemEditor
            csrfToken={csrfToken}
            initial={mode.detail}
            onSaved={onSaved ?? (() => {})}
            onCancel={onCancelEdit ?? onClose}
            onDirtyChange={onDirtyChange}
            onBusyChange={onBusyChange}
            onMoveConfirm={onMoveConfirm}
          />
        )}
        {mode.kind === "history" && (
          <HistoryPage
            itemId={mode.detail.id}
            csrfToken={csrfToken}
            canManage={canManage}
            onRestored={onRestored ?? (() => {})}
            onClose={onHistoryClose ?? onClose}
            onBusyChange={onBusyChange}
          />
        )}
      </div>
    </Dialog>
  );
}
