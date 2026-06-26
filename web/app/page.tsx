import Link from "next/link";

export default function Landing() {
  return (
    <div className="container">
      <section className="hero">
        <span className="eyebrow">multiplayer backend platform</span>
        <h1>
          Write game rules,
          <br />
          ship a <span className="accent">backend</span>.
        </h1>
        <p className="lead muted">
          Phoenix is the multiplayer backend platform where every game is just a
          reducer over an append-only event log. Plug in your rules, get realtime
          rooms, replay, and an authoritative server for free.
        </p>
        <div className="cta-row">
          <Link href="/play">
            <button className="btn-primary">Play Chess →</button>
          </Link>
          <Link href="/admin">
            <button className="btn-ghost">Admin Dashboard</button>
          </Link>
        </div>
      </section>

      <section className="explainer">
        <div className="section-title">Everything is an event</div>
        <p style={{ margin: "0 0 4px", lineHeight: 1.7 }}>
          A move isn&apos;t mutated state — it&apos;s an <code>event</code> appended to a
          per-room log. The server folds those events through your game&apos;s reducer to
          produce the authoritative state. Because the log is the source of truth, you
          get time-travel replay, reconnection, and audit for free.
        </p>
        <div className="flow">
          <span className="pill">client sends move</span>
          <span className="arrow">→</span>
          <span className="pill">server validates</span>
          <span className="arrow">→</span>
          <span className="pill">append event</span>
          <span className="arrow">→</span>
          <span className="pill">reduce → state</span>
          <span className="arrow">→</span>
          <span className="pill">broadcast to room</span>
        </div>
      </section>

      <section>
        <div className="section-title">Built in</div>
        <div className="grid feature-grid">
          <div className="card feature">
            <div className="ico">🧾</div>
            <h3>Event sourcing</h3>
            <p>
              Every action is an immutable, ordered event. State is a pure fold over the
              log — deterministic and replayable.
            </p>
          </div>
          <div className="card feature">
            <div className="ico">⏪</div>
            <h3>Replay & time-travel</h3>
            <p>
              Reconstruct any match at any ply. Scrub through history, debug bugs, build
              spectator highlights.
            </p>
          </div>
          <div className="card feature">
            <div className="ico">⚡</div>
            <h3>Realtime WebSockets</h3>
            <p>
              Rooms stream snapshots and events over WS. Reconnect and the server resends
              an authoritative snapshot.
            </p>
          </div>
          <div className="card feature">
            <div className="ico">🧩</div>
            <h3>Plugin architecture</h3>
            <p>
              A game is a reducer + validator. Drop in chess today, ship your own ruleset
              tomorrow — same backend.
            </p>
          </div>
        </div>
      </section>

      <section className="explainer" style={{ display: "flex", flexWrap: "wrap", justifyContent: "space-between", alignItems: "center", gap: 16 }}>
        <div>
          <div className="section-title" style={{ marginBottom: 8 }}>Try it now</div>
          <strong style={{ fontSize: 18 }}>Jump into a live chess match.</strong>
          <p className="muted" style={{ margin: "6px 0 0" }}>
            Guest login, quick match, fully authoritative server.
          </p>
        </div>
        <Link href="/play">
          <button className="btn-green">Play Chess →</button>
        </Link>
      </section>

      <footer className="footer">
        Phoenix · multiplayer backend platform · everything is an event
      </footer>
    </div>
  );
}
