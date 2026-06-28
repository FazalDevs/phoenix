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
	"github.com/fazal/phoenix/internal/bots"
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

// Engine is the developer-facing handle to Phoenix. Register one or more games
// (each a set of reducers) and call Run; Phoenix boots the entire backend — auth,
// rooms, WebSocket gateway, event store, state engine, projections, and admin API
// — hosting all registered games on one process. "Import the SDK, add rules, deploy."
type Engine struct {
	games map[string]*Game
	store EventStore // optional override (defaults to Postgres)
}

// Game holds the rules for a single game type.
type Game struct {
	gameType      string
	reducers      map[string]Reducer
	onJoin        func(Player)
	onLeave       func(Player)
	initState     func() any
	derive        Deriver
	restore       func([]byte) any
	snapshotEvery int64
}

// Option configures the Engine.
type Option func(*Engine)

// WithEventStore injects a custom EventStore (e.g. in-memory for tests). When
// unset, Phoenix uses the bundled Postgres store — the plugin seam in action.
func WithEventStore(s EventStore) Option { return func(e *Engine) { e.store = s } }

// New creates an Engine.
func New(opts ...Option) *Engine {
	e := &Engine{games: make(map[string]*Game)}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Game returns the registration for a game type, creating it on first use. Call
// its OnEvent/OnJoin/etc. to define the rules.
func (e *Engine) Game(gameType string) *Game {
	if g, ok := e.games[gameType]; ok {
		return g
	}
	g := &Game{
		gameType:  gameType,
		reducers:  make(map[string]Reducer),
		initState: func() any { return map[string]any{} },
	}
	e.games[gameType] = g
	return g
}

// OnJoin registers a callback fired when a player joins a room of this game.
func (g *Game) OnJoin(f func(Player)) *Game { g.onJoin = f; return g }

// OnLeave registers a callback fired when a player leaves.
func (g *Game) OnLeave(f func(Player)) *Game { g.onLeave = f; return g }

// OnEvent registers the reducer for an event type. The reducer folds the event
// into prior state and returns new state. Keep it deterministic for replay.
func (g *Game) OnEvent(eventType string, r Reducer) *Game { g.reducers[eventType] = r; return g }

// InitialState sets the factory for a new room's starting state.
func (g *Game) InitialState(f func() any) *Game { g.initState = f; return g }

// Derive emits server-authored domain events from a state transition (e.g.
// MatchStarted/MatchEnded) — these flow through the same persist->publish path.
func (g *Game) Derive(f Deriver) *Game { g.derive = f; return g }

// RestoreState decodes a snapshot's JSON back into the game's typed state.
// Required to enable snapshots (see SnapshotEvery).
func (g *Game) RestoreState(f func([]byte) any) *Game { g.restore = f; return g }

// SnapshotEvery enables state snapshots every n events; rehydration then folds
// only the tail of the log instead of replaying the whole match.
func (g *Game) SnapshotEvery(n int64) *Game { g.snapshotEvery = n; return g }

// Run boots the full backend and blocks until signalled.
func (e *Engine) Run() error {
	if len(e.games) == 0 {
		return errors.New("no games registered: call engine.Game(...).OnEvent(...) first")
	}

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
	if err := migrations.Apply(ctx, pool); err != nil {
		return errors.New("migrations failed: " + err.Error())
	}

	// Event bus: store publishes appended events; consumers react. PHOENIX_BUS=
	// redis makes it cluster-wide (multi-node).
	var bus eventbus.Bus = eventbus.NewInProcess()
	if os.Getenv("PHOENIX_BUS") == "redis" {
		if rb, err := eventbus.NewRedisBus(cfg.RedisURL); err != nil {
			log.Printf("phoenix: redis bus unavailable, using in-process (%v)", err)
		} else {
			bus = rb
			log.Println("phoenix: distributed event bus (redis)")
		}
	}

	es := e.store
	if es == nil {
		if os.Getenv("PHOENIX_STORE") == "batched" {
			bs := store.NewBatched(pool, bus, 512, 2*time.Millisecond)
			defer bs.Close() // flush on shutdown
			es = bs
			log.Println("phoenix: batched async event store")
		} else {
			es = store.NewFromPool(pool, bus)
		}
	}

	// Shared snapshot store, used by any game that enabled snapshots.
	snapStore := store.NewPostgresSnapshots(pool)
	if err := snapStore.EnsureSchema(ctx); err != nil {
		log.Printf("phoenix: snapshots schema: %v", err)
	}

	// Build the per-game handler map for the multi-game hub.
	handlers := make(map[string]state.Handlers, len(e.games))
	for t, g := range e.games {
		h := state.Handlers{
			Reducers:  g.reducers,
			InitState: g.initState,
			OnJoin:    g.onJoin,
			OnLeave:   g.onLeave,
			Derive:    g.derive,
		}
		if g.restore != nil && g.snapshotEvery > 0 {
			h.Snapshots = snapStore
			h.Restore = g.restore
			h.SnapshotEvery = g.snapshotEvery
		}
		handlers[t] = h
		log.Printf("phoenix: game registered: %q (%d event types)", t, len(g.reducers))
	}

	authSvc := auth.NewService(pool, cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	authH := auth.NewHandler(authSvc)
	roomSvc := room.NewService(pool)
	roomH := room.NewHandler(roomSvc)
	mmSvc := matchmaking.NewService(roomSvc)
	mmH := matchmaking.NewHandler(mmSvc)

	hub := state.NewHub(es, handlers)
	gw := gateway.New(authH, authSvc, roomSvc, hub, cfg.HeartbeatInterval, cfg.ReconnectWindow)

	// Presence (Redis) — best-effort.
	var pres core.PresenceStore
	if p, err := presence.NewRedis(ctx, cfg.RedisURL); err != nil {
		log.Printf("phoenix: presence disabled (redis: %v)", err)
	} else {
		pres = p
		bus.Subscribe("presence", p.Handle)
	}

	// Leaderboard (Postgres read model) — rebuild on boot, then follow the bus.
	lb := leaderboard.NewService(pool)
	if err := lb.EnsureSchema(ctx); err != nil {
		log.Printf("phoenix: leaderboard schema: %v", err)
	}
	if err := lb.RebuildFromLog(ctx); err != nil {
		log.Printf("phoenix: leaderboard rebuild: %v", err)
	}
	bus.Subscribe("leaderboard", lb.Handle)
	lbH := leaderboard.NewHandler(lb)

	botRunner := bots.NewRunner(hub, roomSvc, authSvc)
	adminH := admin.NewHandler(es, roomSvc, hub, pool, bus, pres, botRunner)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/ws", gw.ServeWS)
	authH.Routes(mux)
	adminH.Routes(mux)
	lbH.Routes(mux)

	openMux := http.NewServeMux()
	roomH.Routes(openMux)
	mmH.Routes(openMux)
	mux.Handle("/rooms", authH.Optional(openMux))
	mux.Handle("/rooms/", authH.Optional(openMux))
	mux.Handle("/matchmake", authH.Optional(openMux))

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: httpx.CORS(mux), ReadTimeout: 0}

	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		log.Println("phoenix: shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	log.Printf("phoenix: listening on :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
