// Thin client for the Phoenix API. All admin endpoints are open in Phase 1.
export const BASE = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8090";

export type Room = {
  id: string;
  owner_id: string;
  game_type: string;
  status: string;
  max_players: number;
  is_private: boolean;
  created_at: string;
};

export type PhoenixEvent = {
  id?: string;
  seq: number;
  type: string;
  room_id?: string;
  player_id: string;
  payload: any;
  timestamp: string;
  version?: number;
};

export type Metrics = {
  active_rooms: number;
  online_players: number; // live WebSocket connections
  per_room: Record<string, number>;
  presence_online?: number; // distinct players online via Redis presence projection
  events_published?: number; // cumulative events fanned out on the event bus
};

export type Standing = {
  player_id: string;
  display_name: string;
  wins: number;
  losses: number;
  draws: number;
};

export type AuthUser = {
  id: string;
  display_name: string;
  is_guest: boolean;
};

export type AuthTokens = {
  access_token: string;
  refresh_token: string;
  expires_in: number;
};

export type LoginResponse = {
  user: AuthUser;
  tokens: AuthTokens;
};

export type MatchmakeResponse = {
  room: Room;
  ws: string; // "/ws?room=<id>"
};

export type ChessState = {
  fen: string;
  turn: "w" | "b";
  status: "waiting" | "active" | "check" | "checkmate" | "stalemate" | "draw" | string;
  players: { w: string; b: string };
  winner: "w" | "b" | "";
  lastMove?: { from: string; to: string } | null;
  history: string[];
};

export type ArenaPlayer = {
  x: number;
  y: number;
  score: number;
  name: string;
  color: string;
};

export type ArenaFood = { id: number; x: number; y: number };

export type ArenaState = {
  w: number;
  h: number;
  players: Record<string, ArenaPlayer>;
  food: ArenaFood[];
};

// Heuristic: does an arbitrary reduced state look like an arena game state?
export function isArenaState(s: any): s is ArenaState {
  return (
    !!s &&
    typeof s === "object" &&
    typeof s.w === "number" &&
    typeof s.h === "number" &&
    !!s.players &&
    typeof s.players === "object" &&
    Array.isArray(s.food)
  );
}

export type LaunchDemoResponse = { room: string; ws: string };

async function get<T>(path: string): Promise<T> {
  const res = await fetch(`${BASE}${path}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`${res.status} ${path}`);
  return res.json();
}

async function post<T>(path: string, body?: unknown, token?: string): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const res = await fetch(`${BASE}${path}`, {
    method: "POST",
    cache: "no-store",
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error(`${res.status} ${path}`);
  return res.json();
}

export const api = {
  // auth + matchmaking
  loginGuest: (displayName: string) =>
    post<LoginResponse>("/login", { mode: "guest", display_name: displayName }),
  matchmake: (token?: string, game = "chess") =>
    post<MatchmakeResponse>(`/matchmake?game=${encodeURIComponent(game)}`, undefined, token),

  // admin
  launchDemo: (bots = 8) => post<LaunchDemoResponse>(`/admin/demo?bots=${bots}`),
  metrics: () => get<Metrics>("/admin/metrics"),
  rooms: () => get<Room[]>("/admin/rooms"),
  leaderboard: () => get<Standing[]>("/leaderboard"),
  events: (roomId: string, from = 1) =>
    get<PhoenixEvent[]>(`/admin/rooms/${roomId}/events?from=${from}`),
  roomState: (roomId: string) =>
    get<{ active: boolean; state?: any }>(`/admin/rooms/${roomId}/state`),
  terminate: (roomId: string) =>
    fetch(`${BASE}/admin/rooms/${roomId}/terminate`, { method: "POST", cache: "no-store" }),
};

// --- local session helpers ---------------------------------------------------
export type Session = { token: string; userId: string; displayName: string };

const SESSION_KEY = "phoenix.session";

// Sessions live in sessionStorage (per-tab), NOT localStorage. This is
// deliberate: each browser tab/window is its own player, so you can open two
// tabs and play yourself — white in one, black in the other. localStorage is
// shared across tabs, which would make both tabs the same guest and leave the
// second seat unfilled (the game would never start).
export function loadSession(): Session | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = sessionStorage.getItem(SESSION_KEY);
    return raw ? (JSON.parse(raw) as Session) : null;
  } catch {
    return null;
  }
}

export function saveSession(s: Session) {
  if (typeof window === "undefined") return;
  sessionStorage.setItem(SESSION_KEY, JSON.stringify(s));
}

export function clearSession() {
  if (typeof window === "undefined") return;
  sessionStorage.removeItem(SESSION_KEY);
}
