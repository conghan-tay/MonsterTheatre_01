# The web pages, built once and served by `next start`.
#
# API_ORIGIN is not set, at build or at run time, so the app adds no /api
# rewrite: on AWS the load balancer sends /api/* to the Go server itself.
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY next.config.ts ./
COPY --from=build /app/.next ./.next
USER node
EXPOSE 3000
# 0.0.0.0, so the load balancer can reach it from outside the container.
CMD ["node_modules/.bin/next", "start", "--hostname", "0.0.0.0", "--port", "3000"]
