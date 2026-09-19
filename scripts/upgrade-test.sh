#!/usr/bin/env bash
# Upgrade test: run the previous release with sample data, then the candidate image on the
# same /config and /projects, and check that nothing is lost on the way.
#
#   scripts/upgrade-test.sh ghcr.io/envoryx/envoryx:0.1.0 envoryx:candidate
#
# Needs Docker, curl and jq on the host. The script creates a throw-away work directory,
# an Envoryx container "envoryx-upgrade-test" on 127.0.0.1:18787 (UPGRADE_PORT) with the
# project port range 21000-21099 and no proxy/SSH listeners, so it can run next to a
# real Envoryx. Everything it creates is removed at the end, also on failure.
set -euo pipefail

OLD=${1:?usage: upgrade-test.sh <old image> <new image>}
NEW=${2:?usage: upgrade-test.sh <old image> <new image>}
PORT=${UPGRADE_PORT:-18787}
NAME=envoryx-upgrade-test
SLUG=upgrade-test
WORK=$(mktemp -d "${TMPDIR:-/tmp}/envoryx-upgrade.XXXXXX")
BASE="http://127.0.0.1:$PORT/api/v1"
PASSWORD="upgrade-test-pass-1"
TOKEN=""

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
	local rc=$?
	set +e
	log "cleanup"
	if [ -n "$TOKEN" ] && curl -sf -m 5 "$BASE/health" >/dev/null 2>&1; then
		id=$(api GET /projects | jq -r --arg s "$SLUG" '(.projects // .)[] | select(.slug == $s) | .id' 2>/dev/null | head -1)
		[ -n "$id" ] && api DELETE "/projects/$id" "{\"confirm\":\"$SLUG\",\"deleteFiles\":true}" >/dev/null 2>&1
	fi
	docker rm -f "$NAME" >/dev/null 2>&1
	# Whatever the test's project left behind (label-scoped, never anything else).
	docker ps -aq --filter "label=envoryx.project.name=$SLUG" | xargs -r docker rm -f >/dev/null 2>&1
	docker network ls -q --filter "label=envoryx.project.name=$SLUG" | xargs -r docker network rm >/dev/null 2>&1
	docker volume ls -q --filter "label=envoryx.project.name=$SLUG" | xargs -r docker volume rm >/dev/null 2>&1
	rm -rf "$WORK"
	if [ $rc -eq 0 ]; then log "upgrade test passed"; else log "upgrade test FAILED (rc=$rc); container logs above"; fi
	exit $rc
}
trap cleanup EXIT

# api METHOD PATH [JSON] – bearer-authenticated call, prints the body, fails on HTTP >= 400.
api() {
	local method=$1 path=$2 body=${3:-}
	local out code
	out=$(curl -s -m 120 -X "$method" -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
		${body:+--data "$body"} -w '\n%{http_code}' "$BASE$path")
	code=${out##*$'\n'}
	out=${out%$'\n'*}
	if [ "$code" -ge 400 ]; then
		printf '%s %s -> %s: %s\n' "$method" "$path" "$code" "$out" >&2
		return 1
	fi
	printf '%s' "$out"
}

run_envoryx() {
	local image=$1
	log "starting $image"
	docker run -d --name "$NAME" --stop-timeout 30 \
		-p "127.0.0.1:$PORT:8787" \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v "$WORK/config:/config" -v "$WORK/projects:/projects" \
		-e ENVORYX_CONFIG_HOST_PATH="$WORK/config" -e ENVORYX_PROJECTS_HOST_PATH="$WORK/projects" \
		-e ENVORYX_ADMIN_USER=admin -e ENVORYX_ADMIN_PASSWORD="$PASSWORD" \
		-e ENVORYX_PORT_RANGE_START=21000 -e ENVORYX_PORT_RANGE_END=21099 \
		-e ENVORYX_PROXY_HTTP= -e ENVORYX_PROXY_HTTPS= -e ENVORYX_SSH= \
		-e ENVORYX_UPDATE_CHECK=false -e ENVORYX_LOG_FORMAT=text \
		-e PUID="$(id -u)" -e PGID="$(id -g)" \
		"$image" >/dev/null
}

wait_health() {
	local i
	for i in $(seq 1 60); do
		if curl -sf -m 3 "$BASE/health" >/dev/null 2>&1; then
			return 0
		fi
		if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" != "true" ]; then
			docker logs "$NAME" 2>&1 | tail -20
			fail "container exited while waiting for health"
		fi
		sleep 1
	done
	docker logs "$NAME" 2>&1 | tail -20
	fail "no healthy answer within 60 s"
}

stop_envoryx() {
	log "stopping Envoryx"
	docker stop "$NAME" >/dev/null
	docker logs "$NAME" 2>&1 | grep -iE 'error|panic|warn' | grep -v 'docker engine not reachable' | tail -20 || true
	docker rm "$NAME" >/dev/null
}

mkdir -p "$WORK/config" "$WORK/projects"

# ---- 1. previous release with sample data -------------------------------------------
run_envoryx "$OLD"
wait_health
cookies="$WORK/cookies"
curl -sf -m 10 -c "$cookies" -H "X-Requested-With: Envoryx" -H "Content-Type: application/json" \
	--data "{\"username\":\"admin\",\"password\":\"$PASSWORD\"}" "$BASE/auth/login" >/dev/null || fail "login on $OLD"
TOKEN=$(curl -sf -m 10 -b "$cookies" -H "X-Requested-With: Envoryx" -H "Content-Type: application/json" \
	--data '{"name":"upgrade-test"}' "$BASE/tokens" | jq -r .secret)
[ -n "$TOKEN" ] && [ "$TOKEN" != null ] || fail "no API token from $OLD"

old_settings=$(api GET /settings)
old_schema=$(jq -r .schemaVersion <<<"$old_settings")
old_version=$(jq -r .version <<<"$old_settings")
log "old: version=$old_version schema=$old_schema"

log "creating sample project (pulls PHP and Caddy images)"
created=$(api POST /projects '{"name":"Upgrade Test","docroot":"public","php":{"version":"8.4","config":{}},"createStarter":true,"start":true}')
project_id=$(jq -r .project.id <<<"$created")
[ "$(jq -r .project.status.state <<<"$created")" = running ] || fail "project not running after create: $(jq -c .project.status <<<"$created")"
api PATCH /settings '{"xdebugClientHost":"10.7.7.7"}' >/dev/null
echo "sample marker" >"$WORK/projects/$SLUG/public/upgrade-marker.txt"

stop_envoryx

# ---- 2. candidate on the same data --------------------------------------------------
run_envoryx "$NEW"
wait_health
new_settings=$(api GET /settings) || fail "the API token from $OLD is rejected by $NEW"
new_schema=$(jq -r .schemaVersion <<<"$new_settings")
new_version=$(jq -r .version <<<"$new_settings")
log "new: version=$new_version schema=$new_schema"
[ "$new_schema" -ge "$old_schema" ] || fail "schema went backwards: $old_schema -> $new_schema"

backups=$(api GET /instance/backups)
if [ "$new_schema" -gt "$old_schema" ]; then
	pre=$(jq -r '.backups[] | select(.kind == "pre-migrate") | .id' <<<"$backups" | head -1)
	[ -n "$pre" ] || fail "schema changed $old_schema -> $new_schema but no pre-migrate backup exists: $backups"
	log "pre-migrate backup present: $pre"
else
	[ "$(jq '[.backups[] | select(.kind == "pre-migrate")] | length' <<<"$backups")" = 0 ] || fail "no schema change but a pre-migrate backup was written"
	log "no schema change, no pre-migrate backup (correct)"
fi

[ "$(jq -r .xdebugClientHost <<<"$new_settings")" = "10.7.7.7" ] || fail "settings not carried over: $new_settings"

projects=$(api GET /projects)
p=$(jq -c --arg id "$project_id" '(.projects // .)[] | select(.id == $id)' <<<"$projects")
[ -n "$p" ] || fail "sample project missing after upgrade: $projects"
[ "$(jq -r .lifecycle <<<"$p")" = ready ] || fail "lifecycle after upgrade: $(jq -c . <<<"$p")"
[ "$(jq -r .status.state <<<"$p")" = running ] || fail "containers not found after upgrade: $(jq -c .status <<<"$p")"
[ "$(jq -r '.status.warnings | length' <<<"$p")" = 0 ] || fail "warnings after upgrade: $(jq -c .status.warnings <<<"$p")"
[ -f "$WORK/projects/$SLUG/public/upgrade-marker.txt" ] || fail "project files touched by the upgrade"
api GET "/audit?limit=50" | jq -e '[.entries[] | select(.action == "project.created")] | length > 0' >/dev/null || fail "audit log lost"
api POST "/projects/$project_id/restart" >/dev/null || fail "restart on $NEW"
log "project survived: lifecycle ready, containers running, files and audit intact"

# ---- 3. downgrade refusal (only when the schema moved) ------------------------------
if [ "$new_schema" -gt "$old_schema" ]; then
	stop_envoryx
	log "starting $OLD against the migrated database: it must refuse"
	run_envoryx "$OLD"
	for i in $(seq 1 30); do
		[ "$(docker inspect -f '{{.State.Running}}' "$NAME")" = "true" ] || break
		sleep 1
	done
	[ "$(docker inspect -f '{{.State.Running}}' "$NAME")" != "true" ] || fail "$OLD started on a newer schema instead of refusing"
	docker logs "$NAME" 2>&1 | grep -qi "newer" || { docker logs "$NAME" 2>&1 | tail -10; fail "refusal does not explain the newer schema"; }
	docker rm "$NAME" >/dev/null
	run_envoryx "$NEW"
	wait_health
	[ "$(api GET /projects | jq -r --arg id "$project_id" '(.projects // .)[] | select(.id == $id) | .lifecycle')" = ready ] || fail "project gone after the refused downgrade"
	log "downgrade refused cleanly, data intact"
fi
