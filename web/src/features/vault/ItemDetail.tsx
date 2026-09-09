import { Button } from "../../design-system/Button";
import { describeItem, TYPE_LABELS, type CreditCardPayload, type IdentityPayload, type ItemDetail as ItemDetailData, type LoginPayload, type SecureNotePayload, type SshKeyPayload } from "./types";
import { SensitiveField } from "./SensitiveField";

export type ItemDetailProps = {
  detail: ItemDetailData;
  csrfToken: string;
  canManage: boolean;
  showHeader?: boolean;
  showActions?: boolean;
  onEdit: () => void;
  onShowHistory: () => void;
  onToggleFavorite: () => void;
  onTrash: () => void;
};

function websiteHref(value: string): string | null {
  const trimmed = value.trim();
  if (!trimmed || /[\u0000-\u0020\u007f\\]/.test(trimmed)) return null;
  let candidate = trimmed;
  if (candidate.startsWith("//")) {
    candidate = `https:${candidate}`;
  } else if (!/^https?:\/\//i.test(candidate)) {
    // A host with a numeric port is not a URI scheme (e.g. localhost:8080).
    const hasScheme = /^[a-z][a-z\d+.-]*:/i.test(candidate);
    const hasPort = /^[^/:?#]+:\d+(?:[/?#]|$)/.test(candidate);
    if ((hasScheme && !hasPort) || /^[/?#]/.test(candidate)) return null;
    candidate = `https://${candidate}`;
  }
  try {
    const url = new URL(candidate);
    return url.protocol === "https:" || url.protocol === "http:" ? url.href : null;
  } catch {
    return null;
  }
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <span className="block font-mono text-xs uppercase tracking-widest">{label}</span>
      <div className="break-all font-body text-sm">{children}</div>
    </div>
  );
}

/**
 * ItemDetail renders the decrypted entry. Sensitive values go through
 * SensitiveField (masked + audited reveal/copy); everything else is plain.
 */
export function ItemDetail({
  detail,
  csrfToken,
  canManage,
  showHeader = true,
  showActions = true,
  onEdit,
  onShowHistory,
  onToggleFavorite,
  onTrash,
}: ItemDetailProps) {
  return (
    <article className="space-y-6" aria-label={`条目 ${detail.title}`}>
      {showHeader && (
        <header className="space-y-2">
          <p className="font-mono text-xs uppercase tracking-widest text-neutral-500">
            {TYPE_LABELS[detail.item_type]} · {describeItem(detail)}
          </p>
          <h3 className="font-display text-3xl font-bold">{detail.title || "（无标题）"}</h3>
          <p className="font-mono text-xs text-neutral-500">
            版本 {detail.revision} · 更新于 {detail.updated_at.slice(0, 19).replace("T", " ")}Z
          </p>
        </header>
      )}

      {detail.vault_scope === "shared" && !canManage && (
        <p className="border border-ink px-3 py-2 font-body text-xs" role="note">
          这是 {detail.creator_name ?? "其他成员"} 创建的共享条目：可以查看，但只有创建者能修改。
        </p>
      )}
      {(detail.tags?.length ?? 0) > 0 && (
        <ul className="flex flex-wrap gap-2" aria-label="标签">
          {detail.tags.map((tag) => (
            <li key={tag} className="border border-ink px-2 py-0.5 font-mono text-xs">{tag}</li>
          ))}
        </ul>
      )}

      {(() => {
        switch (detail.item_type) {
          case "login": {
            const p = detail.payload as LoginPayload;
            return (
              <div className="space-y-5">
                <Row label="用户名">{p.username || "—"}</Row>
                <SensitiveField label="密码" field="password" value={p.password ?? ""} itemId={detail.id} csrfToken={csrfToken} />
                {(p.urls?.length ?? 0) > 0 && (
                  <Row label="网址">
                    <ul className="space-y-1">
                      {p.urls!.map((url, i) => {
                        const href = websiteHref(url);
                        return (
                        <li key={i} className="break-all">
                          {href ? <a href={href} className="underline decoration-accent decoration-2 underline-offset-4" rel="noreferrer noopener">{url}</a> : <span>{url}</span>}
                        </li>
                        );
                      })}
                    </ul>
                  </Row>
                )}
                {p.notes && <Row label="备注">{p.notes}</Row>}
                {p.password_updated_at && <Row label="密码更新日期">{p.password_updated_at}</Row>}
                {p.password_expires_at && <Row label="密码过期日期">{p.password_expires_at}</Row>}
              </div>
            );
          }
          case "ssh_key": {
            const p = detail.payload as SshKeyPayload;
            return (
              <div className="space-y-5">
                <Row label="算法">{p.algorithm}</Row>
                <Row label="公钥">
                  <span className="block whitespace-pre-wrap font-mono text-xs">{p.public_key}</span>
                </Row>
                <SensitiveField label="私钥" field="private_key" value={p.private_key ?? ""} itemId={detail.id} csrfToken={csrfToken} />
                {(p.key_passphrase ?? "") !== "" && (
                  <SensitiveField label="私钥口令" field="key_passphrase" value={p.key_passphrase ?? ""} itemId={detail.id} csrfToken={csrfToken} />
                )}
                {p.comment && <Row label="注释">{p.comment}</Row>}
                {p.fingerprint && <Row label="指纹">{p.fingerprint}</Row>}
                {p.notes && <Row label="备注">{p.notes}</Row>}
              </div>
            );
          }
          case "credit_card": {
            const p = detail.payload as CreditCardPayload;
            return (
              <div className="space-y-5">
                <Row label="持卡人">{p.cardholder}</Row>
                <SensitiveField label="卡号" field="number" value={p.number} itemId={detail.id} csrfToken={csrfToken} />
                <Row label="有效期">{p.exp_month}/{p.exp_year}</Row>
                {(p.cvv ?? "") !== "" && (
                  <SensitiveField label="CVV" field="cvv" value={p.cvv ?? ""} itemId={detail.id} csrfToken={csrfToken} />
                )}
                {(p.pin ?? "") !== "" && (
                  <SensitiveField label="PIN" field="pin" value={p.pin ?? ""} itemId={detail.id} csrfToken={csrfToken} />
                )}
                {p.notes && <Row label="备注">{p.notes}</Row>}
              </div>
            );
          }
          case "identity": {
            const p = detail.payload as IdentityPayload;
            const rows: Array<[string, string | undefined]> = [
              ["姓名", p.full_name], ["公司", p.company], ["电话", p.phone], ["邮箱", p.email],
              ["国家/地区", p.country], ["省/州", p.state], ["城市", p.city], ["区县", p.district],
              ["地址行", p.address_line], ["邮政编码", p.postal_code], ["备注", p.notes],
            ];
            return (
              <div className="space-y-5">
                {rows.filter(([, v]) => !!v).map(([label, value]) => (
                  <Row key={label} label={label}>{value}</Row>
                ))}
              </div>
            );
          }
          case "secure_note": {
            const p = detail.payload as SecureNotePayload;
            return (
              <Row label="正文">
                <span className="whitespace-pre-wrap">{p.body}</span>
              </Row>
            );
          }
        }
      })()}

      {showActions && canManage && (
        <footer className="flex flex-wrap gap-2 border-t border-divider pt-4">
          <Button onClick={onEdit}>编辑</Button>
          <Button variant="secondary" onClick={onToggleFavorite}>
            {detail.favorite ? "取消收藏" : "收藏"}
          </Button>
          <Button variant="ghost" onClick={onShowHistory}>历史</Button>
          <Button variant="danger" onClick={onTrash}>移入回收站</Button>
        </footer>
      )}
      {showActions && !canManage && (
        <footer className="flex flex-wrap gap-2 border-t border-divider pt-4">
          <Button variant="ghost" onClick={onShowHistory}>历史</Button>
        </footer>
      )}
    </article>
  );
}
