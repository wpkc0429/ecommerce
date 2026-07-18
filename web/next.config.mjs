/** @type {import('next').NextConfig} */
const nextConfig = {
  // The storefront is fully dynamic: every page depends on the Host header
  // and the render bundle API (Redis is the caching layer, not Next).
  reactStrictMode: true,
  // Self-contained server bundle for the production Docker image.
  output: "standalone",
};

export default nextConfig;
