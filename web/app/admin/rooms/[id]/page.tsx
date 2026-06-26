"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { Chessboard } from "react-chessboard";
import { Chess } from "chess.js";
import { api, PhoenixEvent, ArenaState, isArenaState } from "@/lib/api";
import { ReconnectingSocket, WsStatus, adminStreamUrl } from "@/lib/ws";
import ArenaCanvas from "@/components/ArenaCanvas";

const START_FEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";

type Tab = "live" | "replay";

export default function RoomDetail({ params }: { params: { id: string } }) {
  const { id } = params;
  const [tab, setTab] = useState<Tab>("live");

  return (
    <div className="container grid" style={{ gap: 24 }}>
      <div>
        <Link href="/admin">← dashboard</Link>
        <h1 style={{ margin: "8px 0" }}>Room {id.slice(0, 8)}…</h1>
        <span className="muted" style={{ fontSize: 12 }}>
          <code>{id}</code>
        </span>
      </div>

      <div className="tabs">
        <div className={`tab ${tab === "live" ? "active" : ""}`} onClick={() => setTab("live")}>
          Live stream
        </div>
        <div className={`tab ${tab === "replay" ? "active" : ""}`} onClick={() => setTab("replay")}>
          Replay
        </div>
      </div>

      {tab === "live" ? <LiveStream roomId={id} /> : <Replay roomId={id} />}
    </div>
  );
}

// --------------------------------------------------------------------------
// Live stream tab
// --------------------------------------------------------------------------
function LiveStream({ roomId }: { roomId: string }) {
  const [events, setEvents] = useState<PhoenixEvent[]>([]);
  const [state, setState] = useState<any>(null);
  const [status, setStatus] = useState<WsStatus>("closed");
  const [gameType, setGameType] = useState<string | null>(null);
  const feedRef = useRef<HTMLDivElement | null>(null);
  // live arena state for the read-only canvas (read each animation frame)
  const arenaRef = useRef<ArenaState | null>(null);

  const updateState = useCallback((st: any) => {
    setState(st);
    if (isArenaState(st)) arenaRef.current = st;
  }, []);

  // seed with existing log + state + room metadata, then stream live
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [ev, st] = await Promise.all([api.events(roomId), api.roomState(roomId)]);
        if (cancelled) return;
        setEvents(ev);
        updateState(st.state ?? null);
      } catch {
        /* backend may be down or room empty */
      }
      try {
        const all = await api.rooms();
        if (cancelled) return;
        const found = all.find((r) => r.id === roomId);
        if (found) setGameType(found.game_type);
      } catch {
        /* ignore */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [roomId, updateState]);

  useEffect(() => {
    const sock = new ReconnectingSocket(adminStreamUrl(roomId), {
      onStatusChange: setStatus,
      onMessage: (msg) => {
        if (msg.type === "event" && (msg as any).event) {
          const ev = (msg as any).event as PhoenixEvent;
          setEvents((prev) => {
            if (prev.some((e) => e.seq === ev.seq)) return prev;
            return [...prev, ev];
          });
          if ((msg as any).state) updateState((msg as any).state);
        } else if (msg.type === "snapshot" && (msg as any).state) {
          updateState((msg as any).state);
        }
      },
    });
    sock.connect();
    return () => sock.close();
  }, [roomId, updateState]);

  // newest at top
  const ordered = useMemo(() => [...events].sort((a, b) => b.seq - a.seq), [events]);

  useEffect(() => {
    // auto-scroll to top (newest)
    if (feedRef.current) feedRef.current.scrollTop = 0;
  }, [ordered.length]);

  const fen: string | null = state?.fen ?? null;
  const isArena = gameType === "arena" || isArenaState(state);

  return (
    <div className="grid" style={{ gap: 24, gridTemplateColumns: "1fr 360px", alignItems: "start" }}>
      <div className="grid" style={{ gap: 24, minWidth: 0 }}>
        <div className="card">
          <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 12 }}>
            <strong>Live event stream</strong>
            <span className={`conn ${status}`}>
              <span className="dot" /> {status}
            </span>
          </div>
          <div className="event-feed" ref={feedRef}>
            {ordered.map((e) => (
              <div className="event-item" key={`${e.seq}-${e.id ?? ""}`}>
                <div className="ev-head">
                  <span>
                    <span className="muted">#{e.seq}</span> <span className="ev-type">{e.type}</span>
                  </span>
                  <span className="muted">{fmtTime(e.timestamp)}</span>
                </div>
                <div className="muted" style={{ marginTop: 2 }}>
                  player: {e.player_id ? e.player_id.slice(0, 8) + "…" : "—"}
                </div>
                {e.payload !== undefined && e.payload !== null && (
                  <code>{JSON.stringify(e.payload)}</code>
                )}
              </div>
            ))}
            {ordered.length === 0 && <p className="muted">No events yet. Waiting for activity…</p>}
          </div>
        </div>

        <div className="card">
          <strong>Current reduced state</strong>
          <p className="muted" style={{ marginTop: 4 }}>
            Folded from the event log — the authoritative server state.
          </p>
          <pre>{state ? JSON.stringify(state, null, 2) : "— (room not active in memory)"}</pre>
        </div>
      </div>

      <div className="card">
        <div className="section-title" style={{ marginBottom: 12 }}>
          {isArena ? "Arena (live)" : "Board"}
        </div>
        {isArena ? (
          isArenaState(state) ? (
            <ArenaCanvas stateRef={arenaRef} selfId={null} maxWidth={360} />
          ) : (
            <p className="muted">Arena room inactive — no live state.</p>
          )
        ) : fen ? (
          <Chessboard
            position={fen}
            arePiecesDraggable={false}
            boardOrientation="white"
            customBoardStyle={{ borderRadius: 8 }}
            customDarkSquareStyle={{ backgroundColor: "#3a4256" }}
            customLightSquareStyle={{ backgroundColor: "#aab0c0" }}
            id="live-board"
          />
        ) : (
          <p className="muted">No board state (non-chess or inactive room).</p>
        )}
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------
// Replay tab
// --------------------------------------------------------------------------
function Replay({ roomId }: { roomId: string }) {
  const [moves, setMoves] = useState<{ from: string; to: string; promotion?: string }[]>([]);
  const [sans, setSans] = useState<string[]>([]);
  const [ply, setPly] = useState(0); // number of moves applied
  const [playing, setPlaying] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const ev = await api.events(roomId);
        if (cancelled) return;
        // extract move events; reconstruct SANs via chess.js
        const moveEvents = ev
          .filter((e) => e.type === "move" && e.payload && e.payload.from && e.payload.to)
          .sort((a, b) => a.seq - b.seq)
          .map((e) => ({
            from: e.payload.from as string,
            to: e.payload.to as string,
            promotion: e.payload.promotion as string | undefined,
          }));

        const c = new Chess();
        const sanList: string[] = [];
        const valid: typeof moveEvents = [];
        for (const m of moveEvents) {
          try {
            const res = c.move({ from: m.from as any, to: m.to as any, promotion: (m.promotion as any) || "q" });
            if (res) {
              sanList.push(res.san);
              valid.push(m);
            }
          } catch {
            break; // stop at first inconsistency
          }
        }
        setMoves(valid);
        setSans(sanList);
        setPly(valid.length); // start at final position
        setErr(null);
      } catch (e: any) {
        setErr(e.message || "could not load events");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [roomId]);

  // reconstruct fen at current ply
  const fen = useMemo(() => {
    const c = new Chess();
    for (let i = 0; i < ply && i < moves.length; i++) {
      const m = moves[i];
      try {
        c.move({ from: m.from as any, to: m.to as any, promotion: (m.promotion as any) || "q" });
      } catch {
        break;
      }
    }
    return c.fen();
  }, [ply, moves]);

  const lastMove = useMemo(() => (ply > 0 ? moves[ply - 1] : null), [ply, moves]);

  const customSquareStyles = useMemo(() => {
    const s: Record<string, React.CSSProperties> = {};
    if (lastMove) {
      s[lastMove.from] = { background: "rgba(255,107,61,0.25)" };
      s[lastMove.to] = { background: "rgba(255,107,61,0.35)" };
    }
    return s;
  }, [lastMove]);

  // autoplay
  useEffect(() => {
    if (!playing) return;
    if (ply >= moves.length) {
      setPlaying(false);
      return;
    }
    const t = setTimeout(() => setPly((p) => Math.min(p + 1, moves.length)), 700);
    return () => clearTimeout(t);
  }, [playing, ply, moves.length]);

  const step = useCallback(
    (d: number) => setPly((p) => Math.max(0, Math.min(moves.length, p + d))),
    [moves.length]
  );

  if (loading) {
    return (
      <div className="card">
        <p className="muted" style={{ margin: 0 }}>Loading match…</p>
      </div>
    );
  }

  if (err) {
    return <div className="banner err">{err}</div>;
  }

  if (moves.length === 0) {
    return (
      <div className="card">
        <p className="muted" style={{ margin: 0 }}>No moves recorded for this room yet.</p>
      </div>
    );
  }

  return (
    <div className="grid" style={{ gap: 24, gridTemplateColumns: "minmax(280px, 440px) 1fr", alignItems: "start" }}>
      <div className="card">
        <Chessboard
          position={fen}
          arePiecesDraggable={false}
          boardOrientation="white"
          customSquareStyles={customSquareStyles}
          customBoardStyle={{ borderRadius: 8 }}
          customDarkSquareStyle={{ backgroundColor: "#3a4256" }}
          customLightSquareStyle={{ backgroundColor: "#aab0c0" }}
          id="replay-board"
        />
        <div className="muted" style={{ textAlign: "center", marginTop: 8, fontSize: 13 }}>
          Move {ply} / {moves.length}
        </div>

        <div className="scrubber" style={{ marginTop: 12 }}>
          <input
            type="range"
            min={0}
            max={moves.length}
            value={ply}
            onChange={(e) => {
              setPlaying(false);
              setPly(Number(e.target.value));
            }}
          />
        </div>

        <div className="replay-controls" style={{ marginTop: 12, justifyContent: "center" }}>
          <button onClick={() => { setPlaying(false); setPly(0); }} disabled={ply === 0}>⏮ start</button>
          <button onClick={() => { setPlaying(false); step(-1); }} disabled={ply === 0}>◀ back</button>
          {playing ? (
            <button className="btn-primary" onClick={() => setPlaying(false)}>⏸ pause</button>
          ) : (
            <button className="btn-primary" onClick={() => { if (ply >= moves.length) setPly(0); setPlaying(true); }}>
              ▶ play
            </button>
          )}
          <button onClick={() => { setPlaying(false); step(1); }} disabled={ply >= moves.length}>next ▶</button>
          <button onClick={() => { setPlaying(false); setPly(moves.length); }} disabled={ply >= moves.length}>end ⏭</button>
        </div>
      </div>

      <div className="card">
        <div className="section-title" style={{ marginBottom: 8 }}>Moves</div>
        <div className="history-list">
          {pairMoves(sans).map((pair, i) => {
            const whitePly = i * 2 + 1;
            const blackPly = i * 2 + 2;
            return (
              <React.Fragment key={i}>
                <span className="num">{i + 1}.</span>
                <span
                  className={`ply ${ply === whitePly ? "current" : ""}`}
                  style={{ cursor: "pointer", padding: "0 4px" }}
                  onClick={() => { setPlaying(false); setPly(whitePly); }}
                >
                  {pair[0]}
                </span>
                <span
                  className={`ply ${ply === blackPly ? "current" : ""}`}
                  style={{ cursor: pair[1] ? "pointer" : "default", padding: "0 4px" }}
                  onClick={() => pair[1] && (setPlaying(false), setPly(blackPly))}
                >
                  {pair[1] ?? ""}
                </span>
              </React.Fragment>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function pairMoves(history: string[]): [string, string?][] {
  const pairs: [string, string?][] = [];
  for (let i = 0; i < history.length; i += 2) pairs.push([history[i], history[i + 1]]);
  return pairs;
}

function fmtTime(ts: string): string {
  try {
    return new Date(ts).toLocaleTimeString();
  } catch {
    return ts;
  }
}
