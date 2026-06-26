# Deploying Phoenix for free

Phoenix splits into a **Go backend** (needs Postgres + Redis) and a **Next.js
frontend**. All four pieces have a usable free tier:

| Piece | Free host | Why |
| --- | --- | --- |
| Frontend (Next.js) | **Vercel** | first-class Next.js, generous free tier |
| Backend (Go/Docker) | **Render** (free web service) | builds from the Dockerfile, supports WebSockets |
| Postgres | **Neon** | free serverless Postgres |
| Redis | **Upstash** | free serverless Redis (TLS `rediss://`) |

The backend **self-migrates on boot** (embedded schema), so a brand-new Neon
database needs no manual SQL.

## Steps

1. **Push to GitHub** (see "Version control" below if it isn't a repo yet).

2. **Neon → Postgres.** Create a project, copy the connection string. It looks
   like `postgres://user:pass@ep-xxx.neon.tech/neondb?sslmode=require`. That's
   your `DATABASE_URL`.

3. **Upstash → Redis.** Create a database, copy the `rediss://...` URL. That's
   your `REDIS_URL`. (go-redis understands `rediss://` and uses TLS automatically.)

4. **Render → backend.** New → Web Service → connect the repo → it detects the
   `Dockerfile`. Set environment variables:

   | Var | Value |
   | --- | --- |
   | `DATABASE_URL` | the Neon string |
   | `REDIS_URL` | the Upstash string |
   | `JWT_SECRET` | any long random string |
   | `PHOENIX_BUS` | `redis` (optional; only needed if you run >1 instance) |

   Render injects `PORT`; the app already reads it. Deploy → you get
   `https://your-backend.onrender.com`. Check `https://.../healthz` returns `ok`.

5. **Vercel → frontend.** Import the repo, set **Root Directory = `web`**, and add
   env var `NEXT_PUBLIC_API_URL = https://your-backend.onrender.com`. Deploy → you
   get `https://your-app.vercel.app`.

Open the Vercel URL: `/` (landing), `/play` (chess), `/admin` (dashboard). The
browser talks HTTPS/WSS to the Render backend; CORS and WS origins are already
open.

## Caveats (free tier)

- **Render free spins down after ~15 min idle** → first request cold-starts in
  ~30–60s and any open WebSocket drops. For a demo, hit `/healthz` once to warm it
  before showing it. (Paid Render / Fly.io / Railway avoid this.)
- **Neon / Upstash auto-suspend** when idle; the first query wakes them.
- Run a **single backend instance** on free tier. `PHOENIX_BUS=redis` only matters
  with 2+ instances (the multi-node story); a single instance can use the default
  in-process bus.

## Easiest paid-ish alternative

**Railway** puts Postgres + Redis + the Go service in one project with a few
clicks (uses the Dockerfile, auto-wires `DATABASE_URL`/`REDIS_URL`). It runs on a
~$5 trial credit rather than a perpetual free tier, but it's the least fiddly.

## Run the whole stack locally with Docker

```bash
docker compose up -d            # Postgres + Redis (ports 55432 / 63790)
go run ./cmd/chess              # backend on :8080 (or set PORT)
cd web && npm install && npm run dev   # dashboard + game client
```
