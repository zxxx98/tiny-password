import { Field } from "../../../design-system/Field";
import { LIMITS, overLimit, type IdentityPayload } from "../types";
import type { FieldsProps } from "./LoginFields";

export function validateIdentity(p: IdentityPayload): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!p.name.trim()) errors.name = "名称必填。";
  else {
    const over = overLimit(p.name, LIMITS.name);
    if (over) errors.name = over;
  }
  const checks: Array<[keyof IdentityPayload, number]> = [
    ["full_name", LIMITS.username],
    ["company", LIMITS.username],
    ["phone", LIMITS.phone],
    ["email", LIMITS.email],
    ["country", LIMITS.region],
    ["state", LIMITS.region],
    ["city", LIMITS.region],
    ["district", LIMITS.region],
    ["address_line", LIMITS.addressLine],
    ["postal_code", LIMITS.postalCode],
  ];
  for (const [key, max] of checks) {
    const err = overLimit(p[key], max);
    if (err) errors[key] = err;
  }
  const notes = overLimit(p.notes, LIMITS.notes);
  if (notes) errors.notes = notes;
  return errors;
}

export function IdentityFields({ payload, errors, onChange, disabled }: FieldsProps<IdentityPayload>) {
  return (
    <>
      <Field id="f-name" label="名称" required value={payload.name} error={errors.name} disabled={disabled}
        onChange={(e) => onChange({ name: e.target.value })} />
      <Field id="f-full-name" label="姓名" value={payload.full_name ?? ""} error={errors.full_name} disabled={disabled}
        onChange={(e) => onChange({ full_name: e.target.value })} />
      <Field id="f-company" label="公司" value={payload.company ?? ""} error={errors.company} disabled={disabled}
        onChange={(e) => onChange({ company: e.target.value })} />
      <Field id="f-phone" label="电话" value={payload.phone ?? ""} error={errors.phone} disabled={disabled}
        onChange={(e) => onChange({ phone: e.target.value })} />
      <Field id="f-email" label="邮箱" value={payload.email ?? ""} error={errors.email} disabled={disabled}
        onChange={(e) => onChange({ email: e.target.value })} />
      <Field id="f-country" label="国家/地区" value={payload.country ?? ""} error={errors.country} disabled={disabled}
        onChange={(e) => onChange({ country: e.target.value })} />
      <Field id="f-state" label="省/州" value={payload.state ?? ""} error={errors.state} disabled={disabled}
        onChange={(e) => onChange({ state: e.target.value })} />
      <Field id="f-city" label="城市" value={payload.city ?? ""} error={errors.city} disabled={disabled}
        onChange={(e) => onChange({ city: e.target.value })} />
      <Field id="f-district" label="区县" value={payload.district ?? ""} error={errors.district} disabled={disabled}
        onChange={(e) => onChange({ district: e.target.value })} />
      <Field id="f-address" label="地址行" value={payload.address_line ?? ""} error={errors.address_line} disabled={disabled}
        onChange={(e) => onChange({ address_line: e.target.value })} />
      <Field id="f-postal" label="邮政编码" value={payload.postal_code ?? ""} error={errors.postal_code} disabled={disabled}
        onChange={(e) => onChange({ postal_code: e.target.value })} />
      <Field id="f-notes" label="备注" value={payload.notes ?? ""} error={errors.notes} disabled={disabled}
        onChange={(e) => onChange({ notes: e.target.value })} />
    </>
  );
}
