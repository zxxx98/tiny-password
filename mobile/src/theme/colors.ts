// Newsprint design tokens (style.md). Permanent light mode.
export const colors = {
  background: '#F9F9F7',
  foreground: '#111111',
  muted: '#E5E5E0',
  accent: '#CC0000',
  focusBg: '#F0F0F0',
  neutral100: '#F5F5F5',
  neutral400: '#A3A3A3',
  neutral500: '#737373',
  neutral600: '#525252',
  paper: '#F9F9F7',
  invertedText: '#F9F9F7',
} as const;

export type NewsprintColor = keyof typeof colors;
