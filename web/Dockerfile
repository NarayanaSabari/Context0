# Stage 1: Build
FROM node:26-alpine AS builder

# pnpm is installed with npm rather than through corepack. Node 26 no longer
# ships corepack -- `corepack enable` fails with "not found" -- so the previous
# line broke the build outright on this base.
#
# The version is pinned rather than @latest, which is what corepack was asked
# for. A build that resolves its own package manager at build time is not
# reproducible: the same commit could build with a different pnpm next week and
# resolve the lockfile differently. This is the major matching the local
# toolchain and the lockfileVersion 9.0 in pnpm-lock.yaml.
RUN npm install -g pnpm@11.21.0
WORKDIR /app
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm build

# Stage 2: Serve with nginx
#
# nginx-unprivileged rather than nginx: the stock image starts as root to bind
# port 80 and to write its pid and temp files, which makes the pod fail the
# Pod Security "restricted" profile (runAsNonRoot, drop ALL capabilities). This
# variant runs as uid 101 throughout and listens on 8080, so no capability is
# needed to bind the port.
FROM nginxinc/nginx-unprivileged:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
# The image auto-runs envsubst on /etc/nginx/templates/*.template at startup.
ENV API_HOST=api
ENV API_PORT=8080
EXPOSE 8080
