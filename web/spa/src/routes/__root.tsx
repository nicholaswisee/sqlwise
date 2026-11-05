import type { ReactNode } from "react";
import {
  createRootRouteWithContext,
  ClientOnly,
  HeadContent,
  Link,
  Outlet,
  Scripts,
} from "@tanstack/react-router";
import type { AppContext } from "../lib/query-client";
import styles from "../styles.css?url";

export const Route = createRootRouteWithContext<AppContext>()({
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1" },
      { title: "sqlwise" },
    ],
    links: [
      { rel: "stylesheet", href: styles },
      { rel: "preconnect", href: "https://fonts.googleapis.com" },
      { rel: "preconnect", href: "https://fonts.gstatic.com", crossOrigin: "anonymous" },
      {
        rel: "stylesheet",
        href: "https://fonts.googleapis.com/css2?family=Fragment+Mono&family=Instrument+Sans:wght@400;500;600;700&display=swap",
      },
    ],
  }),
  // The prerendered SPA shell has no route UI to hydrate.
  component: () => (
    <ClientOnly>
      <Outlet />
    </ClientOnly>
  ),
  shellComponent: RootDocument,
  notFoundComponent: () => (
    <main className="mx-auto max-w-5xl px-4 py-16">
      <h1 className="text-2xl font-semibold">Page not found</h1>
      <Link to="/" className="text-cyan-700 underline">
        Return home
      </Link>
    </main>
  ),
  errorComponent: () => (
    <main className="mx-auto max-w-5xl px-4 py-16">
      <h1 className="text-2xl font-semibold">Unable to open this page</h1>
      <p role="alert">Please reload and try again.</p>
    </main>
  ),
});

export function RootDocument({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <head>
        <HeadContent />
      </head>
      <body className="min-h-screen bg-slate-100 text-slate-900 antialiased">
        {children}
        <Scripts />
      </body>
    </html>
  );
}
