#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(git -C "$script_dir" rev-parse --show-toplevel)"
source "$script_dir/source-revision.sh"
revision="$(get_clean_source_revision "$repository_root" "Build a clean, committed source tree so the image revision identifies its source.")"

image_id_path=''
cleanup() {
  if [[ -n "$image_id_path" ]]; then
    rm -f -- "$image_id_path"
  fi
}
trap cleanup EXIT

for service in api worker; do
  tag="cloudchat-${service}:${revision}"
  image_id_path="$(mktemp)"
  docker build \
    --build-arg "VCS_REF=$revision" \
    --target "$service" \
    --iidfile "$image_id_path" \
    --tag "$tag" \
    "$repository_root"

  image_id="$(<"$image_id_path")"
  rm -f -- "$image_id_path"
  image_id_path=''
  image_revision="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$tag")"
  if [[ "$image_revision" != "$revision" || ! "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    printf 'Built %s image did not retain the requested source revision and content ID.\n' "$service" >&2
    exit 1
  fi
  printf '%s local tag: %s\n' "$service" "$tag"
  printf '%s local image ID: %s\n' "$service" "$image_id"
done
