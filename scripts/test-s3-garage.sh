#!/bin/sh
# Starts a throwaway Garage on this machine for the S3 image store tests, and
# prints the variables that point the tests at it:
#
#   eval "$(scripts/test-s3-garage.sh)"
#   go test -count=1 -tags sqlite -run 'S3|ImageStore|ImageSync|StreamImages' .
#   scripts/test-s3-garage.sh stop
#
# Local Docker only. The key and secret are generated per run and die with
# the container.
set -eu

NAME=${WF_TEST_GARAGE_NAME:-wisp-test-garage}
IMAGE=${WF_TEST_GARAGE_IMAGE:-dxflrs/garage:v1.0.1}
PORT=${WF_TEST_GARAGE_PORT:-3900}
BUCKET=wisp-test

if [ "${1:-}" = stop ]; then
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	exit 0
fi

docker rm -f "$NAME" >/dev/null 2>&1 || true

conf=$(mktemp)
trap 'rm -f "$conf"' EXIT
cat >"$conf" <<TOML
metadata_dir = "/var/lib/garage/meta"
data_dir = "/var/lib/garage/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "[::]:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "$(openssl rand -hex 32)"

[s3_api]
s3_region = "garage"
api_bind_addr = "[::]:3900"
root_domain = ".s3.garage.localhost"
TOML
chmod 644 "$conf"

docker run -d --name "$NAME" -p "127.0.0.1:$PORT:3900" \
	-v "$conf:/etc/garage.toml:ro" "$IMAGE" >/dev/null

g() { docker exec -e RUST_LOG=warn "$NAME" /garage "$@"; }

i=0
until node=$(g node id -q 2>/dev/null); do
	i=$((i + 1))
	[ $i -lt 30 ] || { echo "garage did not start" >&2; docker logs "$NAME" >&2; exit 1; }
	sleep 1
done
node=${node%%@*}

g layout assign -z dc1 -c 1G "$node" >/dev/null
g layout apply --version 1 >/dev/null
g bucket create "$BUCKET" >/dev/null

# Garage key IDs are GK plus 24 hex digits; secrets are 64 hex digits.
key="GK$(openssl rand -hex 12)"
secret=$(openssl rand -hex 32)
g key import --yes "$key" "$secret" >/dev/null
g bucket allow --read --write --owner "$BUCKET" --key "$key" >/dev/null

# The admin commands above talk RPC, not S3. Wait until the S3 API answers
# too, so a caller that starts testing straight away (CI) does not race it.
# Any HTTP status will do: anonymous requests are refused, which is an answer.
i=0
until curl -s -o /dev/null "http://127.0.0.1:$PORT/"; do
	i=$((i + 1))
	[ $i -lt 30 ] || { echo "garage S3 API did not answer" >&2; docker logs "$NAME" >&2; exit 1; }
	sleep 1
done

echo "export WF_TEST_S3_ENDPOINT=http://127.0.0.1:$PORT"
echo "export WF_TEST_S3_REGION=garage"
echo "export WF_TEST_S3_BUCKET=$BUCKET"
echo "export WF_TEST_S3_ACCESS_KEY_ID=$key"
echo "export WF_TEST_S3_SECRET_ACCESS_KEY=$secret"
