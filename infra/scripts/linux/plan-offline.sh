#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
module_directory="$(cd -- "$script_dir/../.." && pwd)"
plan_directory="$module_directory/.plan-only"
mkdir -p -- "$plan_directory"
cp -- "$module_directory"/*.tf "$plan_directory/"
for file in .terraform.lock.hcl terraform.tfvars.example; do
  if [[ -f "$module_directory/$file" ]]; then
    cp -- "$module_directory/$file" "$plan_directory/$file"
  fi
done

versions_path="$plan_directory/versions.tf"
if ! grep -Eq '^[[:space:]]*backend[[:space:]]+"s3"[[:space:]]*\{' "$versions_path"; then
  printf '%s\n' "The isolated plan copy did not contain the expected S3 backend block." >&2
  exit 1
fi
temporary_versions="$(mktemp)"
if ! awk '
  /^[[:space:]]*backend[[:space:]]+"s3"[[:space:]]*\{/ { skipping=1; next }
  skipping && /^[[:space:]]*\}/ { skipping=0; next }
  !skipping { print }
  END { if (skipping) exit 1 }
' "$versions_path" >"$temporary_versions"; then
  rm -f -- "$temporary_versions"
  printf '%s\n' "Could not remove the S3 backend block from the isolated copy." >&2
  exit 1
fi
mv -- "$temporary_versions" "$versions_path"

docker run --rm \
  --mount "type=bind,source=$module_directory,target=/work" \
  --workdir /work \
  hashicorp/terraform:1.17.1 fmt -recursive

docker run --rm \
  --mount "type=bind,source=$plan_directory,target=/work" \
  --workdir /work \
  --env AWS_ACCESS_KEY_ID=plan-only \
  --env AWS_SECRET_ACCESS_KEY=plan-only \
  --env AWS_SHARED_CREDENTIALS_FILE=/dev/null \
  --env AWS_CONFIG_FILE=/dev/null \
  --env AWS_EC2_METADATA_DISABLED=true \
  --env TF_VAR_valkey_auth_token=plan-only-placeholder-token \
  --entrypoint /bin/sh \
  hashicorp/terraform:1.17.1 \
  -c 'terraform init -backend=false && terraform validate && terraform plan -refresh=false -input=false -var-file=terraform.tfvars.example'
