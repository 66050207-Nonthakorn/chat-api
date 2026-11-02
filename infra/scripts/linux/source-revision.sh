get_clean_source_revision() {
  local repository_root="$1"
  local dirty_message="$2"
  local revision working_tree

  if ! revision="$(git -C "$repository_root" rev-parse --verify HEAD)" || [[ ! "$revision" =~ ^[0-9a-f]{40}$ ]]; then
    printf '%s\n' "Could not resolve a full Git source revision." >&2
    return 1
  fi
  if ! working_tree="$(git -C "$repository_root" status --porcelain)"; then
    printf '%s\n' "Could not inspect the Git working tree." >&2
    return 1
  fi
  if [[ -n "$working_tree" ]]; then
    printf '%s\n' "$dirty_message" >&2
    return 1
  fi
  printf '%s\n' "$revision"
}
