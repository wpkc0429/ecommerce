# syntax=docker/dockerfile:1
# No-build static SPA — the API base URL is configured in-app (localStorage), not at build time.
#
# `COPY .` rather than an explicit file list (change admin-commerce-catalog):
# the app is now several native ES modules under lib/ and views/, and a
# per-file COPY list carries a failure mode where one forgotten line ships an
# image that 404s a module while `python3 -m http.server` locally is perfectly
# fine — a production-only white screen. Adding a module therefore needs no
# change here at all.
#
# .dockerignore keeps Dockerfile/package.json out of the context; nginx.conf
# has to stay in it (it is COPYed to the nginx config dir), so it is removed
# from the web root afterwards rather than served as a static asset.
FROM nginx:stable-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY . /usr/share/nginx/html/
RUN rm -f /usr/share/nginx/html/nginx.conf
EXPOSE 80
