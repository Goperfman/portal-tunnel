#!/bin/bash
set -e

docker compose pull portal portal-frontend portal-api
docker compose up -d portal portal-frontend portal-api
bash nginx_deploy.sh
