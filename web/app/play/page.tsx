"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Chessboard } from "react-chessboard";
import { Chess, Square } from "chess.js";
import {
  api,
  ChessState,
  loadSession,
  saveSession,
  clearSession,
  Session,
} from "@/lib/api";
import {
  ReconnectingSocket,
  WsStatus,
  WsMessage,
  gameSocketUrl,
} from "@/lib/ws";

const START_FEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";

type Promotion = "q" | "r" | "b" | "n";

export default function PlayPage() {
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

  if (!session) {
    return <LoginGate onLogin={setSession} />;
  }

  return (
    <Game
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
          <h2 style={{ marginTop: 0 }}>Enter the arena</h2>
          <p className="muted" style={{ marginTop: 0 }}>
            Pick a display name to play as a guest.
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
              {busy ? "…" : "Play →"}
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

function Game({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const [roomId, setRoomId] = useState<string | null>(null);
  const [joinId, setJoinId] = useState("");
  const [state, setState] = useState<ChessState | null>(null);
  const [status, setStatus] = useState<WsStatus>("closed");
  const [error, setError] = useState<string | null>(null);
  const [matchBusy, setMatchBusy] = useState(false);

  const socketRef = useRef<ReconnectingSocket | null>(null);
  // local chess engine mirrors the authoritative fen, used for legal-move UX
  const engineRef = useRef(new Chess());
  const [fen, setFen] = useState(START_FEN);
  const [pendingPromo, setPendingPromo] = useState<{ from: Square; to: Square } | null>(null);
  const [selected, setSelected] = useState<Square | null>(null);

  const myColor: "w" | "b" | null = useMemo(() => {
    if (!state) return null;
    if (state.players.w === session.userId) return "w";
    if (state.players.b === session.userId) return "b";
    return null;
  }, [state, session.userId]);

  const isSpectator = myColor === null;
  const boardOrientation = myColor === "b" ? "black" : "white";

  const applyState = useCallback((st: ChessState) => {
    setState(st);
    if (st.fen) {
      setFen(st.fen);
      try {
        engineRef.current.load(st.fen);
      } catch {
        /* ignore bad fen */
      }
    }
  }, []);

  // connect socket whenever roomId changes
  useEffect(() => {
    if (!roomId) return;
    setError(null);
    const sock = new ReconnectingSocket(gameSocketUrl(roomId, session.token), {
      onStatusChange: setStatus,
      onMessage: (msg: WsMessage) => {
        if (msg.type === "snapshot" && msg.state) {
          applyState(msg.state as ChessState);
        } else if (msg.type === "event" && msg.state) {
          applyState(msg.state as ChessState);
        } else if (msg.type === "error") {
          // server rejected; revert board to authoritative fen
          setError(msg.error || "move rejected");
          setState((cur) => {
            if (cur?.fen) {
              setFen(cur.fen);
              try {
                engineRef.current.load(cur.fen);
              } catch {}
            }
            return cur;
          });
          setTimeout(() => setError(null), 3500);
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

  async function quickMatch() {
    setMatchBusy(true);
    setError(null);
    try {
      const res = await api.matchmake(session.token, "chess");
      setState(null);
      setFen(START_FEN);
      engineRef.current.reset();
      setRoomId(res.room.id);
    } catch (e: any) {
      setError(e.message || "matchmaking failed — is the backend running?");
    } finally {
      setMatchBusy(false);
    }
  }

  function joinRoom() {
    const id = joinId.trim();
    if (!id) return;
    setState(null);
    setFen(START_FEN);
    engineRef.current.reset();
    setRoomId(id);
  }

  const canMove =
    !!myColor &&
    !!state &&
    state.turn === myColor &&
    (state.status === "active" || state.status === "check");

  function sendMove(from: Square, to: Square, promotion?: Promotion) {
    const sock = socketRef.current;
    if (!sock) return false;
    // optimistic local validation for instant feedback
    const snapshot = engineRef.current.fen();
    let result;
    try {
      result = engineRef.current.move({ from, to, promotion: promotion ?? "q" });
    } catch {
      result = null;
    }
    if (!result) {
      // illegal locally — keep board as-is
      engineRef.current.load(snapshot);
      return false;
    }
    setFen(engineRef.current.fen());
    const ok = sock.send({ type: "move", payload: { from, to, ...(promotion ? { promotion } : {}) } });
    if (!ok) {
      // not connected; revert optimistic move
      engineRef.current.load(snapshot);
      setFen(snapshot);
      setError("not connected — move not sent");
      setTimeout(() => setError(null), 2500);
      return false;
    }
    return true;
  }

  function needsPromotion(from: Square, to: Square): boolean {
    const piece = engineRef.current.get(from);
    if (!piece || piece.type !== "p") return false;
    const rank = to[1];
    return (piece.color === "w" && rank === "8") || (piece.color === "b" && rank === "1");
  }

  function onPieceDrop(from: Square, to: Square): boolean {
    if (!canMove) return false;
    setSelected(null);
    if (needsPromotion(from, to)) {
      setPendingPromo({ from, to });
      return false; // wait for promotion choice; board snaps back until confirmed
    }
    return sendMove(from, to);
  }

  function onSquareClick(square: Square) {
    if (!canMove) return;
    if (selected) {
      if (square === selected) {
        setSelected(null);
        return;
      }
      // attempt move
      if (needsPromotion(selected, square)) {
        setPendingPromo({ from: selected, to: square });
        setSelected(null);
        return;
      }
      const moved = sendMove(selected, square);
      setSelected(null);
      if (!moved) {
        // maybe re-select a friendly piece
        const piece = engineRef.current.get(square);
        if (piece && piece.color === myColor) setSelected(square);
      }
      return;
    }
    const piece = engineRef.current.get(square);
    if (piece && piece.color === myColor) setSelected(square);
  }

  function confirmPromotion(promo: Promotion) {
    if (!pendingPromo) return;
    sendMove(pendingPromo.from, pendingPromo.to, promo);
    setPendingPromo(null);
  }

  // highlight legal moves for selected piece
  const customSquareStyles = useMemo(() => {
    const styles: Record<string, React.CSSProperties> = {};
    if (state?.lastMove) {
      styles[state.lastMove.from] = { background: "rgba(255,107,61,0.25)" };
      styles[state.lastMove.to] = { background: "rgba(255,107,61,0.35)" };
    }
    if (selected) {
      styles[selected] = { background: "rgba(61,220,132,0.45)" };
      try {
        const moves = engineRef.current.moves({ square: selected, verbose: true }) as any[];
        for (const m of moves) {
          styles[m.to] = {
            ...(styles[m.to] || {}),
            background: "radial-gradient(circle, rgba(61,220,132,0.55) 22%, transparent 24%)",
          };
        }
      } catch {}
    }
    return styles;
  }, [selected, state, fen]);

  return (
    <div className="container">
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", flexWrap: "wrap", gap: 12, marginBottom: 16 }}>
        <div>
          <h1 style={{ margin: "4px 0" }}>Chess</h1>
          <span className="muted">
            Signed in as <strong>{session.displayName}</strong>{" "}
            {isSpectator && state ? "· spectating" : ""}
          </span>
        </div>
        <div className="row">
          <button className="btn-green" onClick={quickMatch} disabled={matchBusy}>
            {matchBusy ? "Matching…" : "Quick Match"}
          </button>
          <button className="btn-ghost" onClick={onLogout}>
            Sign out
          </button>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 16, display: "flex", gap: 12, flexWrap: "wrap", alignItems: "center" }}>
        <input
          className="field"
          type="text"
          placeholder="Room id to join / spectate"
          value={joinId}
          onChange={(e) => setJoinId(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && joinRoom()}
          style={{ minWidth: 240 }}
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
      {roomId && state?.status === "waiting" && !isSpectator && (
        <div className="banner warn" style={{ marginBottom: 16 }}>
          Waiting for an opponent. Share this room id — open a second tab (or send it
          to a friend) and use <strong>Join room</strong>:{" "}
          <code>{roomId}</code>
        </div>
      )}
      {(status === "reconnecting" || status === "connecting") && roomId && (
        <div className="banner warn" style={{ marginBottom: 16 }}>
          {status === "connecting" ? "Connecting…" : "Reconnecting… the server will resend a snapshot."}
        </div>
      )}

      {!roomId ? (
        <div className="card" style={{ textAlign: "center", padding: 48 }}>
          <p className="muted" style={{ margin: 0 }}>
            Hit <strong>Quick Match</strong> to be auto-paired into a chess room, or join a
            specific room id to spectate / rejoin.
          </p>
        </div>
      ) : (
        <div className="game-layout">
          <div className="board-wrap">
            <Chessboard
              position={fen}
              onPieceDrop={(s, t) => onPieceDrop(s as Square, t as Square)}
              onSquareClick={(sq) => onSquareClick(sq as Square)}
              boardOrientation={boardOrientation}
              arePiecesDraggable={canMove}
              customSquareStyles={customSquareStyles}
              customBoardStyle={{ borderRadius: 8 }}
              customDarkSquareStyle={{ backgroundColor: "#3a4256" }}
              customLightSquareStyle={{ backgroundColor: "#aab0c0" }}
              id="play-board"
            />
            {pendingPromo && (
              <PromotionPicker color={myColor === "b" ? "b" : "w"} onPick={confirmPromotion} onCancel={() => setPendingPromo(null)} />
            )}
          </div>

          <Hud state={state} myColor={myColor} isSpectator={isSpectator} />
        </div>
      )}
    </div>
  );
}

function PromotionPicker({
  color,
  onPick,
  onCancel,
}: {
  color: "w" | "b";
  onPick: (p: Promotion) => void;
  onCancel: () => void;
}) {
  const pieces: Promotion[] = ["q", "r", "b", "n"];
  const glyphs: Record<Promotion, string> = color === "w"
    ? { q: "♕", r: "♖", b: "♗", n: "♘" }
    : { q: "♛", r: "♜", b: "♝", n: "♞" };
  return (
    <div style={{ marginTop: 12, display: "flex", gap: 8, alignItems: "center", justifyContent: "center" }}>
      <span className="muted" style={{ fontSize: 13 }}>Promote to:</span>
      {pieces.map((p) => (
        <button key={p} onClick={() => onPick(p)} style={{ fontSize: 22, lineHeight: 1, padding: "4px 12px" }}>
          {glyphs[p]}
        </button>
      ))}
      <button className="btn-ghost" onClick={onCancel}>cancel</button>
    </div>
  );
}

function Hud({
  state,
  myColor,
  isSpectator,
}: {
  state: ChessState | null;
  myColor: "w" | "b" | null;
  isSpectator: boolean;
}) {
  if (!state) {
    return (
      <div className="grid" style={{ gap: 16 }}>
        <div className="card">
          <p className="muted" style={{ margin: 0 }}>Waiting for game state…</p>
        </div>
      </div>
    );
  }

  const endBanner = endState(state);

  return (
    <div className="grid" style={{ gap: 16 }}>
      {endBanner && <div className={`status-banner ${endBanner.cls}`}>{endBanner.text}</div>}

      <div className="card">
        <PlayerRow color="b" state={state} myColor={myColor} />
        <PlayerRow color="w" state={state} myColor={myColor} />
        <div className="status-line muted" style={{ marginTop: 10 }}>
          {state.status === "waiting"
            ? "Waiting for an opponent…"
            : `${state.turn === "w" ? "White" : "Black"} to move`}
          {isSpectator && " · spectator view"}
        </div>
      </div>

      <div className="card">
        <div className="section-title" style={{ marginBottom: 8 }}>Move history</div>
        {state.history.length === 0 ? (
          <p className="muted" style={{ margin: 0 }}>No moves yet.</p>
        ) : (
          <div className="history-list">
            {pairMoves(state.history).map((pair, i) => (
              <React.Fragment key={i}>
                <span className="num">{i + 1}.</span>
                <span>{pair[0]}</span>
                <span>{pair[1] ?? ""}</span>
              </React.Fragment>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function PlayerRow({
  color,
  state,
  myColor,
}: {
  color: "w" | "b";
  state: ChessState;
  myColor: "w" | "b" | null;
}) {
  const seated = state.players[color];
  const active = state.turn === color && (state.status === "active" || state.status === "check");
  const isYou = myColor === color;
  return (
    <div className={`player-row ${active ? "active" : ""} ${isYou ? "you" : ""}`}>
      <span className="player-color">
        <span className={`dot ${color}`} />
        {color === "w" ? "White" : "Black"}
        {isYou && <span className="turn-tag">· you</span>}
      </span>
      <span className="muted" style={{ fontSize: 12 }}>
        {seated ? `${seated.slice(0, 8)}…` : "open seat"}
        {active && <span className="turn-tag"> ● turn</span>}
      </span>
    </div>
  );
}

function endState(state: ChessState): { text: string; cls: string } | null {
  switch (state.status) {
    case "checkmate": {
      const winner = state.winner === "w" ? "White" : state.winner === "b" ? "Black" : "";
      return { text: winner ? `Checkmate — ${winner} wins` : "Checkmate", cls: "win" };
    }
    case "stalemate":
      return { text: "Stalemate — draw", cls: "draw" };
    case "draw":
      return { text: "Draw", cls: "draw" };
    case "check":
      return { text: "Check!", cls: "check" };
    default:
      return null;
  }
}

function pairMoves(history: string[]): [string, string?][] {
  const pairs: [string, string?][] = [];
  for (let i = 0; i < history.length; i += 2) {
    pairs.push([history[i], history[i + 1]]);
  }
  return pairs;
}
