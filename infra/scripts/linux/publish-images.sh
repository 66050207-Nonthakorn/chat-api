#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: publish-images.sh --region REGION --api-repository ECR_URL --worker-repository ECR_URL
EOF
  exit 2
}

region=''
api_repository=''
worker_repository=''
while (($#)); do
  case "$1" in
    --region) (($# >= 2)) || usage; region="$2"; shift 2 ;;
    --api-repository) (($# >= 2)) || usage; api_repository="$2"; shift 2 ;;
    --worker-repository) (($# >= 2)) || usage; worker_repository="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "$region" && -n "$api_repository" && -n "$worker_repository" ]] || usage

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(git -C "$script_dir" rev-parse --show-toplevel)"
source "$script_dir/source-revision.sh"
revision="$(get_clean_source_revision "$repository_root" "Publish only a clean commit whose CI checks have passed.")"

api_registry="${api_repository%%/*}"
worker_registry="${worker_repository%%/*}"
if [[ "$api_registry" == "$api_repository" || "$api_registry" != "$worker_registry" || "$api_registry" != *".dkr.ecr."* ]]; then
  printf '%s\n' "API and worker repositories must use the same Amazon ECR registry." >&2
  exit 1
fi

aws ecr get-login-password --region "$region" |
  docker login --username AWS --password-stdin "$api_registry" >/dev/null

get_existing_digest() {
  local repository_url="$1"
  local repository_name="${repository_url#*/}"
  local digest
  if ! digest="$(aws ecr describe-images \
    --region "$region" \
    --repository-name "$repository_name" \
    --image-ids "imageTag=$revision" \
    --query 'imageDetails[0].imageDigest' \
    --output text 2>/dev/null)"; then
    return 0
  fi
  if [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    printf '%s\n' "$digest"
  fi
}

publish_image() {
  local service="$1"
  local repository_url="$2"
  local existing metadata_path digest

  existing="$(get_existing_digest "$repository_url")"
  if [[ -n "$existing" ]]; then
    printf '%s@%s\n' "$repository_url" "$existing"
    return
  fi

  metadata_path="$(mktemp)"
  if ! docker buildx build \
    --platform linux/amd64 \
    --push \
    --build-arg "VCS_REF=$revision" \
    --target "$service" \
    --tag "${repository_url}:${revision}" \
    --metadata-file "$metadata_path" \
    "$repository_root"; then
    rm -f -- "$metadata_path"
    printf 'Image publish failed for %s.\n' "$service" >&2
    return 1
  fi

  digest="$(grep -oE '"containerimage\.digest"[[:space:]]*:[[:space:]]*"sha256:[0-9a-f]{64}"' "$metadata_path" | sed -E 's/.*"(sha256:[0-9a-f]{64})"/\1/' || true)"
  rm -f -- "$metadata_path"
  if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    printf 'Buildx did not return a registry manifest digest for %s.\n' "$service" >&2
    return 1
  fi
  printf '%s@%s\n' "$repository_url" "$digest"
}

publish_image api "$api_repository"
publish_image worker "$worker_repository"
