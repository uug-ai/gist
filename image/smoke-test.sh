#!/usr/bin/env bash
# Hardened image smoke test, run by uug-ai/workflows release-create (profile standalone).
set -euo pipefail
image="${1:?usage: image/smoke-test.sh IMAGE}"
architecture=$(docker image inspect "$image" --format '{{.Architecture}}')
bash image/check-image.sh "$image" "$architecture" "${RUNTIME_EVIDENCE_DIR:-.build/evidence}"
args=(--rm --network none --read-only --cap-drop ALL
  --security-opt no-new-privileges --pids-limit 32 --memory 128m --cpus 1)

# Offline and in memory: create, view, read raw, edit and diff a gist over loopback.
timeout 30 docker run "${args[@]}" "$image" --self-test

# Unknown arguments must fail with usage (exit 2), not start the server.
if timeout 30 docker run "${args[@]}" "$image" --invalid-option; then
  echo "invalid arguments must not start the server" >&2
  exit 1
else
  test "$?" -eq 2
fi

# Without a writable data directory the server must refuse to start (exit 1)
# rather than lose gists.
if timeout 30 docker run "${args[@]}" "$image"; then
  echo "the server must not start without a writable GIST_DATA_DIR" >&2
  exit 1
else
  test "$?" -eq 1
fi

# With a writable data directory it serves gists.
container=$(docker run -d --read-only --cap-drop ALL --security-opt no-new-privileges \
  --pids-limit 32 --memory 128m --cpus 1 -p 127.0.0.1::8080 \
  --tmpfs /data:rw,uid=65532,gid=65532,mode=0700 "$image")
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT
port=$(docker port "$container" 8080/tcp | head -n1 | sed 's/.*://')
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:${port}/healthz" | grep -qx ok
url=$(printf 'hello from the smoke test\n' | curl -fsS -T - "http://127.0.0.1:${port}/")
case "$url" in
  "http://127.0.0.1:${port}/"*) ;;
  *) echo "unexpected create response: $url" >&2; exit 1 ;;
esac
test "$(curl -fsS "$url")" = "hello from the smoke test"
