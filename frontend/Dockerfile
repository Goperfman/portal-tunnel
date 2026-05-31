# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM node:22-slim AS deps
WORKDIR /src

COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci

FROM deps AS frontend-builder
COPY . .
RUN npm run build

FROM deps AS api-builder
COPY . .
RUN npm run build:api

FROM node:22-alpine AS api
WORKDIR /app

COPY --from=api-builder /src/dist-api ./dist-api

ENV PORT=8081
EXPOSE 8081

CMD ["node", "dist-api/api/server.js"]

FROM nginx:1.27-alpine AS frontend

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=frontend-builder /src/dist /usr/share/nginx/html

EXPOSE 8080
