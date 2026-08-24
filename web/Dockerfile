# Stage 1: Build
FROM node:26-alpine AS builder
RUN corepack enable && corepack prepare pnpm@latest --activate
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
