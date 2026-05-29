#!/bin/bash
set -e

docker compose pull portal portal-frontend
docker compose up -d portal portal-frontend
bash nginx_deploy.sh
