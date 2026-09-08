/** Decorative engraving-style artwork, using the page's newsprint palette. */
export function VaultIllustration() {
  return (
    <svg viewBox="0 0 520 440" fill="none" className="mx-auto w-full max-w-lg text-ink" aria-hidden="true" focusable="false">
      {/* Fine construction lines give the illustration a printed-plate feel. */}
      <g stroke="currentColor" opacity="0.12">
        <circle cx="266" cy="212" r="180" />
        <circle cx="266" cy="212" r="157" strokeDasharray="2 7" />
        <path d="M36 212h460M266 16v392M80 388h380" />
        <path d="M72 64h20m-10-10v20m354 264h20m-10-10v20" />
      </g>
      {/* A shallow isometric cabinet with a hatched spine. */}
      <g stroke="currentColor" strokeWidth="2" strokeLinejoin="miter">
        <path d="m126 106 42-27h238v269l-42 28H126Z" className="fill-paper" />
        <path d="M126 106h238v270m0-270 42-27m-42 297 42-28" />
        <path d="m374 111 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14m-22 28 22-14" opacity="0.22" />
        <path d="M146 127h198v227H146Z" />
        <path d="M156 137h178v207H156Z" strokeWidth="1" opacity="0.3" />
        <path d="M139 164h14v31h-14zm0 121h14v31h-14z" className="fill-paper" />
        <circle cx="252" cy="238" r="56" />
        <circle cx="252" cy="238" r="46" strokeWidth="1" />
        <circle cx="252" cy="238" r="15" className="fill-paper" />
        <path d="M252 198v25m35 35-22-12m-48 12 22-12" strokeWidth="5" />
        <path d="M252 176v8m62 54h-8m-54 62v-8m-62-54h8" />
        <path d="M174 151h44m-44 8h28" strokeWidth="1" />
        <path d="M174 326h46m64 0h32" strokeWidth="1" />
        <path d="M144 377v11h32v-11m144 0v11h26v-11" />
      </g>
      {/* A single red key is the visual accent. */}
      <g className="text-accent" stroke="currentColor" strokeWidth="3">
        <circle cx="108" cy="315" r="24" className="fill-paper" />
        <circle cx="108" cy="315" r="8" />
        <path d="m125 332 49 49m-22-22 11-11m0 22 11-11" strokeWidth="5" />
      </g>
      <g fill="currentColor" opacity="0.35">
        <circle cx="146" cy="116" r="2" /><circle cx="344" cy="116" r="2" />
        <circle cx="344" cy="365" r="2" /><circle cx="146" cy="365" r="2" />
      </g>
    </svg>
  );
}
