// vendored from docs/Quiver Design System (assets/quiver-mark.svg) @ 2026-06-10
// — do not hand-edit; re-vendor instead. The arrow mark doubles as the shell
// prompt chevron `›`; tint via currentColor on the parent.
export function QuiverMark({ size = 20 }: { size?: number }) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 32 32"
      width={size}
      height={size}
      fill="none"
      role="img"
      aria-label="Quiver"
    >
      <g stroke="currentColor" strokeWidth="3.4" strokeLinecap="round" strokeLinejoin="round">
        <path d="M5 16 H18.5" />
        <path d="M14.5 9 L22.5 16 L14.5 23" />
        <path d="M5 12.5 L7.5 16 L5 19.5" opacity="0.55" />
      </g>
    </svg>
  );
}
