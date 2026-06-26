"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  ArenaState,
  loadSession,
  saveSession,
  clearSession,
  Session,
} from "@/lib/api";
import { ReconnectingSocket, WsStatus, WsMessage, gameSocketUrl } from "@/lib/ws";
import ArenaCanvas from "@/components/ArenaCanvas";

export default function ArenaPage() {
  const [session, setSession] = useState<Session | null>(null);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    setSession(loadSession());
    setReady(true);
  }, []);

  if (!ready) {
    return (
      <div className="container">
        <p className="muted">Loading…</p>
      </div>
    );
  }

  if (!session) return <LoginGate onLogin={setSession} />;

  return (
    <Arena
      session={session}
      onLogout={() => {
        clearSession();
        setSession(null);
      }}
    />
  );
}

function LoginGate({ onLogin }: { onLogin: (s: Session) => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    const displayName = name.trim() || "Guest";
    setBusy(true);
    setErr(null);
    try {
      const res = await api.loginGuest(displayName);
      const s: Session = {
        token: res.tokens.access_token,
        userId: res.user.id,
        displayName: res.user.display_name,
      };
      saveSession(s);
      onLogin(s);
    } catch (e: any) {
      setErr(e.message || "login failed — is the backend running?");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="container">
      <div className="gate">
        <div className="card">
          <h2 style={{ marginTop: 0 }}>Enter the Arena</h2>
          <p className="muted" style={{ marginTop: 0 }}>
            Pick a display name to play as a guest. Move with WASD or the arrow keys.
          </p>
          <div className="row" style={{ marginTop: 16 }}>
            <input
              className="field"
              type="text"
              placeholder="Display name"
              value={name}
              maxLength={24}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && submit()}
            />
            <button className="btn-primary" onClick={submit} disabled={busy}>
              {busy ? "…" : "Enter →"}
            </button>
          </div>
          {err && (
            <div className="banner err" style={{ marginTop: 12 }}>
              {err}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

const SPEED = 6; // px per frame the intended position moves while keys held
const SEND_HZ = 15;

function Arena({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const [roomId, setRoomId] = useState<string | null>(null);
  const [joinId, setJoinId] = useState("");
  const [status, setStatus] = useState<WsStatus>("closed");
  const [error, setError] = useState<string | null>(null);
  const [matchBusy, setMatchBusy] = useState(false);
  // mirror of the latest state into React (for the HUD); the canvas reads the ref.
  const [hudState, setHudState] = useState<ArenaState | null>(null);

  const socketRef = useRef<ReconnectingSocket | null>(null);
  const stateRef = useRef<ArenaState | null>(null);
  // local "intended" position for the controlling player
  const intentRef = useRef<{ x: number; y: number } | null>(null);
  const keys = useRef<Record<string, boolean>>({});
  const rafRef = useRef<number | null>(null);
  const lastSent = useRef(0);

  const selfId = session.userId;
  const inGame = !!hudState && !!hudState.players[selfId];

  const applyState = useCallback(
    (st: ArenaState) => {
      stateRef.current = st;
      setHudState(st);
      // initialise / reconcile intended position from server authority
      const me = st.players[selfId];
      if (me) {
        if (!intentRef.current) {
          intentRef.current = { x: me.x, y: me.y };
        } else {
          // gently pull intent toward server truth so clamps/anti-teleport land
          intentRef.current.x += (me.x - intentRef.current.x) * 0.35;
          intentRef.current.y += (me.y - intentRef.current.y) * 0.35;
        }
      }
    },
    [selfId]
  );

  // connect socket whenever roomId changes
  useEffect(() => {
    if (!roomId) return;
    setError(null);
    stateRef.current = null;
    intentRef.current = null;
    setHudState(null);
    const sock = new ReconnectingSocket(gameSocketUrl(roomId, session.token), {
      onStatusChange: setStatus,
      onMessage: (msg: WsMessage) => {
        if ((msg.type === "snapshot" || msg.type === "event") && (msg as any).state) {
          applyState((msg as any).state as ArenaState);
        } else if (msg.type === "error") {
          setError((msg as any).error || "intent rejected");
          setTimeout(() => setError(null), 3000);
        }
      },
    });
    socketRef.current = sock;
    sock.connect();
    return () => {
      sock.close();
      socketRef.current = null;
    };
  }, [roomId, session.token, applyState]);

  // keyboard input
  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      const k = e.key.toLowerCase();
      if (["arrowup", "arrowdown", "arrowleft", "arrowright", "w", "a", "s", "d"].includes(k)) {
        keys.current[k] = true;
        e.preventDefault();
      }
    };
    const up = (e: KeyboardEvent) => {
      keys.current[e.key.toLowerCase()] = false;
    };
    window.addEventListener("keydown", down);
    window.addEventListener("keyup", up);
    return () => {
      window.removeEventListener("keydown", down);
      window.removeEventListener("keyup", up);
    };
  }, []);

  // input + send loop
  useEffect(() => {
    function tick() {
      const st = stateRef.current;
      const intent = intentRef.current;
      if (st && intent && st.players[selfId]) {
        let dx = 0;
        let dy = 0;
        const k = keys.current;
        if (k["arrowup"] || k["w"]) dy -= 1;
        if (k["arrowdown"] || k["s"]) dy += 1;
        if (k["arrowleft"] || k["a"]) dx -= 1;
        if (k["arrowright"] || k["d"]) dx += 1;
        if (dx !== 0 || dy !== 0) {
          // normalise diagonal
          const len = Math.hypot(dx, dy) || 1;
          intent.x = clamp(intent.x + (dx / len) * SPEED, 0, st.w);
          intent.y = clamp(intent.y + (dy / len) * SPEED, 0, st.h);
        }
        // throttle sends to ~15/sec
        const now = performance.now();
        if (now - lastSent.current >= 1000 / SEND_HZ) {
          lastSent.current = now;
          socketRef.current?.send({
            type: "move",
            payload: { x: Math.round(intent.x), y: Math.round(intent.y) },
          });
        }
      }
      rafRef.current = requestAnimationFrame(tick);
    }
    rafRef.current = requestAnimationFrame(tick);
    return () => {
      if (rafRef.current) cancelAnimationFrame(rafRef.current);
    };
  }, [selfId]);

  async function joinArena() {
    setMatchBusy(true);
    setError(null);
    try {
      const res = await api.matchmake(session.token, "arena");
      setRoomId(res.room.id);
    } catch (e: any) {
      setError(e.message || "matchmaking failed — is the backend running?");
    } finally {
      setMatchBusy(false);
    }
  }

  function joinRoom() {
    const id = joinId.trim();
    if (id) setRoomId(id);
  }

  // live leaderboard of players in this arena
  const ranked = useMemo(() => {
    if (!hudState) return [];
    return Object.entries(hudState.players)
      .map(([id, p]) => ({ id, ...p }))
      .sort((a, b) => b.score - a.score);
  }, [hudState]);

  const me = hudState?.players[selfId] ?? null;
  const playerCount = hudState ? Object.keys(hudState.players).length : 0;

  return (
    <div className="container">
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 12,
          marginBottom: 16,
        }}
      >
        <div>
          <h1 style={{ margin: "4px 0" }}>⚡ Arena</h1>
          <span className="muted">
            Signed in as <strong>{session.displayName}</strong>
            {roomId && !inGame && status === "open" ? " · spectating" : ""}
          </span>
        </div>
        <div className="row">
          <button className="btn-green" onClick={joinArena} disabled={matchBusy}>
            {matchBusy ? "Joining…" : "Join Arena"}
          </button>
          <button className="btn-ghost" onClick={onLogout}>
            Sign out
          </button>
        </div>
      </div>

      <div
        className="card"
        style={{ marginBottom: 16, display: "flex", gap: 12, flexWrap: "wrap", alignItems: "center" }}
      >
        <input
          className="field"
          type="text"
          placeholder="Room id to join / spectate (e.g. demo room)"
          value={joinId}
          onChange={(e) => setJoinId(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && joinRoom()}
          style={{ minWidth: 260 }}
        />
        <button onClick={joinRoom} disabled={!joinId.trim()}>
          Join room
        </button>
        {roomId && (
          <span className="muted" style={{ fontSize: 12 }}>
            Room: <code>{roomId}</code>
          </span>
        )}
        <span className={`conn ${status}`} style={{ marginLeft: "auto" }}>
          <span className="dot" /> {status}
        </span>
      </div>

      {error && <div className="banner err" style={{ marginBottom: 16 }}>{error}</div>}
      {(status === "reconnecting" || status === "connecting") && roomId && (
        <div className="banner warn" style={{ marginBottom: 16 }}>
          {status === "connecting"
            ? "Connecting…"
            : "Reconnecting… the server will resend a snapshot."}
        </div>
      )}
      {roomId && status === "open" && !inGame && (
        <div className="banner warn" style={{ marginBottom: 16 }}>
          You&apos;re spectating this arena (not seated as a player). Watch the action live.
        </div>
      )}

      {!roomId ? (
        <div className="card" style={{ textAlign: "center", padding: 48 }}>
          <p className="muted" style={{ margin: 0 }}>
            Hit <strong>Join Arena</strong> to drop into a real-time field, or join a specific
            room id (e.g. the dashboard demo room) to spectate.
          </p>
        </div>
      ) : (
        <div className="game-layout" style={{ gridTemplateColumns: "minmax(320px, 1fr) 300px" }}>
          <div className="board-wrap">
            <ArenaCanvas
              stateRef={stateRef}
              selfId={inGame ? selfId : null}
              selfIntentRef={intentRef}
              maxWidth={1000}
            />
            <div className="muted" style={{ marginTop: 10, fontSize: 12, textAlign: "center" }}>
              {inGame
                ? "Move: WASD / arrow keys · eat the green dots to score"
                : "Spectator view — no controls"}
            </div>
          </div>

          <div className="grid" style={{ gap: 16 }}>
            <div className="card">
              <div className="section-title" style={{ marginBottom: 10 }}>You</div>
              {me ? (
                <div className="player-row you">
                  <span className="player-color">
                    <span className="dot" style={{ background: me.color, borderColor: me.color }} />
                    {me.name}
                  </span>
                  <span style={{ color: "var(--green)", fontWeight: 700 }}>{me.score}</span>
                </div>
              ) : (
                <p className="muted" style={{ margin: 0 }}>
                  {hudState ? "Spectating — not seated." : "Waiting for state…"}
                </p>
              )}
              <div className="kvbar" style={{ marginTop: 10 }}>
                <span>
                  <span className="k">players</span> {playerCount}
                </span>
                <span>
                  <span className="k">food</span> {hudState?.food.length ?? 0}
                </span>
              </div>
            </div>

            <div className="card">
              <div className="section-title" style={{ marginBottom: 10 }}>Leaderboard</div>
              {ranked.length === 0 ? (
                <p className="muted" style={{ margin: 0 }}>No players yet.</p>
              ) : (
                <table>
                  <thead>
                    <tr>
                      <th>#</th>
                      <th>Player</th>
                      <th>Score</th>
                    </tr>
                  </thead>
                  <tbody>
                    {ranked.map((p, i) => (
                      <tr key={p.id}>
                        <td className="muted">{i + 1}</td>
                        <td>
                          <span className="player-color">
                            <span
                              className="dot"
                              style={{ background: p.color, borderColor: p.color }}
                            />
                            {p.name}
                            {p.id === selfId && <span className="turn-tag"> · you</span>}
                          </span>
                        </td>
                        <td style={{ color: "var(--green)" }}>{p.score}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, v));
}
