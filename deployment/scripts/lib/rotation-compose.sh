#!/usr/bin/env bash
# Shared compose selection for the secret-rotation scripts. Sourced, not run.
#
# The caller sets script_dir and, before calling anything here, its own
# compose_file / project / env_file variables from its options (empty when not
# given). rotation_compose_defaults fills the rest:
#
#   - no compose file anywhere -> the bundled production compose;
#   - the project name "bansoko" only for that production compose.
#
# A compose file the caller names is left to compose's own derivation from that
# file's directory. Naming "bansoko" there would act on the deployed stack
# instead of the one the file describes.

rotation_compose_defaults() {
  local production_compose="$script_dir/../maintainer/docker-compose.production.yml"
  if [[ -z "${compose_file:-}" && -z "${COMPOSE_FILE:-}" ]]; then
    compose_file="$production_compose"
  fi
  if [[ -z "${project:-}" && "${compose_file:-}" == "$production_compose" ]]; then
    project=bansoko
  fi
}

# dc runs docker compose with the selected project, file and env file.
dc() {
  local args=()
  [[ -n "${project:-}" ]] && args+=(-p "$project")
  [[ -n "${compose_file:-}" ]] && args+=(-f "$compose_file")
  [[ -n "${env_file:-}" ]] && args+=(--env-file "$env_file")
  docker compose "${args[@]}" "$@"
}

# rotation_stop_app stops a running app service. With stop_app=0 it refuses
# instead, so a rotation never takes down a serving instance by surprise.
rotation_stop_app() {
  local log_prefix="$1" stop_app="$2" running
  running="$(dc ps --status running --status restarting --services 2>/dev/null | grep -Ex 'app' || true)"
  [[ -n "$running" ]] || return 0
  if [[ "$stop_app" -eq 0 ]]; then
    printf '%s: the app is running; stop it first or pass --stop-app\n' "$log_prefix" >&2
    return 3
  fi
  printf '%s: stopping %s\n' "$log_prefix" "$(echo "$running" | tr '\n' ' ')" >&2
  # shellcheck disable=SC2086 # service names are fixed words
  dc stop $running
}

# rotation_start_hint prints the command that starts the app again.
rotation_start_hint() {
  local log_prefix="$1"
  printf '%s: start it: docker compose %s%s%s up -d app\n' "$log_prefix" \
    "${project:+-p $project }" "${compose_file:+-f $compose_file }" "${env_file:+--env-file $env_file }" >&2
}
