"use client";

import React, { useEffect, useRef } from "react";
import { ArenaState } from "@/lib/api";

type RenderPlayer = { x: number; y: number };

type Props = {
  // Latest authoritative state. Mutated by the parent; the canvas reads it each frame.
  stateRef: React.MutableRefObject<ArenaState | null>;
  // The viewer's own player id (to highlight). Null in read-only spectate mode.
  selfId?: string | null;
  // Optional override of the rendered position for the local player (the
  // "intended" position) so the controlling player feels zero input lag.
  selfIntentRef?: React.MutableRefObject<{ x: number; y: number } | null>;
  // Max pixel width the canvas should occupy; it keeps the field aspect ratio.
  maxWidth?: number;
};

const FOOD_R = 9;
const PLAYER_R = 16;
const LERP = 0.2;

// Per-player interpolated render positions, keyed by player id. Persisted in a
// ref so they survive re-renders and we can smoothly chase the server target.
export default function ArenaCanvas({
  stateRef,
  selfId = null,
  selfIntentRef,
  maxWidth = 1000,
}: Props) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const renderPos = useRef<Map<string, RenderPlayer>>(new Map());
  const rafRef = useRef<number | null>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    let mounted = true;

    function frame() {
      if (!mounted) return;
      const st = stateRef.current;
      const cv = canvasRef.current;
      const c = cv?.getContext("2d");
      if (cv && c && st) {
        draw(c, cv, st);
      }
      rafRef.current = requestAnimationFrame(frame);
    }

    function draw(c: CanvasRenderingContext2D, cv: HTMLCanvasElement, st: ArenaState) {
      const fieldW = st.w || 1000;
      const fieldH = st.h || 640;

      // Responsive sizing: fit within maxWidth, keep aspect ratio. Use DPR for crispness.
      const cssW = Math.min(maxWidth, fieldW);
      const cssH = (cssW / fieldW) * fieldH;
      const dpr = typeof window !== "undefined" ? window.devicePixelRatio || 1 : 1;
      const pxW = Math.round(cssW * dpr);
      const pxH = Math.round(cssH * dpr);
      if (cv.width !== pxW || cv.height !== pxH) {
        cv.width = pxW;
        cv.height = pxH;
      }
      cv.style.width = `${cssW}px`;
      cv.style.height = `${cssH}px`;

      const scale = (cssW / fieldW) * dpr; // field unit -> device px
      const sx = (x: number) => x * scale;
      const sy = (y: number) => y * scale;

      // background
      c.clearRect(0, 0, cv.width, cv.height);
      c.fillStyle = "#0d111a";
      c.fillRect(0, 0, cv.width, cv.height);

      // subtle grid
      c.strokeStyle = "rgba(35,42,59,0.9)";
      c.lineWidth = 1;
      const grid = 80;
      for (let gx = 0; gx <= fieldW; gx += grid) {
        c.beginPath();
        c.moveTo(Math.round(sx(gx)) + 0.5, 0);
        c.lineTo(Math.round(sx(gx)) + 0.5, cv.height);
        c.stroke();
      }
      for (let gy = 0; gy <= fieldH; gy += grid) {
        c.beginPath();
        c.moveTo(0, Math.round(sy(gy)) + 0.5);
        c.lineTo(cv.width, Math.round(sy(gy)) + 0.5);
        c.stroke();
      }

      // border
      c.strokeStyle = "#232a3b";
      c.lineWidth = 2 * dpr;
      c.strokeRect(0, 0, cv.width, cv.height);

      // food — glowing dots
      c.save();
      for (const f of st.food) {
        const r = FOOD_R * scale;
        const cx = sx(f.x);
        const cy = sy(f.y);
        const glow = c.createRadialGradient(cx, cy, 0, cx, cy, r * 2.2);
        glow.addColorStop(0, "rgba(61,220,132,0.55)");
        glow.addColorStop(1, "rgba(61,220,132,0)");
        c.fillStyle = glow;
        c.beginPath();
        c.arc(cx, cy, r * 2.2, 0, Math.PI * 2);
        c.fill();
        c.fillStyle = "#3ddc84";
        c.beginPath();
        c.arc(cx, cy, r, 0, Math.PI * 2);
        c.fill();
      }
      c.restore();

      // players — interpolate render position toward server target
      const ids = Object.keys(st.players);
      const seen = new Set(ids);
      // prune stale render positions
      for (const id of Array.from(renderPos.current.keys())) {
        if (!seen.has(id)) renderPos.current.delete(id);
      }

      for (const id of ids) {
        const p = st.players[id];
        // target position: for self, prefer the local "intent" if provided
        let tx = p.x;
        let ty = p.y;
        if (id === selfId && selfIntentRef?.current) {
          tx = selfIntentRef.current.x;
          ty = selfIntentRef.current.y;
        }
        let rp = renderPos.current.get(id);
        if (!rp) {
          rp = { x: tx, y: ty };
          renderPos.current.set(id, rp);
        } else {
          rp.x += (tx - rp.x) * LERP;
          rp.y += (ty - rp.y) * LERP;
        }

        const cx = sx(rp.x);
        const cy = sy(rp.y);
        const r = PLAYER_R * scale;

        // body
        c.fillStyle = p.color || "#ff6b3d";
        c.beginPath();
        c.arc(cx, cy, r, 0, Math.PI * 2);
        c.fill();

        // highlight self with a white ring
        if (id === selfId) {
          c.strokeStyle = "#ffffff";
          c.lineWidth = 3 * dpr;
          c.beginPath();
          c.arc(cx, cy, r + 2 * dpr, 0, Math.PI * 2);
          c.stroke();
        }

        // name + score above
        const label = `${p.name || "anon"} · ${p.score ?? 0}`;
        c.font = `${Math.max(10, 12 * dpr)}px ui-monospace, Menlo, Consolas, monospace`;
        c.textAlign = "center";
        c.textBaseline = "bottom";
        c.fillStyle = "rgba(11,14,20,0.65)";
        const tw = c.measureText(label).width;
        c.fillRect(cx - tw / 2 - 4 * dpr, cy - r - 18 * dpr, tw + 8 * dpr, 16 * dpr);
        c.fillStyle = "#e6e9ef";
        c.fillText(label, cx, cy - r - 4 * dpr);
      }
    }

    rafRef.current = requestAnimationFrame(frame);
    return () => {
      mounted = false;
      if (rafRef.current) cancelAnimationFrame(rafRef.current);
    };
  }, [stateRef, selfId, selfIntentRef, maxWidth]);

  return (
    <canvas
      ref={canvasRef}
      style={{ display: "block", borderRadius: 10, maxWidth: "100%", touchAction: "none" }}
    />
  );
}
