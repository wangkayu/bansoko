import { createRoot } from 'react-dom/client'
import { Suspense, lazy } from 'react'
import { getLocale, loadLocale } from './lib/i18n'
import { ErrorBoundary } from './ui/ErrorBoundary'
import { installStaleChunkReload } from './lib/staleChunkReload'
// Self-hosted web fonts (OFL-1.1; texts in public/fonts-licenses.txt).
// Nothing is fetched from a font CDN: the CSP allows font-src 'self' only.
// Soft direction: Plus Jakarta Sans for display and body,
// Noto Sans SC for CJK, IBM Plex Mono for code and file paths.
import '@fontsource-variable/plus-jakarta-sans/wght.css'
import '@fontsource-variable/noto-sans-sc/wght.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import './index.css'

installStaleChunkReload();
const App = lazy(async () => {
  await loadLocale(getLocale());
  return import('./App.tsx');
});
createRoot(document.getElementById("root")!).render(
  <ErrorBoundary reloadOnRetry><Suspense fallback={null}><App /></Suspense></ErrorBoundary>,
);
