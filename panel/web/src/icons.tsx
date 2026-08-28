// A single stroke icon family (1.6px, 24-grid) so the whole panel shares one
// visual language. No emoji anywhere in the interface.
type P = { size?: number; className?: string };

const svg = (d: React.ReactNode) => (p: P) => (
  <svg
    width={p.size ?? 16} height={p.size ?? 16} viewBox="0 0 24 24" fill="none"
    stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"
    className={p.className} aria-hidden="true" focusable="false"
  >{d}</svg>
);

export const IconGauge = svg(<><path d="M12 14a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z" /><path d="M13.4 10.6 19 5" /><path d="M20.7 17A9 9 0 1 0 3.3 17" /></>);
export const IconServer = svg(<><rect x="3" y="4" width="18" height="7" rx="2" /><rect x="3" y="13" width="18" height="7" rx="2" /><path d="M7 7.5h.01M7 16.5h.01" /></>);
export const IconArrowIn = svg(<><path d="M3 12h12" /><path d="m11 8 4 4-4 4" /><path d="M19 4v16" /></>);
export const IconArrowOut = svg(<><path d="M9 12h12" /><path d="m17 8 4 4-4 4" /><path d="M5 4v16" /></>);
export const IconGrid = svg(<><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></>);
export const IconList = svg(<><path d="M8 6h13M8 12h13M8 18h13" /><path d="M3.5 6h.01M3.5 12h.01M3.5 18h.01" /></>);
export const IconLayers = svg(<><path d="m12 3 9 5-9 5-9-5 9-5Z" /><path d="m3 13 9 5 9-5" /></>);
export const IconPhone = svg(<><rect x="6" y="2.5" width="12" height="19" rx="2.5" /><path d="M11 18.5h2" /></>);
export const IconPulse = svg(<><path d="M3 12h4l2.5-7 4 14L16 12h5" /></>);
export const IconShield = svg(<><path d="M12 3 4.5 6v6c0 4.5 3.2 7.9 7.5 9 4.3-1.1 7.5-4.5 7.5-9V6L12 3Z" /><path d="m9.2 12 2 2 3.6-3.8" /></>);
export const IconSliders = svg(<><path d="M4 8h10M18 8h2M4 16h4M12 16h8" /><circle cx="16" cy="8" r="2" /><circle cx="10" cy="16" r="2" /></>);
export const IconPlus = svg(<><path d="M12 5v14M5 12h14" /></>);
export const IconTrash = svg(<><path d="M4 7h16" /><path d="M9 7V5.5A1.5 1.5 0 0 1 10.5 4h3A1.5 1.5 0 0 1 15 5.5V7" /><path d="M6.5 7 7 19a1.5 1.5 0 0 0 1.5 1.4h7A1.5 1.5 0 0 0 17 19l.5-12" /></>);
export const IconRefresh = svg(<><path d="M20 11a8 8 0 0 0-13.7-5.2L4 8" /><path d="M4 4v4h4" /><path d="M4 13a8 8 0 0 0 13.7 5.2L20 16" /><path d="M20 20v-4h-4" /></>);
export const IconDownload = svg(<><path d="M12 4v11" /><path d="m8 11 4 4 4-4" /><path d="M4 19h16" /></>);
export const IconCopy = svg(<><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M5 15V6a2 2 0 0 1 2-2h9" /></>);
export const IconClose = svg(<><path d="M6 6 18 18M18 6 6 18" /></>);
export const IconCheck = svg(<><path d="m5 12.5 4.5 4.5L19 7" /></>);
export const IconWarn = svg(<><path d="M12 4 2.8 20h18.4L12 4Z" /><path d="M12 10v4M12 17.2h.01" /></>);
export const IconPlay = svg(<><path d="M7 4.5 19 12 7 19.5v-15Z" /></>);
export const IconRotate = svg(<><path d="M4 13a8 8 0 1 0 2.3-5.7L4 9.5" /><path d="M4 4v6h6" /></>);
export const IconSun = svg(<><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></>);
export const IconMoon = svg(<><path d="M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5Z" /></>);
export const IconLogout = svg(<><path d="M14 4h4a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-4" /><path d="M10 8 6 12l4 4" /><path d="M6 12h10" /></>);
export const IconKey = svg(<><circle cx="8" cy="12" r="4" /><path d="M12 12h9" /><path d="M17 12v3M20 12v2" /></>);
export const IconGlobe = svg(<><circle cx="12" cy="12" r="9" /><path d="M3 12h18" /><path d="M12 3c2.5 2.6 3.8 5.7 3.8 9S14.5 18.4 12 21c-2.5-2.6-3.8-5.7-3.8-9S9.5 5.6 12 3Z" /></>);
