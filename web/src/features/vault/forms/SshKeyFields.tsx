import { Field } from "../../../design-system/Field";
import { LIMITS, overLimit, type SshKeyPayload } from "../types";
import type { FieldsProps } from "./LoginFields";

export function validateSshKey(p: SshKeyPayload): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!p.name.trim()) errors.name = "名称必填。";
  else {
    const over = overLimit(p.name, LIMITS.name);
    if (over) errors.name = over;
  }
  if (p.algorithm !== "ed25519" && p.algorithm !== "rsa4096") errors.algorithm = "算法仅支持 ed25519 或 rsa4096。";
  if (!p.public_key?.trim()) errors.public_key = "公钥必填。";
  else {
    const over = overLimit(p.public_key, LIMITS.publicKey);
    if (over) errors.public_key = over;
  }
  if (!p.private_key?.trim()) errors.private_key = "私钥必填。";
  else {
    const over = overLimit(p.private_key, LIMITS.privateKey);
    if (over) errors.private_key = over;
  }
  const passphrase = overLimit(p.key_passphrase, LIMITS.passphrase);
  if (passphrase) errors.key_passphrase = passphrase;
  const comment = overLimit(p.comment, LIMITS.comment);
  if (comment) errors.comment = comment;
  const fingerprint = overLimit(p.fingerprint, LIMITS.fingerprint);
  if (fingerprint) errors.fingerprint = fingerprint;
  const notes = overLimit(p.notes, LIMITS.notes);
  if (notes) errors.notes = notes;
  return errors;
}

export function SshKeyFields({ payload, errors, onChange, disabled }: FieldsProps<SshKeyPayload>) {
  return (
    <>
      <Field id="f-name" label="名称" required value={payload.name} error={errors.name} disabled={disabled}
        onChange={(e) => onChange({ name: e.target.value })} />
      <div className="space-y-1">
        <label htmlFor="f-algorithm" className="block font-mono text-xs uppercase tracking-widest">算法</label>
        <select id="f-algorithm"
          className="min-h-[44px] w-full border-b-2 border-ink bg-transparent px-3 py-2 font-mono text-sm"
          value={payload.algorithm} disabled={disabled}
          onChange={(e) => onChange({ algorithm: e.target.value as SshKeyPayload["algorithm"] })}>
          <option value="ed25519">Ed25519</option>
          <option value="rsa4096">RSA-4096</option>
        </select>
        {errors.algorithm && <p className="font-body text-xs text-accent">{errors.algorithm}</p>}
      </div>
      <Field id="f-public-key" label="公钥" required value={payload.public_key ?? ""} error={errors.public_key} disabled={disabled}
        onChange={(e) => onChange({ public_key: e.target.value })} />
      <Field id="f-private-key" label="私钥" type="password" autoComplete="off" required value={payload.private_key ?? ""} error={errors.private_key} disabled={disabled}
        onChange={(e) => onChange({ private_key: e.target.value })} hint="保存后以遮蔽形式显示。" />
      <Field id="f-passphrase" label="私钥口令" type="password" autoComplete="off" value={payload.key_passphrase ?? ""} error={errors.key_passphrase} disabled={disabled}
        onChange={(e) => onChange({ key_passphrase: e.target.value })} />
      <Field id="f-comment" label="注释" value={payload.comment ?? ""} error={errors.comment} disabled={disabled}
        onChange={(e) => onChange({ comment: e.target.value })} />
      <Field id="f-fingerprint" label="指纹" value={payload.fingerprint ?? ""} error={errors.fingerprint} disabled={disabled}
        onChange={(e) => onChange({ fingerprint: e.target.value })} />
      <Field id="f-notes" label="备注" value={payload.notes ?? ""} error={errors.notes} disabled={disabled}
        onChange={(e) => onChange({ notes: e.target.value })} />
    </>
  );
}
