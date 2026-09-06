import { useCallback, useState } from "react";
import { ApiError, request } from "../../app/api";
import { navigate } from "../../app/router";
import { useSession } from "../../app/session";
import { Button } from "../../design-system/Button";
import { Field } from "../../design-system/Field";
import { ErrorSummary } from "../../design-system/Status";

type SshGenerated = {
  algorithm: string;
  public_key: string;
  private_key: string;
  fingerprint: string;
};

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

  const [pwLength, setPwLength] = useState(20);
  const [pwClasses, setPwClasses] = useState({ lower: true, upper: true, digits: true, symbols: true, noAmbiguous: false });
  const [password, setPassword] = useState<string | null>(null);

  const [words, setWords] = useState(5);
  const [separator, setSeparator] = useState("-");
  const [capitalize, setCapitalize] = useState(false);
  const [passphrase, setPassphrase] = useState<string | null>(null);

  const [sshAlgorithm, setSshAlgorithm] = useState<"ed25519" | "rsa4096">("ed25519");
  const [sshPassphrase, setSshPassphrase] = useState("");
  const [sshComment, setSshComment] = useState("");
  const [sshKey, setSshKey] = useState<SshGenerated | null>(null);
  const [saving, setSaving] = useState(false);

  const [error, setError] = useState<string | null>(null);
  const [requestId, setRequestId] = useState<string | undefined>();
  const [busy, setBusy] = useState<string | null>(null);

  const generate = useCallback(
    async (kind: "password" | "passphrase" | "ssh-key", body: unknown, apply: (data: any) => void) => {
      setBusy(kind);
      setError(null);
      setRequestId(undefined);
      try {
        const data = await request<any>(`POST`, `/api/v1/generators/${kind}`, body, { csrfToken });
        apply(data);
      } catch (err) {
        const info = errorOf(err);
        setError(info.message);
        setRequestId(info.requestId);
      } finally {
        setBusy(null);
      }
    },
    [csrfToken],
  );

  const saveSshEntry = useCallback(async () => {
    if (!sshKey) {
      return;
    }
    setSaving(true);
    setError(null);
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
            key_passphrase: sshPassphrase || undefined,
            comment: sshComment || undefined,
            fingerprint: sshKey.fingerprint,
          },
        },
        { csrfToken, idempotencyKey: crypto.randomUUID() },
      );
      // The generated material moved into the vault; clear the memory copy.
      setSshKey(null);
      setSshPassphrase("");
      navigate(`/vault/${detail.id}`);
    } catch (err) {
      const info = errorOf(err);
      setError(info.message);
      setRequestId(info.requestId);
    } finally {
      setSaving(false);
    }
  }, [sshKey, sshPassphrase, sshComment, csrfToken]);

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
              (data) => setPassword(data.value),
            )
          }
        >
          {busy === "password" ? "生成中…" : "生成密码"}
        </Button>
        {password && (
          <output className="block break-all border border-ink px-3 py-2 font-mono text-sm" aria-label="生成的密码">
            {password}
          </output>
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
            void generate("passphrase", { words, separator, capitalize }, (data) => setPassphrase(data.value))
          }
        >
          {busy === "passphrase" ? "生成中…" : "生成口令"}
        </Button>
        {passphrase && (
          <output className="block break-all border border-ink px-3 py-2 font-mono text-sm" aria-label="生成的口令">
            {passphrase}
          </output>
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
          onClick={() => void generate("ssh-key", { algorithm: sshAlgorithm, passphrase: sshPassphrase, comment: sshComment }, (data) => setSshKey(data))}
        >
          {busy === "ssh-key" ? "生成中…" : "生成密钥"}
        </Button>
        {sshKey && (
          <div className="space-y-3 border border-ink p-4">
            <p className="font-mono text-xs">{sshKey.fingerprint}</p>
            <output className="block break-all bg-neutral-100 px-3 py-2 font-mono text-xs" aria-label="生成的公钥">
              {sshKey.public_key}
            </output>
            <output className="block max-h-40 overflow-y-auto break-all bg-neutral-100 px-3 py-2 font-mono text-xs" aria-label="生成的私钥">
              {sshKey.private_key}
            </output>
            <p className="font-body text-xs text-neutral-500">
              私钥仅在保存前展示；保存后以遮蔽形式查看。
            </p>
            <Button disabled={saving} onClick={() => void saveSshEntry()}>
              {saving ? "保存中…" : "保存为 SSH 条目"}
            </Button>
          </div>
        )}
      </section>
    </div>
  );
}
