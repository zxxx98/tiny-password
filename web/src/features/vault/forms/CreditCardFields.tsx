import { Field } from "../../../design-system/Field";
import { LIMITS, overLimit, type CreditCardPayload } from "../types";
import type { FieldsProps } from "./LoginFields";

export function validateCreditCard(p: CreditCardPayload): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!p.name.trim()) errors.name = "名称必填。";
  else {
    const over = overLimit(p.name, LIMITS.name);
    if (over) errors.name = over;
  }
  if (!p.cardholder.trim()) errors.cardholder = "持卡人必填。";
  else {
    const over = overLimit(p.cardholder, LIMITS.username);
    if (over) errors.cardholder = over;
  }
  if (!p.number.trim()) errors.number = "卡号必填。";
  else {
    const over = overLimit(p.number, LIMITS.cardNumber);
    if (over) errors.number = over;
  }
  if (!Number.isInteger(p.exp_month) || p.exp_month < 1 || p.exp_month > 12) errors.exp_month = "有效月份为 1–12。";
  if (!Number.isInteger(p.exp_year) || p.exp_year < 2000 || p.exp_year > 9999) errors.exp_year = "有效年份为 2000–9999。";
  const cvv = overLimit(p.cvv, LIMITS.cvv);
  if (cvv) errors.cvv = cvv;
  const pin = overLimit(p.pin, LIMITS.pin);
  if (pin) errors.pin = pin;
  const notes = overLimit(p.notes, LIMITS.notes);
  if (notes) errors.notes = notes;
  return errors;
}

export function CreditCardFields({ payload, errors, onChange, disabled }: FieldsProps<CreditCardPayload>) {
  return (
    <>
      <Field id="f-name" label="名称" required value={payload.name} error={errors.name} disabled={disabled}
        onChange={(e) => onChange({ name: e.target.value })} />
      <Field id="f-cardholder" label="持卡人" required value={payload.cardholder} error={errors.cardholder} disabled={disabled}
        onChange={(e) => onChange({ cardholder: e.target.value })} />
      <Field id="f-number" label="卡号" type="password" autoComplete="off" required value={payload.number} error={errors.number} disabled={disabled}
        onChange={(e) => onChange({ number: e.target.value })} hint="保存后以遮蔽形式显示。" />
      <div className="grid grid-cols-2 gap-4">
        <Field id="f-exp-month" label="有效月份" type="number" min={1} max={12} required value={String(payload.exp_month)} error={errors.exp_month} disabled={disabled}
          onChange={(e) => onChange({ exp_month: Number(e.target.value) })} />
        <Field id="f-exp-year" label="有效年份" type="number" min={2000} max={9999} required value={String(payload.exp_year)} error={errors.exp_year} disabled={disabled}
          onChange={(e) => onChange({ exp_year: Number(e.target.value) })} />
      </div>
      <Field id="f-cvv" label="CVV" type="password" autoComplete="off" value={payload.cvv ?? ""} error={errors.cvv} disabled={disabled}
        onChange={(e) => onChange({ cvv: e.target.value })} />
      <Field id="f-pin" label="PIN" type="password" autoComplete="off" value={payload.pin ?? ""} error={errors.pin} disabled={disabled}
        onChange={(e) => onChange({ pin: e.target.value })} />
      <Field id="f-billing" label="账单地址条目 ID" value={payload.billing_address_item_id ?? ""} disabled={disabled}
        onChange={(e) => onChange({ billing_address_item_id: e.target.value || null })}
        hint="可选：指向本人可读的身份地址条目；引用规则由服务端校验。" />
      <Field id="f-notes" label="备注" value={payload.notes ?? ""} error={errors.notes} disabled={disabled}
        onChange={(e) => onChange({ notes: e.target.value })} />
    </>
  );
}
