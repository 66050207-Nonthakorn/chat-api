#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: run.sh [options]
  --scenario NAME                 baseline, api-restart, worker-restart,
                                  valkey-interruption, all, or cleanup
  --duration-seconds N            default: 60
  --connections N                 default: 100
  --messages-per-second N         default: 10
  --payload-bytes N               default: 256
  --group-percent N               default: 50
  --fault-after-seconds N         default: 12
  --fault-duration-seconds N      default: 5
  --cleanup                       remove the isolated Compose project afterward
EOF
}

scenario=baseline
duration_seconds=60
connections=100
messages_per_second=10
payload_bytes=256
group_percent=50
fault_after_seconds=12
fault_duration_seconds=5
cleanup_after=false
while (($#)); do
  case "$1" in
    --scenario) (($# >= 2)) || { usage >&2; exit 2; }; scenario="$2"; shift 2 ;;
    --duration-seconds) (($# >= 2)) || { usage >&2; exit 2; }; duration_seconds="$2"; shift 2 ;;
    --connections) (($# >= 2)) || { usage >&2; exit 2; }; connections="$2"; shift 2 ;;
    --messages-per-second) (($# >= 2)) || { usage >&2; exit 2; }; messages_per_second="$2"; shift 2 ;;
    --payload-bytes) (($# >= 2)) || { usage >&2; exit 2; }; payload_bytes="$2"; shift 2 ;;
    --group-percent) (($# >= 2)) || { usage >&2; exit 2; }; group_percent="$2"; shift 2 ;;
    --fault-after-seconds) (($# >= 2)) || { usage >&2; exit 2; }; fault_after_seconds="$2"; shift 2 ;;
    --fault-duration-seconds) (($# >= 2)) || { usage >&2; exit 2; }; fault_duration_seconds="$2"; shift 2 ;;
    --cleanup) cleanup_after=true; shift ;;
    --help|-h) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

case "$scenario" in
  baseline|api-restart|worker-restart|valkey-interruption|all|cleanup) ;;
  *) printf 'Unknown scenario: %s\n' "$scenario" >&2; exit 2 ;;
esac

if [[ "$scenario" != cleanup ]]; then
  for value in "$duration_seconds" "$connections" "$messages_per_second" "$payload_bytes" "$group_percent" "$fault_after_seconds" "$fault_duration_seconds"; do
    [[ "$value" =~ ^[0-9]+$ ]] || { printf 'Expected a non-negative integer, got: %s\n' "$value" >&2; exit 2; }
  done
fi

project=chat-ticket08-benchmark
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(git -C "$script_dir" rev-parse --show-toplevel)"
compose_file="$repository_root/infra/load/compose.benchmark.yaml"
evidence_dir="$repository_root/docs/adr/evidence"

invoke_compose() {
  docker compose --project-name "$project" --file "$compose_file" "$@"
}

wait_api_ready() {
  local deadline=$((SECONDS + 60))
  while ((SECONDS < deadline)); do
    if curl --fail --silent --show-error --max-time 2 http://127.0.0.1:18080/health/ready >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  printf '%s\n' "Isolated API did not become ready within 60 seconds." >&2
  return 1
}

load_pid=''
cleanup_load_process() {
  if [[ -n "$load_pid" ]] && kill -0 "$load_pid" 2>/dev/null; then
    kill "$load_pid" 2>/dev/null || true
    wait "$load_pid" 2>/dev/null || true
  fi
}
trap cleanup_load_process EXIT

if [[ "$scenario" == cleanup ]]; then
  invoke_compose down --volumes --remove-orphans
  printf "Removed only isolated Compose project '%s' and its named benchmark volume.\n" "$project"
  exit 0
fi

if ((duration_seconds < 10 || connections < 1 || messages_per_second < 1 || payload_bytes < 1 || payload_bytes > 4096 || group_percent > 100)); then
  printf '%s\n' "Invalid workload settings. Duration must be >=10s, connections/rate positive, payload 1..4096, group percent 0..100." >&2
  exit 2
fi
if [[ "$scenario" == api-restart || "$scenario" == worker-restart || "$scenario" == valkey-interruption || "$scenario" == all ]]; then
  if ((fault_after_seconds + fault_duration_seconds + 5 >= duration_seconds)); then
    printf '%s\n' "The fault window and a post-recovery interval must fit inside the load duration." >&2
    exit 2
  fi
fi

mkdir -p -- "$evidence_dir"
invoke_compose build api worker migrate
invoke_compose up -d --wait postgres valkey
invoke_compose run --rm --no-deps migrate
(
  cd -- "$repository_root"
  APP_ENV=local \
    ENABLE_LOCAL_TEST_IDENTITY=true \
    DATABASE_URL=postgres://chat:chat@127.0.0.1:15432/chat?sslmode=disable \
    VALKEY_URL=redis://127.0.0.1:16379/0 \
    CURSOR_HMAC_KEY=ticket-08-local-benchmark-key-32-bytes-minimum \
    go run ./cmd/seed
)
invoke_compose up -d api worker
wait_api_ready

invoke_load_run() {
  local name="$1"
  local fault_service="$2"
  local report_rel="docs/adr/evidence/ticket-08-${name}.json"
  local report_path="$repository_root/$report_rel"
  local stdout_path="$evidence_dir/ticket-08-${name}.stdout.json"
  local stderr_path="$evidence_dir/ticket-08-${name}.stderr.log"
  local fault_path="$evidence_dir/ticket-08-${name}-fault.json"
  local ready_path="$evidence_dir/ticket-08-${name}-ready.txt"
  local exit_code_path="$evidence_dir/ticket-08-${name}.exit-code"
  local -a arguments=(
    run ./cmd/loadbench
    --base-url http://127.0.0.1:18080
    --connections "$connections"
    --messages-per-second "$messages_per_second"
    --duration "${duration_seconds}s"
    --payload-bytes "$payload_bytes"
    --group-percent "$group_percent"
    --group-room-size 2
    --private-room-size 2
    --compose-file infra/load/compose.benchmark.yaml
    --compose-project "$project"
    --output "$report_rel"
  )

  rm -f -- "$report_path" "$stdout_path" "$stderr_path" "$fault_path" "$ready_path" "$exit_code_path"
  if [[ -n "$fault_service" ]]; then
    arguments+=(--fault-window "docs/adr/evidence/ticket-08-${name}-fault.json" --ready-file "docs/adr/evidence/ticket-08-${name}-ready.txt")
  fi

  (
    set +e
    cd -- "$repository_root" || exit 1
    go "${arguments[@]}"
    local_status=$?
    printf '%s\n' "$local_status" >"$exit_code_path"
    exit "$local_status"
  ) >"$stdout_path" 2>"$stderr_path" &
  load_pid=$!

  if [[ -n "$fault_service" ]]; then
    local ready_deadline=$((SECONDS + 60))
    while [[ ! -f "$ready_path" ]] && ((SECONDS < ready_deadline)); do
      if [[ -f "$exit_code_path" ]]; then
        cat -- "$stderr_path" >&2 || true
        printf "Load run '%s' exited before all WebSocket subscriptions were ready.\n" "$name" >&2
        return 1
      fi
      sleep 0.25
    done
    if [[ ! -f "$ready_path" ]]; then
      printf "Load run '%s' did not reach the requested connection count within 60 seconds.\n" "$name" >&2
      return 1
    fi

    sleep "$fault_after_seconds"
    invoke_compose stop "$fault_service"
    fault_start="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    sleep "$fault_duration_seconds"
    invoke_compose start "$fault_service"
    if [[ "$fault_service" == api || "$fault_service" == valkey ]]; then
      wait_api_ready
    else
      sleep 2
    fi
    fault_end="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '{\n  "service": "%s",\n  "started_at": "%s",\n  "ended_at": "%s"\n}\n' \
      "$fault_service" "$fault_start" "$fault_end" >"$fault_path"
  fi

  local process_status=0
  if wait "$load_pid"; then process_status=0; else process_status=$?; fi
  load_pid=''
  if ((process_status != 0)); then
    cat -- "$stderr_path" >&2 || true
    printf "Load run '%s' failed with exit code %s; see %s\n" "$name" "$process_status" "$stderr_path" >&2
    return 1
  fi
  if ! python3 -c 'import json,sys; report=json.load(open(sys.argv[1], encoding="utf-8")); sys.exit(1 if report["errors"]["sync_errors"] > 0 or report["traffic"]["not_found_by_sync"] > 0 else 0)' "$report_path"; then
    printf "Load run '%s' reported sync errors or accepted messages missing from HTTP sync.\n" "$name" >&2
    return 1
  fi
  rm -f -- "$stdout_path" "$stderr_path" "$exit_code_path"
  printf 'Wrote %s\n' "$report_rel"
}

case "$scenario" in
  baseline) invoke_load_run baseline '' ;;
  api-restart) invoke_load_run api-restart api ;;
  worker-restart) invoke_load_run worker-restart worker ;;
  valkey-interruption) invoke_load_run valkey-interruption valkey ;;
  all)
    invoke_load_run baseline ''
    invoke_load_run api-restart api
    invoke_load_run worker-restart worker
    invoke_load_run valkey-interruption valkey
    ;;
esac

if [[ "$cleanup_after" == true ]]; then
  invoke_compose down --volumes --remove-orphans
  printf "Removed only isolated Compose project '%s' and its named benchmark volume.\n" "$project"
fi
