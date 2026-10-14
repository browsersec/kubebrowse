// Base URL for backend REST API calls.
// In production (e.g. Cloudflare Pages) set VITE_API_BASE_URL to the backend origin,
// e.g. https://api.kubebrowse.example.com or https://kubebrowse-tunnel.pages.dev
//
// Encore serves local APIs at http://localhost:4000 by default. Set
// VITE_API_BASE_URL=http://localhost:4000 when the frontend runs separately.
export const API_BASE = (import.meta.env.VITE_API_BASE_URL || "").replace(/\/$/, "");

// Derive WebSocket / HTTP tunnel bases from the same origin so they stay in sync.
const apiUrl = API_BASE ? new URL(API_BASE) : null;

export const WS_BASE = apiUrl
  ? `${apiUrl.protocol === "https:" ? "wss" : "ws"}://${apiUrl.host}`
  : `${window.location.protocol === "https:" ? "wss" : "ws"}://${location.host}`;

export const HTTP_TUNNEL_BASE = apiUrl
  ? `${apiUrl.protocol}//${apiUrl.host}`
  : `${window.location.protocol}//${location.host}`;
