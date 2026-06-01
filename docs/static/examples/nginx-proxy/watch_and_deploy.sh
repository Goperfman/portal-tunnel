#!/usr/bin/env bash
set -euo pipefail

default_images() {
    docker compose config --images 2>/dev/null \
        | grep -E '^ghcr\.io/gosuda/portal(:|-frontend:|-api:)' \
        | sort -u \
        | tr '\n' ' ' || true
}

IMAGES="${IMAGES:-$(default_images)}"
IMAGES="${IMAGES:-ghcr.io/gosuda/portal:2 ghcr.io/gosuda/portal-frontend:2 ghcr.io/gosuda/portal-api:2}"
DIGEST_FILE="${DIGEST_FILE:-.portal_image_digest}"
INTERVAL="${INTERVAL:-60}"
DEPLOY_SCRIPT="${DEPLOY_SCRIPT:-deploy_portal.sh}"

get_remote_digest() {
    for image in $IMAGES; do
        digest="$(docker manifest inspect "$image" 2>/dev/null \
            | grep -m1 '"digest"' \
            | awk -F'"' '{print $4}')"
        if [[ -z "$digest" ]]; then
            return 1
        fi
        printf '%s=%s\n' "$image" "$digest"
    done
}

echo "Watching $IMAGES for digest changes (interval: ${INTERVAL}s)"
echo "Deploy script: $DEPLOY_SCRIPT"

while true; do
    if ! NEW_DIGEST=$(get_remote_digest); then
        NEW_DIGEST=""
    fi

    if [[ -z "$NEW_DIGEST" ]]; then
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] Failed to fetch digest, retrying in ${INTERVAL}s"
        sleep "$INTERVAL"
        continue
    fi

    OLD_DIGEST=""
    if [[ -f "$DIGEST_FILE" ]]; then
        OLD_DIGEST=$(cat "$DIGEST_FILE")
    fi

    if [[ "$NEW_DIGEST" != "$OLD_DIGEST" ]]; then
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] Digest changed: ${OLD_DIGEST:-<none>} -> $NEW_DIGEST"
        echo "$NEW_DIGEST" > "$DIGEST_FILE"
        bash "$DEPLOY_SCRIPT"
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] Deploy completed"
    fi

    sleep "$INTERVAL"
done
