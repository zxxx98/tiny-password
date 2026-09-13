// Font families map 1:1 onto TTF files bundled in
// android/app/src/main/assets/fonts/. Select the exact file per role instead
// of relying on synthetic bold so rendering is deterministic on Android.
export const fonts = {
  display: 'PlayfairDisplay-Bold',
  displayHeavy: 'PlayfairDisplay-ExtraBold',
  body: 'Lora-Regular',
  bodySemi: 'Lora-SemiBold',
  ui: 'Inter-Regular',
  uiSemi: 'Inter-SemiBold',
  uiBold: 'Inter-Bold',
  mono: 'JetBrainsMono-Regular',
  monoMedium: 'JetBrainsMono-Medium',
} as const;

export const typeScale = {
  hero: {fontSize: 40, lineHeight: 44, fontFamily: fonts.displayHeavy},
  h1: {fontSize: 30, lineHeight: 34, fontFamily: fonts.display},
  h2: {fontSize: 22, lineHeight: 27, fontFamily: fonts.display},
  h3: {fontSize: 18, lineHeight: 23, fontFamily: fonts.display},
  label: {fontSize: 12, lineHeight: 16, fontFamily: fonts.uiSemi},
  meta: {fontSize: 11, lineHeight: 15, fontFamily: fonts.mono},
  body: {fontSize: 15, lineHeight: 24, fontFamily: fonts.body},
  bodySemi: {fontSize: 15, lineHeight: 24, fontFamily: fonts.bodySemi},
  ui: {fontSize: 14, lineHeight: 20, fontFamily: fonts.ui},
  uiSemi: {fontSize: 14, lineHeight: 20, fontFamily: fonts.uiSemi},
  button: {fontSize: 14, lineHeight: 18, fontFamily: fonts.uiSemi},
  mono: {fontSize: 14, lineHeight: 20, fontFamily: fonts.mono},
  monoSmall: {fontSize: 12, lineHeight: 17, fontFamily: fonts.mono},
} as const;

export const letterSpacing = {
  label: 1.4,
  button: 2.0,
  wide: 3.0,
} as const;
