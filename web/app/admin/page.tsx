"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { api, Metrics, Room, Standing } from "@/lib/api";

export default function Dashboard() {
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const [rooms, setRooms] = useState<Room[]>([]);
  const [board, setBoard] = useState<Standing[]>([]);
  const [eventsPerSec, setEventsPerSec] = useState<number | null>(null);
  const [err, setErr] = useState<string | null>(null);

  // track previous bus counter to derive events/sec
  const prev = useRef<{ published: number; t: number } | null>(null);

  async function refresh() {
    try {
      const [m, r, lb] = await Promise.all([api.metrics(), api.rooms(), api.leaderboard().catch(() => [])]);
      setMetrics(m);
      setRooms(r);
      setBoard(lb as Standing[]);
      setErr(null);

      if (m.events_published != null) {
        const now = Date.now();
        if (prev.current) {
          const dt = (now - prev.current.t) / 1000;
          if (dt > 0) setEventsPerSec(Math.max(0, (m.events_published - prev.current.published) / dt));
        }
        prev.current = { published: m.events_published, t: now };
      }
    } catch (e: any) {
      setErr(e.message || "failed to reach backend");
    }
  }

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 2000);
    return () => clearInterval(t);
  }, []);

  async function terminate(id: string) {
    try {
      await api.terminate(id);
    } catch {
      /* ignore */
    }
    refresh();
  }

  function statusClass(s: string) {
    if (s === "open" || s === "waiting") return "open";
    if (s === "active" || s === "playing") return "playing";
    return "closed";
  }

  return (
    <div className="container grid" style={{ gap: 24 }}>
      <div>
        <h1 style={{ margin: "8px 0" }}>Admin Dashboard</h1>
        <p className="muted" style={{ margin: 0 }}>
          Live operational view of the Phoenix backend · auto-refresh 2s
        </p>
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
          <div className="muted">Event bus</div>
          <div className="stat">{eventsPerSec != null ? eventsPerSec.toFixed(1) : "—"}<span style={{ fontSize: 13 }}> /s</span></div>
          <div className="muted" style={{ fontSize: 11 }}>
            {metrics?.events_published != null ? `${metrics.events_published.toLocaleString()} total events` : "events/sec"}
          </div>
        </div>
      </div>

      <div className="grid" style={{ gridTemplateColumns: "1.4fr 1fr", gap: 24, alignItems: "start" }}>
        {/* Rooms */}
        <div className="card">
          <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 12 }}>
            <strong>Rooms</strong>
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
                  <td>{r.game_type}</td>
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
                    No rooms yet. Start a match from <Link href="/play">/play</Link>.
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
