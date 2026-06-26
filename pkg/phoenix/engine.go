package phoenix

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fazal/phoenix/deploy/migrations"
	"github.com/fazal/phoenix/internal/admin"
	"github.com/fazal/phoenix/internal/auth"
	"github.com/fazal/phoenix/internal/config"
	"github.com/fazal/phoenix/internal/core"
	"github.com/fazal/phoenix/internal/eventbus"
	"github.com/fazal/phoenix/internal/gateway"
	"github.com/fazal/phoenix/internal/httpx"
	"github.com/fazal/phoenix/internal/leaderboard"
	"github.com/fazal/phoenix/internal/matchmaking"
	"github.com/fazal/phoenix/internal/presence"
	"github.com/fazal/phoenix/internal/room"
	"github.com/fazal/phoenix/internal/state"
	"github.com/fazal/phoenix/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Engine is the developer-facing handle to Phoenix. A game registers its rules
// (OnJoin/OnLeave/OnEvent) and calls Run; Phoenix boots the entire backend —
// auth, rooms, WebSocket gateway, event store, state engine, and admin API —
// against this one game. This is the "import the SDK, add rules, deploy" model.
type Engine struct {
	gameType      string
	reducers      map[string]Reducer
	onJoin        func(Player)
	onLeave       func(Player)
	initState     func() any
	derive        Deriver
	restore       func([]byte) any
	snapshotEvery int64

	store EventStore // optional override (defaults to Postgres)
}

// Option configures the Engine.
type Option func(*Engine)

// WithGameType labels rooms created by this engine (e.g. "chess").
func WithGameType(t string) Option { return func(e *Engine) { e.gameType = t } }

// WithEventStore injects a custom EventStore (e.g. in-memory for tests). When
// unset, Phoenix uses the bundled Postgres store. This is the plugin seam in
// action — the engine never assumes a concrete store.
func WithEventStore(s EventStore) Option { return func(e *Engine) { e.store = s } }

// WithSnapshotEvery enables state snapshots every n events. Combined with
// RestoreState, room rehydration (reconnect / crash recovery) restores the
// latest snapshot and folds only the events after it, bounding recovery cost.
func WithSnapshotEvery(n int64) Option { return func(e *Engine) { e.snapshotEvery = n } }

// New creates an Engine with sane defaults.
func New(opts ...Option) *Engine {
	e := &Engine{
		gameType:  "game",
		reducers:  make(map[string]Reducer),
		initState: func() any { return map[string]any{} },
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// OnJoin registers a callback fired when a player joins a room.
func (e *Engine) OnJoin(f func(Player)) { e.onJoin = f }

// OnLeave registers a callback fired when a player leaves a room.
func (e *Engine) OnLeave(f func(Player)) { e.onLeave = f }

// OnEvent registers the reducer for an event type. The reducer folds the event
// into prior state and returns new state. Keep it pure for deterministic replay.
func (e *Engine) OnEvent(eventType string, r Reducer) { e.reducers[eventType] = r }

// InitialState sets the factory for a new room's starting state.
func (e *Engine) InitialState(f func() any) { e.initState = f }

// Derive registers a function that emits server-authored domain events from a
// state transition (e.g. MatchStarted/MatchEnded). These flow through the same
// persist -> publish -> broadcast pipeline, so bus consumers (leaderboard, etc.)
// react to meaningful domain events.
func (e *Engine) Derive(f Deriver) { e.derive = f }

// RestoreState registers a decoder that turns a snapshot's JSON back into the
// game's typed state. Required for snapshots (see WithSnapshotEvery), since the
// engine stores state as an opaque value it can't otherwise reconstruct.
func (e *Engine) RestoreState(f func([]byte) any) { e.restore = f }

// Run boots the full backend and blocks until the process is signalled. It
// returns an error only on a fatal startup or shutdown failure.
func (e *Engine) Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("cannot reach postgres: " + err.Error())
	}

	// Apply the embedded schema so Phoenix boots against any empty Postgres
	// (local, Neon, Supabase, RDS) with no manual migration step.
	if err := migrations.Apply(ctx, pool); err != nil {
		return errors.New("migrations failed: " + err.Error())
	}

	// The event bus: the store publishes appended events to it; consumer
	// services (presence, leaderboard, dashboard stream) subscribe and react
	// asynchronously. PHOENIX_BUS=redis makes it cluster-wide (multi-node) by
	// fanning events through Redis; otherwise it's a fast in-process bus.
	var bus eventbus.Bus = eventbus.NewInProcess()
	if os.Getenv("PHOENIX_BUS") == "redis" {
		if rb, err := eventbus.NewRedisBus(cfg.RedisURL); err != nil {
			log.Printf("phoenix: redis bus unavailable, using in-process (%v)", err)
		} else {
			bus = rb
			log.Println("phoenix: distributed event bus (redis)")
		}
	}

	// EventStore: injected override or default Postgres (the plugin seam). The
	// store publishes to the bus after each durable append.
	es := e.store
	if es == nil {
		es = store.NewFromPool(pool, bus)
	}

	authSvc := auth.NewService(pool, cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	authH := auth.NewHandler(authSvc)
	roomSvc := room.NewService(pool)
	roomH := room.NewHandler(roomSvc)
	mmSvc := matchmaking.NewService(roomSvc)
	mmH := matchmaking.NewHandler(mmSvc)

	// Snapshots (optional): bound rehydration cost for long matches. Requires a
	// RestoreState decoder and a snapshot interval.
	var snaps core.SnapshotStore
	if e.restore != nil && e.snapshotEvery > 0 {
		ps := store.NewPostgresSnapshots(pool)
		if err := ps.EnsureSchema(ctx); err != nil {
			log.Printf("phoenix: snapshots disabled (schema: %v)", err)
		} else {
			snaps = ps
			log.Printf("phoenix: snapshots every %d events", e.snapshotEvery)
		}
	}

	hub := state.NewHub(es, state.Handlers{
		Reducers:      e.reducers,
		InitState:     e.initState,
		OnJoin:        e.onJoin,
		OnLeave:       e.onLeave,
		Derive:        e.derive,
		Snapshots:     snaps,
		Restore:       e.restore,
		SnapshotEvery: e.snapshotEvery,
	})
	gw := gateway.New(authH, authSvc, roomSvc, hub, cfg.HeartbeatInterval, cfg.ReconnectWindow)

	// --- bus consumers (event-driven read models / projections) --------------

	// Presence (Redis). Best-effort: if Redis is down the platform still runs,
	// just without live presence — a slow/absent consumer never affects games.
	var pres core.PresenceStore
	if p, err := presence.NewRedis(ctx, cfg.RedisURL); err != nil {
		log.Printf("phoenix: presence disabled (redis: %v)", err)
	} else {
		pres = p
		bus.Subscribe("presence", p.Handle)
	}

	// Leaderboard (Postgres read model). Rebuild from the log on boot, then keep
	// it current by reacting to MatchEnded events from the bus.
	lb := leaderboard.NewService(pool)
	if err := lb.EnsureSchema(ctx); err != nil {
		log.Printf("phoenix: leaderboard schema: %v", err)
	}
	if err := lb.RebuildFromLog(ctx); err != nil {
		log.Printf("phoenix: leaderboard rebuild: %v", err)
	}
	bus.Subscribe("leaderboard", lb.Handle)
	lbH := leaderboard.NewHandler(lb)

	adminH := admin.NewHandler(es, roomSvc, hub, pool, bus, pres)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/ws", gw.ServeWS) // auth handled inside the handshake
	authH.Routes(mux)
	adminH.Routes(mux)
	lbH.Routes(mux)

	// Room + matchmaking routes work anonymously but pick up the owner when a
	// token is present.
	openMux := http.NewServeMux()
	roomH.Routes(openMux)
	mmH.Routes(openMux)
	mux.Handle("/rooms", authH.Optional(openMux))
	mux.Handle("/rooms/", authH.Optional(openMux))
	mux.Handle("/matchmake", authH.Optional(openMux))

	srv := &http.Server{
		Addr:        ":" + cfg.Port,
		Handler:     httpx.CORS(mux),
		ReadTimeout: 0, // WebSockets are long-lived
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		log.Println("phoenix: shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	log.Printf("phoenix: game=%q listening on :%s", e.gameType, cfg.Port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
