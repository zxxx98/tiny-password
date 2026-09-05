# Self-hosted font licenses (decision D12)

All four typefaces are bundled at build time from @fontsource packages and
served from this application's own origin. No font CDN is contacted at
runtime.

| Typeface | Package | License | Copyright |
| --- | --- | --- | --- |
| Playfair Display | @fontsource/playfair-display | SIL Open Font License 1.1 | The Playfair Display Project Authors |
| Lora | @fontsource/lora | SIL Open Font License 1.1 | The Lora Project Authors |
| Inter | @fontsource/inter | SIL Open Font License 1.1 | The Inter Project Authors |
| JetBrains Mono | @fontsource/jetbrains-mono | SIL Open Font License 1.1 | JetBrains |

The SIL Open Font License 1.1 full text ships inside each package
(`node_modules/@fontsource/<family>/LICENSE`), see
https://openfontlicense.org. Font files are reproduced verbatim; no outline
data has been modified.
