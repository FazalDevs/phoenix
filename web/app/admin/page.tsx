"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { api, Metrics, Room, Standing } from "@/lib/api";

const POLL_MS = 1500;
const MAX_SAMPLES = 40;

export default function Dashboard() {
  const router = useRouter();
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const [rooms, setRooms] = useState<Room[]>([]);
  const [board, setBoard] = useState<Standing[]>([]);
  const [eventsPerSec, setEventsPerSec] = useState<number | null>(null);
  const [series, setSeries] = useState<number[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [demoBusy, setDemoBusy] = useState(false);

  // track previous bus counter to derive events/sec
  const prev = useRef<{ published: number; t: number } | null>(null);

  async function refresh() {
    try {
      const [m, r, lb] = await Promise.all([
        api.metrics(),
        api.rooms(),
        api.leaderboard().catch(() => [] as Standing[]),
      ]);
      setMetrics(m);
      setRooms(r);
      setBoard(lb as Standing[]);
      setErr(null);

      if (m.events_published != null) {
        const now = Date.now();
        if (prev.current) {
          const dt = (now - prev.current.t) / 1000;
          if (dt > 0) {
            const eps = Math.max(0, (m.events_published - prev.current.published) / dt);
            setEventsPerSec(eps);
            setSeries((s) => [...s, eps].slice(-MAX_SAMPLES));
          }
        }
        prev.current = { published: m.events_published, t: now };
      }
    } catch (e: any) {
      setErr(e.message || "failed to reach backend");
    }
  }

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, POLL_MS);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function terminate(id: string) {
    try {
      await api.terminate(id);
    } catch {
      /* ignore */
    }
    refresh();
  }

  async function launchDemo() {
    setDemoBusy(true);
    try {
      const res = await api.launchDemo(8);
      router.push(`/admin/rooms/${res.room}`);
    } catch (e: any) {
      setErr(e.message || "could not launch demo");
      setDemoBusy(false);
    }
  }

  function statusClass(s: string) {
    if (s === "open" || s === "waiting") return "open";
    if (s === "active" || s === "playing") return "playing";
    return "closed";
  }

  return (
    <div className="container grid" style={{ gap: 24 }}>
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 12,
        }}
      >
        <div>
          <h1 style={{ margin: "8px 0" }}>Mission Control</h1>
          <p className="muted" style={{ margin: 0 }}>
            Live operational view of the Phoenix backend · auto-refresh {POLL_MS / 1000}s
          </p>
        </div>
        <button className="btn-primary" onClick={launchDemo} disabled={demoBusy}>
          {demoBusy ? "Launching…" : "⚡ Launch Live Demo"}
        </button>
      </div>

      {err && (
        <div className="banner err">
          Backend unreachable: {err}. Is the Phoenix server running on{" "}
          {process.env.NEXT_PUBLIC_API_URL || "http://localhost:8090"}?
        </div>
      )}

      {/* Live metrics — sockets, Redis presence, rooms, and event-bus throughput */}
      <div className="grid cards">
        <div className="card">
          <div className="muted">Players online</div>
          <div className="stat green">{metrics?.online_players ?? "—"}</div>
          <div className="muted" style={{ fontSize: 11 }}>live WebSocket connections</div>
        </div>
        <div className="card">
          <div className="muted">Presence (Redis)</div>
          <div className="stat">{metrics?.presence_online ?? "—"}</div>
          <div className="muted" style={{ fontSize: 11 }}>presence projection</div>
        </div>
        <div className="card">
          <div className="muted">Active rooms</div>
          <div className="stat">{metrics?.active_rooms ?? "—"}</div>
          <div className="muted" style={{ fontSize: 11 }}>{rooms.length} total</div>
        </div>
        <div className="card">
          <div className="muted">Events / sec</div>
          <div className="stat">
            {eventsPerSec != null ? eventsPerSec.toFixed(1) : "—"}
            <span style={{ fontSize: 13 }}> /s</span>
          </div>
          <div className="muted" style={{ fontSize: 11 }}>
            {metrics?.events_published != null
              ? `${metrics.events_published.toLocaleString()} total events`
              : "event bus throughput"}
          </div>
        </div>
      </div>

      {/* Live events/sec line chart */}
      <div className="card">
        <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 12 }}>
          <strong>Event bus throughput</strong>
          <span className="muted" style={{ fontSize: 12 }}>
            events/sec · last {MAX_SAMPLES} samples
          </span>
        </div>
        <Sparkline data={series} />
      </div>

      <div
        className="grid"
        style={{ gridTemplateColumns: "1.4fr 1fr", gap: 24, alignItems: "start" }}
      >
        {/* Rooms */}
        <div className="card">
          <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 12 }}>
            <strong>Active sessions</strong>
            <span className="muted">live</span>
          </div>
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>Game</th>
                <th>Status</th>
                <th>Players</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rooms.map((r) => (
                <tr key={r.id}>
                  <td>
                    <Link href={`/admin/rooms/${r.id}`}>{r.id.slice(0, 8)}…</Link>
                  </td>
                  <td>
                    <span className={`badge ${r.game_type === "arena" ? "playing" : "open"}`}>
                      {r.game_type}
                    </span>
                  </td>
                  <td>
                    <span className={`badge ${statusClass(r.status)}`}>{r.status}</span>
                  </td>
                  <td>{metrics?.per_room?.[r.id] ?? 0}</td>
                  <td>
                    {r.status !== "closed" && (
                      <button className="danger" onClick={() => terminate(r.id)}>
                        terminate
                      </button>
                    )}
                  </td>
                </tr>
              ))}
              {rooms.length === 0 && !err && (
                <tr>
                  <td colSpan={5} className="muted">
                    No rooms yet. Hit <strong>⚡ Launch Live Demo</strong> or start a match from{" "}
                    <Link href="/play">/play</Link> · <Link href="/arena">/arena</Link>.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>

        {/* Leaderboard — CQRS read model folded from MatchEnded events */}
        <div className="card">
          <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 4 }}>
            <strong>Leaderboard</strong>
            <span className="muted">read model</span>
          </div>
          <p className="muted" style={{ margin: "0 0 12px", fontSize: 11 }}>
            Projection folded from MatchEnded events via the event bus.
          </p>
          <table>
            <thead>
              <tr>
                <th>#</th>
                <th>Player</th>
                <th>W</th>
                <th>L</th>
                <th>D</th>
              </tr>
            </thead>
            <tbody>
              {board.map((s, i) => (
                <tr key={s.player_id}>
                  <td className="muted">{i + 1}</td>
                  <td>{s.display_name}</td>
                  <td style={{ color: "var(--green)" }}>{s.wins}</td>
                  <td>{s.losses}</td>
                  <td className="muted">{s.draws}</td>
                </tr>
              ))}
              {board.length === 0 && (
                <tr>
                  <td colSpan={5} className="muted">
                    No finished matches yet. Play a game to checkmate.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

// Hand-rolled SVG line chart — no chart deps.
function Sparkline({ data }: { data: number[] }) {
  const W = 720;
  const H = 120;
  const PAD = 8;

  const { path, area, max } = useMemo(() => {
    if (data.length < 2) return { path: "", area: "", max: 0 };
    const mx = Math.max(1, ...data);
    const n = data.length;
    const x = (i: number) => PAD + (i / (n - 1)) * (W - PAD * 2);
    const y = (v: number) => H - PAD - (v / mx) * (H - PAD * 2);
    const pts = data.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`);
    const line = `M ${pts.join(" L ")}`;
    const fill = `${line} L ${x(n - 1).toFixed(1)},${H - PAD} L ${x(0).toFixed(1)},${H - PAD} Z`;
    return { path: line, area: fill, max: mx };
  }, [data]);

  if (data.length < 2) {
    return (
      <div style={{ height: H, display: "flex", alignItems: "center" }}>
        <span className="muted" style={{ fontSize: 13 }}>
          Collecting samples… (needs the backend live)
        </span>
      </div>
    );
  }

  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      style={{ width: "100%", height: H, display: "block" }}
    >
      <defs>
        <linearGradient id="epsFill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#ff6b3d" stopOpacity="0.35" />
          <stop offset="100%" stopColor="#ff6b3d" stopOpacity="0" />
        </linearGradient>
      </defs>
      {/* gridlines */}
      {[0.25, 0.5, 0.75].map((f) => (
        <line
          key={f}
          x1={PAD}
          x2={W - PAD}
          y1={PAD + f * (H - PAD * 2)}
          y2={PAD + f * (H - PAD * 2)}
          stroke="#232a3b"
          strokeWidth={1}
        />
      ))}
      <path d={area} fill="url(#epsFill)" />
      <path d={path} fill="none" stroke="#ff6b3d" strokeWidth={2} strokeLinejoin="round" />
      <text x={W - PAD} y={PAD + 12} textAnchor="end" fill="#8b93a7" fontSize={11} fontFamily="monospace">
        peak {max.toFixed(1)}/s
      </text>
    </svg>
  );
}
