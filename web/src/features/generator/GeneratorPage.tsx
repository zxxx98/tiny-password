import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, request } from "../../app/api";
import { navigate, navigateWithState } from "../../app/router";
import { SESSION_EXPIRED_EVENT, useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { ErrorSummary } from "../../design-system/Status";

type SshGenerated = {
  algorithm: string;
  public_key: string;
  private_key: string;
  fingerprint: string;
  key_passphrase: string;
  comment: string;
};

function MaskedOutput({
  value,
  label,
  secretName,
  revealed,
  onToggle,
}: {
  value: string;
  label: string;
  secretName: string;
  revealed: boolean;
  onToggle: () => void;
}) {
  return (
    <div className="flex flex-wrap items-start gap-2">
      <output className="block min-w-0 flex-1 break-all border border-ink px-3 py-2 font-mono text-sm" aria-label={label}>
        {revealed ? value : "••••••••"}
      </output>
      <Button variant="secondary" aria-pressed={revealed} onClick={onToggle}>
        {revealed ? `隐藏${secretName}` : `显示${secretName}`}
      </Button>
    </div>
  );
}

function errorOf(err: unknown): { message: string; requestId?: string } {
  if (err instanceof ApiError) {
    return { message: err.message, requestId: err.requestId };
  }
  return { message: "网络错误，请重试。" };
}

/**
 * GeneratorPage (design §6.5): passwords, passphrases and SSH keys from the
 * OS CSPRNG. Results live only in this component's memory; saving an SSH key
 * posts it to the vault as an ssh_key entry — cancelling leaves nothing.
 */
export function GeneratorPage() {
  const { csrfToken } = useSession();
  const mountedRef = useRef(true);
  const sessionExpiredRef = useRef(false);
  const controllersRef = useRef(new Set<AbortController>());

  const [pwLength, setPwLength] = useState(20);
  const [pwClasses, setPwClasses] = useState({ lower: true, upper: true, digits: true, symbols: true, noAmbiguous: false });
  const [password, setPassword] = useState<string | null>(null);
  const [passwordRevealed, setPasswordRevealed] = useState(false);

  const [words, setWords] = useState(5);
  const [separator, setSeparator] = useState("-");
  const [capitalize, setCapitalize] = useState(false);
  const [passphrase, setPassphrase] = useState<string | null>(null);
  const [passphraseRevealed, setPassphraseRevealed] = useState(false);

  const [sshAlgorithm, setSshAlgorithm] = useState<"ed25519" | "rsa4096">("ed25519");
  const [sshPassphrase, setSshPassphrase] = useState("");
  const [sshComment, setSshComment] = useState("");
  const [sshKey, setSshKey] = useState<SshGenerated | null>(null);
  const [privateKeyRevealed, setPrivateKeyRevealed] = useState(false);
  const [saving, setSaving] = useState(false);

  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [busy, setBusy] = useState<string | null>(null);

  const clearGenerated = useCallback(() => {
    setPassword(null);
    setPasswordRevealed(false);
    setPassphrase(null);
    setPassphraseRevealed(false);
    setSshKey(null);
    setPrivateKeyRevealed(false);
    setSshPassphrase("");
    setSshComment("");
    setBusy(null);
    setSaving(false);
  }, []);

  const clearSsh = useCallback(() => {
    setSshKey(null);
    setPrivateKeyRevealed(false);
    setSshPassphrase("");
    setSshComment("");
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    const onExpired = () => {
      sessionExpiredRef.current = true;
      for (const controller of controllersRef.current) {
        controller.abort();
      }
      clearGenerated();
    };
    window.addEventListener(SESSION_EXPIRED_EVENT, onExpired);
    return () => {
      mountedRef.current = false;
      for (const controller of controllersRef.current) {
        controller.abort();
      }
      clearGenerated();
      window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired);
    };
  }, [clearGenerated]);

  const generate = useCallback(
    async (kind: "password" | "passphrase" | "ssh-key", body: unknown, apply: (data: any) => void) => {
      setBusy(kind);
      setError(null);
      setRequestId(undefined);
      const controller = new AbortController();
      controllersRef.current.add(controller);
      try {
        const data = await request<any>(`POST`, `/api/v1/generators/${kind}`, body, { csrfToken, signal: controller.signal });
        if (mountedRef.current && !sessionExpiredRef.current && !controller.signal.aborted) {
          apply(data);
        }
      } catch (err) {
        if (!mountedRef.current || sessionExpiredRef.current || controller.signal.aborted) {
          return;
        }
        const info = errorOf(err);
        setError(info.message);
        setRequestId(info.requestId);
      } finally {
        controllersRef.current.delete(controller);
        if (mountedRef.current && !sessionExpiredRef.current && !controller.signal.aborted) {
          setBusy(null);
        }
      }
    },
    [csrfToken],
  );

  const saveSshEntry = useCallback(async () => {
    if (!sshKey) {
      return;
    }
    const generated = sshKey;
    setSaving(true);
    setError(null);
    const controller = new AbortController();
    controllersRef.current.add(controller);
    try {
      const detail = await request<{ id: string }>(
        "POST",
        "/api/v1/items",
        {
          item_type: "ssh_key",
          vault_scope: "personal",
          payload: {
            name: sshKey.fingerprint,
            algorithm: sshKey.algorithm,
            public_key: sshKey.public_key,
            private_key: sshKey.private_key,
            key_passphrase: generated.key_passphrase || undefined,
            comment: generated.comment || undefined,
            fingerprint: sshKey.fingerprint,
          },
        },
        { csrfToken, idempotencyKey: crypto.randomUUID(), signal: controller.signal },
      );
      if (!mountedRef.current || sessionExpiredRef.current || controller.signal.aborted) {
        return;
      }
      // The generated material moved into the vault; clear the memory copy.
      clearSsh();
      navigate(`/vault/${detail.id}`);
    } catch (err) {
      if (!mountedRef.current || sessionExpiredRef.current || controller.signal.aborted) {
        return;
      }
      const info = errorOf(err);
      setError(info.message);
      setRequestId(info.requestId);
    } finally {
      controllersRef.current.delete(controller);
      if (mountedRef.current && !sessionExpiredRef.current && !controller.signal.aborted) {
        setSaving(false);
      }
    }
  }, [sshKey, csrfToken, clearSsh]);

  const useGeneratedLoginPassword = useCallback(
    (value: string, clear: () => void) => {
      clear();
      navigateWithState("/vault", { kind: "new-login", password: value });
    },
    [],
  );

  return (
    <div className="mx-auto max-w-3xl space-y-8 px-4 py-10">
      <header>
        <h2 className="font-display text-3xl font-bold">生成器</h2>
        <p className="mt-1 font-body text-sm text-neutral-600">
          使用操作系统加密随机源生成；结果只保留在本页内存中，刷新即消失。
        </p>
      </header>

      <ErrorSummary message={error ?? ""} requestId={requestId} onDismiss={() => setError(null)} />

      <section aria-labelledby="gen-password" className="space-y-4 border border-ink p-6">
        <h3 id="gen-password" className="font-display text-xl font-bold">
          密码
        </h3>
        <div className="flex flex-wrap items-center gap-4">
          <label className="flex min-h-[44px] items-center gap-2 font-body text-sm">
            长度
            <input
              type="number"
              min={8}
              max={128}
              value={pwLength}
              onChange={(e) => setPwLength(Number(e.target.value))}
              className="min-h-[44px] w-24 border-b-2 border-ink bg-transparent px-2 font-mono text-sm"
            />
          </label>
          {([
            ["lower", "小写", "lowercase"],
            ["upper", "大写", "uppercase"],
            ["digits", "数字", "digits"],
            ["symbols", "符号", "symbols"],
            ["noAmbiguous", "排除易混淆", "exclude"],
          ] as const).map(([key, label]) => (
            <label key={key} className="flex min-h-[44px] items-center gap-2 font-body text-sm">
              <input
                type="checkbox"
                checked={pwClasses[key]}
                onChange={(e) => setPwClasses({ ...pwClasses, [key]: e.target.checked })}
              />
              {label}
            </label>
          ))}
        </div>
        <Button
          disabled={busy === "password"}
          onClick={() =>
            void generate(
              "password",
              {
                length: pwLength,
                lowercase: pwClasses.lower,
                uppercase: pwClasses.upper,
                digits: pwClasses.digits,
                symbols: pwClasses.symbols,
                exclude_ambiguous: pwClasses.noAmbiguous,
              },
              (data) => {
                setPassword(data.value);
                setPasswordRevealed(false);
              },
            )
          }
        >
          {busy === "password" ? "生成中…" : "生成密码"}
        </Button>
        {password && (
          <>
            <MaskedOutput
              value={password}
              label="生成的密码"
              secretName="密码"
              revealed={passwordRevealed}
              onToggle={() => setPasswordRevealed((revealed) => !revealed)}
            />
            <div className="flex flex-wrap gap-2">
              <Button
                variant="secondary"
                onClick={() => useGeneratedLoginPassword(password, () => {
                  setPassword(null);
                  setPasswordRevealed(false);
                })}
              >
                用于新建登录密码
              </Button>
              <Button variant="ghost" onClick={() => { setPassword(null); setPasswordRevealed(false); }}>
                清除密码
              </Button>
            </div>
          </>
        )}
      </section>

      <section aria-labelledby="gen-passphrase" className="space-y-4 border border-ink p-6">
        <h3 id="gen-passphrase" className="font-display text-xl font-bold">
          多词口令
        </h3>
        <p className="font-body text-xs text-neutral-500">
          词表：EFF 短词表（1296 词，CC BY 3.0），随镜像内置。
        </p>
        <div className="flex flex-wrap items-center gap-4">
          <label className="flex min-h-[44px] items-center gap-2 font-body text-sm">
            单词数
            <input
              type="number"
              min={3}
              max={10}
              value={words}
              onChange={(e) => setWords(Number(e.target.value))}
              className="min-h-[44px] w-24 border-b-2 border-ink bg-transparent px-2 font-mono text-sm"
            />
          </label>
          <label className="flex min-h-[44px] items-center gap-2 font-body text-sm">
            分隔符
            <input
              value={separator}
              onChange={(e) => setSeparator(e.target.value)}
              className="min-h-[44px] w-24 border-b-2 border-ink bg-transparent px-2 font-mono text-sm"
            />
          </label>
          <label className="flex min-h-[44px] items-center gap-2 font-body text-sm">
            <input type="checkbox" checked={capitalize} onChange={(e) => setCapitalize(e.target.checked)} />
            首字母大写
          </label>
        </div>
        <Button
          disabled={busy === "passphrase"}
          onClick={() =>
            void generate("passphrase", { words, separator, capitalize }, (data) => {
              setPassphrase(data.value);
              setPassphraseRevealed(false);
            })
          }
        >
          {busy === "passphrase" ? "生成中…" : "生成口令"}
        </Button>
        {passphrase && (
          <>
            <MaskedOutput
              value={passphrase}
              label="生成的口令"
              secretName="口令"
              revealed={passphraseRevealed}
              onToggle={() => setPassphraseRevealed((revealed) => !revealed)}
            />
            <div className="flex flex-wrap gap-2">
              <Button
                variant="secondary"
                onClick={() => useGeneratedLoginPassword(passphrase, () => {
                  setPassphrase(null);
                  setPassphraseRevealed(false);
                })}
              >
                用于新建登录口令
              </Button>
              <Button variant="ghost" onClick={() => { setPassphrase(null); setPassphraseRevealed(false); }}>
                清除口令
              </Button>
            </div>
          </>
        )}
      </section>

      <section aria-labelledby="gen-ssh" className="space-y-4 border border-ink p-6">
        <h3 id="gen-ssh" className="font-display text-xl font-bold">
          SSH 密钥
        </h3>
        <div className="space-y-4">
          <div className="space-y-1">
            <label htmlFor="ssh-algorithm" className="block font-mono text-xs uppercase tracking-widest">
              算法
            </label>
            <select
              id="ssh-algorithm"
              value={sshAlgorithm}
              onChange={(e) => setSshAlgorithm(e.target.value as "ed25519" | "rsa4096")}
              className="min-h-[44px] w-full border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm"
            >
              <option value="ed25519">Ed25519</option>
              <option value="rsa4096">RSA-4096</option>
            </select>
            <p className="font-body text-xs text-neutral-500">Ed25519 默认；RSA-4096 生成较慢。</p>
          </div>
          <Field
            id="ssh-passphrase"
            label="私钥口令（可选）"
            type="password"
            autoComplete="new-password"
            value={sshPassphrase}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSshPassphrase(e.target.value)}
          />
          <Field
            id="ssh-comment"
            label="注释（可选）"
            value={sshComment}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSshComment(e.target.value)}
          />
        </div>
        <Button
          disabled={busy === "ssh-key"}
          onClick={() => {
            const generatedPassphrase = sshPassphrase;
            const generatedComment = sshComment;
            void generate(
              "ssh-key",
              { algorithm: sshAlgorithm, passphrase: generatedPassphrase, comment: generatedComment },
              (data) => {
                setSshKey({
                  ...data,
                  key_passphrase: generatedPassphrase,
                  comment: generatedComment,
                });
                setPrivateKeyRevealed(false);
              },
            );
          }}
        >
          {busy === "ssh-key" ? "生成中…" : "生成密钥"}
        </Button>
        {sshKey && (
          <div className="space-y-3 border border-ink p-4">
            <p className="font-mono text-xs">{sshKey.fingerprint}</p>
            <output className="block break-all bg-neutral-100 px-3 py-2 font-mono text-xs" aria-label="生成的公钥">
              {sshKey.public_key}
            </output>
            <MaskedOutput
              value={sshKey.private_key}
              label="生成的私钥"
              secretName="私钥"
              revealed={privateKeyRevealed}
              onToggle={() => setPrivateKeyRevealed((revealed) => !revealed)}
            />
            <p className="font-body text-xs text-neutral-500">
              私钥默认遮蔽；需要时使用“显示私钥”，保存或取消后会清除内存副本。
            </p>
            <div className="flex flex-wrap gap-2">
              <Button disabled={saving} onClick={() => void saveSshEntry()}>
                {saving ? "保存中…" : "保存为 SSH 条目"}
              </Button>
              <Button variant="secondary" disabled={saving} onClick={clearSsh}>
                取消并清除密钥
              </Button>
            </div>
          </div>
        )}
      </section>
    </div>
  );
}
