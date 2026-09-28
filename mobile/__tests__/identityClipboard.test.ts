import {formatIdentityClipboard, hasIdentityClipboardContent} from '../src/vault/identityClipboard';
import type {IdentityPayload} from '../src/api/types';

const full: IdentityPayload = {
  name: '张伟',
  full_name: '张伟',
  company: 'Tiny Password',
  phone: '13800000000',
  email: 'zhang@example.com',
  country: '中国',
  state: '北京市',
  city: '北京市',
  district: '海淀区',
  address_line: '中关村大街 1 号',
  postal_code: '100000',
  notes: '前台收件',
};

test('formats the identity payload as name, address and phone lines', () => {
  expect(formatIdentityClipboard(full)).toBe(
    ['姓名：张伟', '地址：北京市 北京市 海淀区 中关村大街 1 号 100000', '联系电话：13800000000'].join('\n'),
  );
});

test('drops empty address parts and flattens newlines', () => {
  expect(
    formatIdentityClipboard({
      name: 'A',
      full_name: 'A\nB',
      state: '',
      city: '上海市',
      address_line: '  南京路\n2 号 ',
      phone: ' 555 ',
    }),
  ).toBe(['姓名：A B', '地址：上海市 南京路 2 号', '联系电话：555'].join('\n'));
});

test('reports whether anything is copyable', () => {
  expect(hasIdentityClipboardContent(full)).toBe(true);
  expect(hasIdentityClipboardContent({name: 'A', company: 'B'})).toBe(false);
  expect(hasIdentityClipboardContent({name: 'A', city: ' '})).toBe(false);
});
