/// <reference types="vite/client" />
/// <reference types="@testing-library/jest-dom" />

interface ImportMetaEnv {
  readonly VITE_BANSOKO_E2E_FIXTURES?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
