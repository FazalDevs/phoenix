import "./globals.css";
import type { Metadata } from "next";
import Link from "next/link";

export const metadata: Metadata = {
  title: "Phoenix — multiplayer backend platform",
  description: "Write game rules, ship a backend. Event-sourced, realtime, replayable.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <nav className="topnav">
          <div className="topnav-inner">
            <Link href="/" className="brand">
              <span>▲</span> Phoenix
            </Link>
            <div className="topnav-links">
              <Link href="/play">Play</Link>
              <Link href="/arena">Arena</Link>
              <Link href="/admin">Admin</Link>
            </div>
          </div>
        </nav>
        <main>{children}</main>
      </body>
    </html>
  );
}
