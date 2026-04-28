import type { NextConfig } from "next";

/** Go API (used by dev server and Docker image at build time). Local: 127.0.0.1:8001; compose: http://rota-core:8001 */
const internalApiOrigin = (
  process.env.INTERNAL_API_URL?.trim() ||
  process.env.API_INTERNAL_URL?.trim() ||
  "http://127.0.0.1:8001"
).replace(/\/$/, "");

const nextConfig: NextConfig = {
  output: "standalone",
  async rewrites() {
    return [
      {
        source: "/api/v1/:path*",
        destination: `${internalApiOrigin}/api/v1/:path*`,
      },
    ];
  },
};

export default nextConfig;
