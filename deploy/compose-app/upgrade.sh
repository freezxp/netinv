#!/usr/bin/env bash
# Upgrade a running Compose deployment of NetInv in place (doc 32, Upgrading).
#
# The flags a NetInv stack was created with are not optional and not guessable:
# it is a two-file project with an --env-file, and a bare `docker compose up -d
# api` from the repo root resolves to the base file alone. That file carries no
# NETINV_PG_DSN, so the api is silently recreated in skeleton mode with no
# database, and postgres/rabbitmq come back with base-file credentials — at
# which point every other service fails AMQP auth. Nothing is lost, but the
# stack is down until someone works out why.
#
# So this script does not accept compose flags and does not assume any: it
# reads them back off a running container, where Compose records the project
# name, the config files and the env file as labels. Upgrading the wrong stack,
# or half of one, is therefore not a thing it can do.
#
# Data lives in named volumes and survives. Migrations are embedded in the api
# binary and run at startup under an advisory lock, so there is no migration
# step here — but that is also why a new backend/migrations/*.sql does nothing
# until the image is rebuilt, which is what step 5 verifies.
#
# Usage:
#   ./deploy/compose-app/upgrade.sh                     # rebuild from the working tree
#   ./deploy/compose-app/upgrade.sh --ref v1.1.0        # fetch + check out first
#   ./deploy/compose-app/upgrade.sh --skip-backup       # you already have one
#   ./deploy/compose-app/upgrade.sh --keep 5            # keep 5 backups (0 = keep all)
#   ./deploy/compose-app/upgrade.sh --dry-run           # print the plan, change nothing
#   ./deploy/compose-app/upgrade.sh --recover           # stack is down: bring it back up
#   ./deploy/compose-app/upgrade.sh --no-rollback       # leave the wreckage for inspection
#   PROJECT=netinv-staging ./deploy/compose-app/upgrade.sh
#
# On failure the script rolls back by itself: it restores the checkout it started
# from and brings the stack back up on the images that were already there. What
# it will not do is touch the database — goose does not roll a migration back, so
# code that has already applied one is rolled back *under* a newer schema. That is
# safe (the schema is a superset) but it is not a restore, and the difference
# matters enough to be told rather than inferred.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

REF=""
KEEP="${KEEP:-3}"
SKIP_BACKUP=0
DRY_RUN=0
RECOVER=0
NO_ROLLBACK=0
BACKUP_DIR="${BACKUP_DIR:-$ROOT/backups}"

while [ $# -gt 0 ]; do
	case "$1" in
	--ref) REF="${2:?--ref needs a git ref}"; shift 2 ;;
	--skip-backup) SKIP_BACKUP=1; shift ;;
	--keep) KEEP="${2:?--keep needs a count}"; shift 2 ;;
	--dry-run) DRY_RUN=1; shift ;;
	--recover) RECOVER=1; shift ;;
	--no-rollback) NO_ROLLBACK=1; shift ;;
	-h | --help) sed -n '2,36p' "$0" | sed 's/^# \?//'; exit 0 ;;
	*) echo "unknown option: $1 (try --help)" >&2; exit 1 ;;
	esac
done

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
run() {
	if [ "$DRY_RUN" = 1 ]; then
		printf '  would run: %s\n' "$*"
	else
		"$@"
	fi
}

command -v docker >/dev/null || { echo "Docker is required." >&2; exit 1; }
docker compose version >/dev/null 2>&1 ||
	{ echo "Docker Compose v2 is required." >&2; exit 1; }

# ---------------------------------------------------------- 1. find the stack
say "Locating the running stack"
label() { docker inspect -f "{{index .Config.Labels \"$2\"}}" "$1" 2>/dev/null || true; }

# Any container of the project will do — they all carry the same project
# labels. Preferring the api is only so the message names something recognisable.
ref_container=""
# -a, not just running: recovery exists for the case where nothing is up, and
# the labels this script needs survive on a stopped container.
running_count=0
for candidate in $(docker ps -a --format '{{.Names}}' --filter "label=com.docker.compose.project${PROJECT:+=$PROJECT}"); do
	if docker inspect -f '{{.State.Running}}' "$candidate" 2>/dev/null | grep -q true; then
		running_count=$((running_count + 1))
	fi
	case "$candidate" in
	*api*) ref_container="$candidate"; break ;;
	*) [ -n "$ref_container" ] || ref_container="$candidate" ;;
	esac
done
if [ -z "$ref_container" ]; then
	cat >&2 <<-EOF
	No Compose containers found${PROJECT:+ for project "$PROJECT"}, running or stopped.

	This script upgrades a stack that is already running, because that is where
	the flags it was created with are recorded. For a first install use
	quickstart.sh. If the stack is stopped, start it the way it was created and
	re-run this; if you no longer know how, the labels survive on the stopped
	containers:

	  docker inspect -f '{{index .Config.Labels "com.docker.compose.project.config_files"}}' <container>
	EOF
	exit 1
fi

PROJECT="$(label "$ref_container" com.docker.compose.project)"
CONFIG_FILES="$(label "$ref_container" com.docker.compose.project.config_files)"
ENV_FILE="$(label "$ref_container" com.docker.compose.project.environment_file)"
WORK_DIR="$(label "$ref_container" com.docker.compose.project.working_dir)"
[ -n "$CONFIG_FILES" ] || { echo "container $ref_container has no compose config-files label" >&2; exit 1; }

COMPOSE=(docker compose -p "$PROJECT")
if [ -n "$ENV_FILE" ]; then
	COMPOSE+=(--env-file "$ENV_FILE")
fi
# The label is a comma-separated list, in the order the files were given.
while IFS= read -r f; do
	[ -n "$f" ] || continue
	[ -f "$f" ] || { echo "compose file from the running stack is missing: $f" >&2; exit 1; }
	COMPOSE+=(-f "$f")
done <<<"${CONFIG_FILES//,/$'\n'}"

echo "  project:      $PROJECT"
echo "  config files: ${CONFIG_FILES//,/, }"
echo "  env file:     ${ENV_FILE:-<none>}"
echo "  working dir:  ${WORK_DIR:-<unset>}"
echo "  derived from: $ref_container"

# A stack whose working_dir is not this checkout would be rebuilt from the
# wrong source. Refuse rather than quietly ship someone else's tree.
if [ -n "$WORK_DIR" ] && [ "$WORK_DIR" != "$ROOT" ]; then
	echo >&2
	echo "This stack was created from $WORK_DIR but this script lives in $ROOT." >&2
	echo "Run the upgrade from the checkout the stack was built from." >&2
	exit 1
fi

# The credential vault is sealed with NETINV_MASTER_KEY. A stack that comes
# back with a different one keeps its inventory and history and can no longer
# decrypt a single SNMP credential, which presents as every device failing to
# poll after a "successful" upgrade.
if [ -n "$ENV_FILE" ] && ! grep -q '^NETINV_MASTER_KEY=' "$ENV_FILE"; then
	echo >&2
	echo "WARNING: $ENV_FILE has no NETINV_MASTER_KEY." >&2
	echo "If the services get a fresh key, stored credentials become undecryptable." >&2
fi

SKIP_BUILD=0
if [ "$RECOVER" = 1 ]; then
	# Recovery is deliberately the smallest possible action: start what is
	# already built. It takes no backup (the stack is down; there is nothing to
	# capture that the last backup does not already have) and builds nothing,
	# because a failed build is the most likely reason someone is here and
	# repeating it would just fail again.
	say "Recovery mode"
	echo "  no backup, no build, no checkout — starting the images already on this host"
	SKIP_BUILD=1
	SKIP_BACKUP=1
	if [ -n "$REF" ]; then
		echo "  ignoring --ref $REF: recovery restores service, it does not change version"
		REF=""
	fi
fi

# --------------------------------------------------------------- 2. versions
say "Version"
before_version="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo unknown)"
before_commit="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
moved_checkout=0
echo "  currently checked out: $before_version ($before_commit)"

if [ -n "$REF" ]; then
	if [ -n "$(git -C "$ROOT" status --porcelain 2>/dev/null)" ]; then
		echo "Working tree has uncommitted changes; commit or stash before --ref." >&2
		exit 1
	fi
	say "Fetching and checking out $REF"
	run git -C "$ROOT" fetch --tags --prune origin
	run git -C "$ROOT" checkout "$REF"
	[ "$DRY_RUN" = 1 ] || moved_checkout=1
	echo "  now at: $(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo unknown)"
fi

# ------------------------------------------------------------- 3. preflight
# Both of the things this script needs disk for are measurable before it does
# any work, and neither reports itself usefully when it runs out. A Go build
# that fills the disk fails inside BuildKit as `exit code 1`, with the real
# message — "no space left on device" — buried in one of eight parallel build
# streams. That cost a real deployment several rounds of diagnosis, so it is
# now one line, up front, naming the filesystem.
#
# The Docker root is asked for rather than assumed: /var/lib/docker can be a
# mount with plenty of room while the daemon stores elsewhere, or the reverse.
# The host that hit this had 127 GB free on /var/lib/docker and a full / — and
# `df /var/lib/docker` looked reassuring while being the wrong question.
MIN_BUILD_MB="${MIN_BUILD_MB:-5120}"
MIN_BACKUP_MB="${MIN_BACKUP_MB:-1024}"

# free_mb / inode_pct take a path that may not exist yet and walk up to the
# nearest ancestor that does, since df cannot stat a directory we are about to
# create.
existing_parent() {
	d="$1"
	while [ ! -d "$d" ] && [ "$d" != "/" ]; do d="$(dirname "$d")"; done
	printf '%s' "$d"
}
free_mb() { df -Pk "$(existing_parent "$1")" | awk 'NR==2 {print int($4/1024)}'; }
inode_pct() { df -Pi "$(existing_parent "$1")" | awk 'NR==2 {gsub(/%/,"",$5); print $5+0}'; }
fs_of() { df -Pk "$(existing_parent "$1")" | awk 'NR==2 {print $1" on "$6}'; }

say "Preflight"
docker_root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
docker_root="${docker_root:-/var/lib/docker}"
preflight_failed=0

# DockerRootDir is not where the bytes are when the containerd snapshotter is
# in use — image layers and build cache live under containerd's root instead,
# and DockerRootDir stays nearly empty. That is not exotic: it is the default
# on current Docker installs, and it is precisely how a host came to have
# 127 GB free on /var/lib/docker and a 100% full / that failed every build.
# Checking DockerRootDir alone would pass on exactly the machine that cannot
# build.
containerd_root="${CONTAINERD_ROOT:-}"
if [ -z "$containerd_root" ] && docker info 2>/dev/null | grep -q 'io.containerd.snapshotter'; then
	if command -v containerd >/dev/null 2>&1; then
		containerd_root="$(containerd config dump 2>/dev/null |
			awk -F\" '/^[[:space:]]*root[[:space:]]*=/ {print $2; exit}')"
	fi
	[ -n "$containerd_root" ] || containerd_root=/var/lib/containerd
fi

check_space() { # path, need_mb, what
	have="$(free_mb "$1")"
	pct="$(inode_pct "$1")"
	printf '  %-22s %6s MB free, inodes %s%% used  (%s)\n' "$3" "$have" "$pct" "$(fs_of "$1")"
	if [ "$have" -lt "$2" ]; then
		echo "    NOT ENOUGH: needs about $2 MB free" >&2
		preflight_failed=1
	fi
	# Inodes exhaust independently of bytes, and a Go build writes a great many
	# small files — df -h looks fine while the build fails the same way.
	if [ "$pct" -ge 95 ]; then
		echo "    INODES NEARLY EXHAUSTED: $pct% used" >&2
		preflight_failed=1
	fi
}

check_space "$docker_root" "$MIN_BUILD_MB" "docker root"
# Only worth a second line when it is a different filesystem; on a host where
# both sit on / it would just be the same number twice.
if [ -n "$containerd_root" ] && [ -d "$(existing_parent "$containerd_root")" ] &&
	[ "$(fs_of "$containerd_root")" != "$(fs_of "$docker_root")" ]; then
	check_space "$containerd_root" "$MIN_BUILD_MB" "containerd images/build"
fi
if [ "$SKIP_BACKUP" = 0 ]; then
	# Estimate from the newest existing backup: the best predictor of the size
	# of the next one is the size of the last one, and it beats a number picked
	# out of the air on a deployment whose history has grown for a year.
	need="$MIN_BACKUP_MB"
	# Backup directories are UTC timestamps, so newest is last in sort order —
	# no need to stat them, and it handles an empty directory cleanly.
	newest="$(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d 2>/dev/null |
		sort | tail -1)"
	if [ -n "$newest" ]; then
		last_mb="$(du -sm "$newest" 2>/dev/null | awk '{print $1}')"
		[ -n "$last_mb" ] && [ "$last_mb" -gt 0 ] && need=$(( last_mb * 12 / 10 ))
		[ "$need" -lt "$MIN_BACKUP_MB" ] && need="$MIN_BACKUP_MB"
	fi
	check_space "$BACKUP_DIR" "$need" "backup target"
fi

if [ "$preflight_failed" = 1 ]; then
	cat >&2 <<-EOF

	Refusing to start: there is not enough disk for this to finish.

	Free space, or put the backup somewhere with room:

	  docker builder prune -af          # build cache, safe with the stack up
	  docker image prune -af            # unreferenced images
	  docker system df                  # what is actually holding the space
	  BACKUP_DIR=/some/large/disk $0

	Thresholds are MIN_BUILD_MB (now $MIN_BUILD_MB) and MIN_BACKUP_MB (now
	$MIN_BACKUP_MB) if this is wrong for your host.
	EOF
	exit 1
fi

# ---------------------------------------------------------------- 4. backup
# Before anything is recreated: an upgrade that goes wrong is a restore, and a
# restore needs a backup taken while the old version was still running.
if [ "$SKIP_BACKUP" = 1 ]; then
	say "Skipping backup (--skip-backup)"
else
	say "Backing up to $BACKUP_DIR"
	run "$ROOT/scripts/backup.sh" "$BACKUP_DIR"
	# Prune, because this script is the reason there are many: it takes a full
	# dump plus a metrics snapshot on *every* run, and without this they
	# accumulate on whatever filesystem BACKUP_DIR sits on until it fills. On
	# one host that was a 19 GB root filesystem, and the first thing the full
	# disk broke was this upgrade.
	#
	# Only directories matching the timestamp backup.sh generates are ever
	# removed, so pointing BACKUP_DIR at a directory holding anything else
	# cannot lose it.
	if [ "$KEEP" -gt 0 ] && [ "$DRY_RUN" = 0 ]; then
		stale="$(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d \
			-regextype posix-extended -regex '.*/[0-9]{8}-[0-9]{6}$' 2>/dev/null |
			sort -r | tail -n +$((KEEP + 1)))"
		if [ -n "$stale" ]; then
			echo "  pruning $(printf '%s\n' "$stale" | wc -l) backup(s), keeping the newest $KEEP"
			printf '%s\n' "$stale" | while IFS= read -r old_backup; do
				[ -n "$old_backup" ] || continue
				rm -rf -- "$old_backup"
			done
		fi
	fi
fi

# rollback restores the state this run started from, and is called whenever a
# step that could leave the stack down fails.
#
# It is deliberately not a restore. Two things it puts back — the checkout and
# the running containers — and one it will not touch: the database. goose does
# not roll a migration back, so if the new api started long enough to migrate,
# rolling the code back leaves older code on a newer schema. That is safe as
# far as the schema goes (it is a superset), but anything the new migration
# introduced is now being served by code that knows nothing about it, and the
# only real fix is to go forward again or restore data from the backup.
rollback() {
	reason="$1"
	echo >&2
	echo "FAILED: $reason" >&2

	if [ "$NO_ROLLBACK" = 1 ]; then
		echo >&2
		echo "--no-rollback given, so nothing was undone. The stack is in whatever" >&2
		echo "state that failure left it. To recover by hand:" >&2
		echo "  $0 --recover" >&2
		exit 1
	fi

	say "Rolling back"
	if [ "$moved_checkout" = 1 ]; then
		echo "  restoring checkout $before_version ($before_commit)"
		git -C "$ROOT" checkout --quiet "$before_commit" || {
			echo "  could not restore the checkout — do it by hand:" >&2
			echo "    git -C $ROOT checkout $before_commit" >&2
		}
	else
		echo "  checkout was never moved; leaving it alone"
	fi

	# Whatever images exist are started. After a failed build these are still
	# the previous ones, which is the common case and a full recovery. After a
	# failed recreate the new images are built and tagged, so this brings the
	# *new* code back up rather than the old — say so instead of implying a
	# clean rollback.
	echo "  starting the stack from the images on this host"
	if "${COMPOSE[@]}" --profile app up -d; then
		echo "  services started"
	else
		echo >&2
		echo "  could not start the stack. Current state:" >&2
		"${COMPOSE[@]}" --profile app ps >&2 || true
		echo "  logs:  ${COMPOSE[*]} logs --tail 50 api" >&2
		exit 1
	fi

	cat >&2 <<-EOF

	Rolled back. Two things to check before assuming this is over:

	  * If the api started at any point on the new code, its migrations have
	    already been applied and are NOT undone by this rollback. Compare the
	    schema version against the highest file in backend/migrations:
	      ${COMPOSE[*]} exec postgres psql -tAX -U netinv -d netinv -c 'SELECT max(version_id) FROM goose_db_version'
	    A schema ahead of the checkout means old code on new data.
	  * The original failure is still unfixed. Re-running this script will hit
	    it again until it is addressed.
	EOF
	exit 1
}

# ----------------------------------------------------------------- 5. deploy
# Build first, then start. Doing it in one `up --build` step leaves the stack
# half-recreated when a build fails partway through; building separately means
# a compile error costs nothing but time.
#
# Images are built locally rather than pulled: the GHCR packages are private,
# so an anonymous pull of ghcr.io/freezxp/netinv-* gets a 401 and the "images
# published per release" path needs a token nobody has by default.
say "Building images"
if [ "$SKIP_BUILD" = 1 ]; then
	echo "  skipped — recovery starts what is already built"
elif ! run "${COMPOSE[@]}" --profile app build; then
	# A build failure has not touched the running stack: compose builds into
	# new images and only swaps them in at `up`. The previous images are still
	# tagged, so rollback here is a genuine return to the old version.
	rollback "the image build failed (see the build output above for the failing stage)"
fi

say "Recreating services"
# --wait blocks until healthchecks pass. The api exits rather than waits when
# Postgres is not up yet, so a few restarts here are normal, not a failure.
if [ "$DRY_RUN" = 1 ]; then
	run "${COMPOSE[@]}" --profile app up -d --wait
else
	if ! "${COMPOSE[@]}" --profile app up -d --wait; then
		echo >&2
		echo "Services did not all come up healthy. Current state:" >&2
		"${COMPOSE[@]}" --profile app ps >&2 || true
		echo >&2
		echo "Logs:  ${COMPOSE[*]} logs --tail 50 api" >&2
		rollback "services did not come up healthy"
	fi
fi

# ----------------------------------------------------------------- 6. verify
say "Verifying"
if [ "$DRY_RUN" = 1 ]; then
	echo "  (dry run — nothing to verify)"
	exit 0
fi

"${COMPOSE[@]}" --profile app ps --format 'table {{.Service}}\t{{.Status}}' || true

# Migrations are embedded in the api binary, so the schema advancing is the
# proof that a *new* binary is running. A database version behind the highest
# file on disk means the image was not rebuilt — the same fault that shows up
# as "goose: no migrations to run" while a new .sql sits in the tree.
pg_container="$(docker ps --format '{{.Names}}' \
	--filter "label=com.docker.compose.project=$PROJECT" | grep -m1 -- '-postgres-' || true)"
if [ -n "$pg_container" ]; then
	disk_version="$(find "$ROOT/backend/migrations" -name '[0-9]*.sql' -printf '%f\n' 2>/dev/null |
		sed 's/_.*//' | sort -n | tail -1 | sed 's/^0*//')"
	db_version=""
	for _ in $(seq 1 15); do
		db_version="$(docker exec "$pg_container" psql -tAX -U netinv -d netinv \
			-c 'SELECT max(version_id) FROM goose_db_version' 2>/dev/null || true)"
		[ -n "$db_version" ] && [ "$db_version" = "$disk_version" ] && break
		sleep 2
	done
	echo "  schema version: ${db_version:-unknown} (highest migration on disk: ${disk_version:-unknown})"
	# The two directions are different faults and only one of them is about a
	# stale image, so do not report them with one message.
	if [ -n "$db_version" ] && [ -n "$disk_version" ] && [ "$db_version" -lt "$disk_version" ]; then
		echo >&2
		echo "  Schema is BEHIND the migrations in this checkout. The api image is" >&2
		echo "  probably stale — migrations ship inside the binary, so a new .sql" >&2
		echo "  file does nothing until the image is rebuilt. Check:" >&2
		echo "    ${COMPOSE[*]} logs --tail 30 api" >&2
	elif [ -n "$db_version" ] && [ -n "$disk_version" ] && [ "$db_version" -gt "$disk_version" ]; then
		echo >&2
		echo "  Schema is AHEAD of this checkout: the database has migration" >&2
		echo "  $db_version applied and the tree only goes to $disk_version. You have just" >&2
		echo "  deployed code older than the data it runs against. goose does not" >&2
		echo "  roll anything back on its own, so the schema is intact — but the" >&2
		echo "  build is a downgrade, and whatever shipped with the later" >&2
		echo "  migrations is gone. Check out the branch the stack was built from" >&2
		echo "  (or merge it in) and re-run." >&2
	fi
fi

# The UI, through the frontend proxy, is the end-to-end check: it exercises
# nginx, the api and its database in one request. -k because the certificate is
# self-signed unless someone replaced it.
ui_port="$(docker ps --format '{{.Ports}}' \
	--filter "label=com.docker.compose.project=$PROJECT" |
	grep -o '0.0.0.0:[0-9]*->443/tcp' | head -1 | sed 's/.*:\([0-9]*\)->.*/\1/')"
ui_port="${ui_port:-8443}"
printf '  waiting for the UI on :%s' "$ui_port"
ui_ok=0
for _ in $(seq 1 30); do
	if curl -skf -o /dev/null "https://localhost:$ui_port/"; then ui_ok=1; break; fi
	printf '.'; sleep 2
done
echo
if [ "$ui_ok" = 1 ]; then
	echo "  UI is answering on https://localhost:$ui_port"
else
	echo "  UI did not answer on :$ui_port — check the frontend service." >&2
fi

after_version="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo unknown)"
cat <<EOF

  Upgrade complete: $before_version → $after_version

  If something is wrong, roll back to the previous code and re-run this script:

    git -C $ROOT checkout $before_commit
    $0 --skip-backup

  That restores the previous binaries. It does NOT undo a migration — if the
  new version added one, roll the data back from the backup instead
  (scripts/restore.sh, doc 20 §12.3), and read its --force warning first.
EOF
