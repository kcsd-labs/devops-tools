/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_ISSUER?: string;
  readonly VITE_CLIENT_ID?: string;
}
interface ImportMeta {
  readonly env: ImportMetaEnv;
}
