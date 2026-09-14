import * as fs from 'node:fs';
import * as path from 'node:path';

const androidMain = path.resolve(__dirname, '..', 'android', 'app', 'src', 'main');

test('uses 77Password as the Android app label', () => {
  const strings = fs.readFileSync(path.join(androidMain, 'res', 'values', 'strings.xml'), 'utf8');

  expect(strings).toContain('<string name="app_name">77Password</string>');
});

test.each([
  ['mdpi', 48],
  ['hdpi', 72],
  ['xhdpi', 96],
  ['xxhdpi', 144],
  ['xxxhdpi', 192],
] as const)('has valid %s launcher PNG resources at %dx%d', (density, size) => {
  for (const name of ['ic_launcher.png', 'ic_launcher_round.png']) {
    const file = path.join(androidMain, 'res', `mipmap-${density}`, name);
    const data = fs.readFileSync(file);

    expect(data.length).toBeGreaterThan(24);
    expect(data.subarray(0, 8)).toEqual(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]));
    expect(data.readUInt32BE(16)).toBe(size);
    expect(data.readUInt32BE(20)).toBe(size);
  }
});
