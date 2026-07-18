# syntax=docker/dockerfile:1
# No-build static SPA — the API base URL is configured in-app (localStorage), not at build time.
FROM nginx:stable-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html app.js styles.css /usr/share/nginx/html/
EXPOSE 80
