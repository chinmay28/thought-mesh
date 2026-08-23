/**
 * The running client's version — the calendar `vYEAR.MONTH.PATCH`, where the
 * patch number is the repository's commit count (so `v2026.8.311` is the
 * 311th commit on the 2026.8 line).
 *
 * Inlined at build time by Vite's `define` (see vite.config.ts) from
 * scripts/version.mjs — the same source the Go binary is stamped from, so the
 * header and `/api/health` always agree. Patch `0` means a build made without
 * git available.
 */
declare const __APP_VERSION__: string;

export const APP_VERSION: string = __APP_VERSION__;
