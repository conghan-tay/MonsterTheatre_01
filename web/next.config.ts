import path from "node:path";
import type { NextConfig } from "next";

// In the browser everything is one origin. Locally this app proxies /api/* to
// the Go server; on AWS the load balancer does the same routing and
// API_ORIGIN is left unset.
const apiOrigin = process.env.API_ORIGIN;

const nextConfig: NextConfig = {
  // Every page here is a Client Component that fetches from the Go API in the
  // browser, so server-side caching and prerendering of data have nothing to
  // do. Left off to keep the pages plain.
  cacheComponents: false,
  turbopack: {
    root: path.join(__dirname),
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
  async rewrites() {
    if (!apiOrigin) return [];
    return [{ source: "/api/:path*", destination: `${apiOrigin}/api/:path*` }];
  },
};

export default nextConfig;
