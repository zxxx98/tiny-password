import type {IdentityPayload} from '../api/types';

function clean(value: string | null | undefined): string {
  return (value ?? '').replace(/[\r\n]+/g, ' ').trim();
}

export function formatIdentityClipboard(payload: IdentityPayload): string {
  const address = [payload.state, payload.city, payload.district, payload.address_line, payload.postal_code]
    .map(clean)
    .filter(Boolean)
    .join(' ');
  return [`姓名：${clean(payload.full_name)}`, `地址：${address}`, `联系电话：${clean(payload.phone)}`].join(
    '\n',
  );
}

export function hasIdentityClipboardContent(payload: IdentityPayload): boolean {
  return [
    payload.full_name,
    payload.state,
    payload.city,
    payload.district,
    payload.address_line,
    payload.postal_code,
    payload.phone,
  ].some(value => clean(value).length > 0);
}
