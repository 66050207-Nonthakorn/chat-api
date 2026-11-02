#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: run-migration.sh --region REGION --cluster CLUSTER --task-definition-arn ARN \\
  --private-subnet SUBNET [--private-subnet SUBNET ...] \\
  --task-security-group GROUP [--task-security-group GROUP ...]
EOF
  exit 2
}

region=''
cluster=''
task_definition_arn=''
private_subnets=()
task_security_groups=()
while (($#)); do
  case "$1" in
    --region) (($# >= 2)) || usage; region="$2"; shift 2 ;;
    --cluster) (($# >= 2)) || usage; cluster="$2"; shift 2 ;;
    --task-definition-arn) (($# >= 2)) || usage; task_definition_arn="$2"; shift 2 ;;
    --private-subnet) (($# >= 2)) || usage; private_subnets+=("$2"); shift 2 ;;
    --task-security-group) (($# >= 2)) || usage; task_security_groups+=("$2"); shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$region" && -n "$cluster" && -n "$task_definition_arn" ]] || usage
if ((${#private_subnets[@]} < 2 || ${#task_security_groups[@]} < 1)); then
  printf '%s\n' "Migration tasks require at least two private subnets and one backend task security group." >&2
  exit 1
fi
if [[ ! "$task_definition_arn" =~ :task-definition/.+:[0-9]+$ ]]; then
  printf '%s\n' "Task definition ARN must identify a concrete, registered migration task revision." >&2
  exit 1
fi
for subnet in "${private_subnets[@]}"; do
  [[ "$subnet" =~ ^subnet-[[:alnum:]]+$ ]] || { printf 'Invalid private subnet ID: %s\n' "$subnet" >&2; exit 1; }
done
for security_group in "${task_security_groups[@]}"; do
  [[ "$security_group" =~ ^sg-[[:alnum:]]+$ ]] || { printf 'Invalid task security group ID: %s\n' "$security_group" >&2; exit 1; }
done

json_array() {
  local result=''
  local item
  for item in "$@"; do
    result+="${result:+,}\"${item}\""
  done
  printf '[%s]' "$result"
}

subnet_json="$(json_array "${private_subnets[@]}")"
security_group_json="$(json_array "${task_security_groups[@]}")"
network_configuration="{\"awsvpcConfiguration\":{\"subnets\":${subnet_json},\"securityGroups\":${security_group_json},\"assignPublicIp\":\"DISABLED\"}}"

run_result="$(aws ecs run-task \
  --region "$region" \
  --cluster "$cluster" \
  --task-definition "$task_definition_arn" \
  --launch-type FARGATE \
  --network-configuration "$network_configuration" \
  --query '[length(tasks), length(failures), tasks[0].taskArn]' \
  --output text)"
read -r task_count failure_count task_arn <<<"$run_result"
if [[ "$task_count" != 1 || "$failure_count" != 0 || "$task_arn" != arn:*:task/* ]]; then
  printf '%s\n' "ECS did not start exactly one migration task." >&2
  exit 1
fi

aws ecs wait tasks-stopped --region "$region" --cluster "$cluster" --tasks "$task_arn"
task_count="$(aws ecs describe-tasks --region "$region" --cluster "$cluster" --tasks "$task_arn" --query 'length(tasks)' --output text)"
if [[ "$task_count" != 1 ]]; then
  printf '%s\n' "Could not verify the stopped migration task." >&2
  exit 1
fi
exit_code="$(aws ecs describe-tasks \
  --region "$region" \
  --cluster "$cluster" \
  --tasks "$task_arn" \
  --query "tasks[0].containers[?name=='migrate'].exitCode | [0]" \
  --output text)"
if [[ "$exit_code" != 0 ]]; then
  printf '%s\n' "Migration task failed. Inspect its CloudWatch migration log stream before proceeding." >&2
  exit 1
fi

printf 'Migration task completed successfully: %s\n' "$task_arn"
