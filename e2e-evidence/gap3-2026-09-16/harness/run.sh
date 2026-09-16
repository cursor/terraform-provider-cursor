#!/usr/bin/env bash
# Gap3 mock e2e for terraform-provider-cursor: OpenTofu + dev_overrides + a local stand-in for api.cursor.com
# (Origin API + Team Admin API + Organization Admin API). No live Origin, no real credentials.
#
#   PROVIDER_SRC=<repo root> [GO=go] [TOFU=tofu] [OUT=./out] ./run.sh
#
# Runs: plan -> apply -> re-plan (no-op) -> in-place update -> replace (email change) -> unknown-at-plan replace
#       -> directory drift replace -> import by email -> destroy refused by ruleset deletion_protection -> destroy.
# Then verify.sh asserts the wire contract over the HTTP trace (user_/grp_ ids only, never email/name/g_).
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
OUT=${OUT:-$HERE/out}
BIN=$OUT/bin
GO=${GO:-go}
TOFU=${TOFU:-tofu}
PROXY=${PROXY:-127.0.0.1:18443}
PROVIDER_SRC=${PROVIDER_SRC:-$(git -C "$HERE" rev-parse --show-toplevel 2>/dev/null || true)}
if [ -z "$PROVIDER_SRC" ] || [ ! -f "$PROVIDER_SRC/main.go" ]; then
  echo "set PROVIDER_SRC to the terraform-provider-cursor checkout" >&2
  exit 2
fi

rm -rf "$OUT"
mkdir -p "$BIN" "$OUT/tf"
cp "$HERE/tf/main.tf" "$OUT/tf/main.tf"

echo "provider source: $PROVIDER_SRC @ $(git -C "$PROVIDER_SRC" rev-parse HEAD 2>/dev/null || echo '?')" | tee "$OUT/00-build.log"
( cd "$PROVIDER_SRC" && "$GO" build -ldflags "-X main.version=e2e-$(git rev-parse --short HEAD 2>/dev/null || echo local)" \
    -o "$BIN/terraform-provider-cursor" . ) >> "$OUT/00-build.log" 2>&1 || { cat "$OUT/00-build.log"; exit 1; }
( cd "$HERE/mock" && "$GO" build -o "$BIN/mockcursor" . ) >> "$OUT/00-build.log" 2>&1 || { cat "$OUT/00-build.log"; exit 1; }
echo "built: $(ls "$BIN" | tr '\n' ' ')" | tee -a "$OUT/00-build.log"

cat > "$OUT/tofurc" <<EOF
provider_installation {
  dev_overrides {
    "cursor/cursor" = "$BIN"
  }
  direct {}
}
EOF

# Fake credentials: only ever sent to the local mock. Not key_/crsr_ prefixed, so no token exchange happens.
export CURSOR_TOKEN=e2e-session-token-FAKE
export CURSOR_TEAM_API_KEY=e2e-team-admin-key-FAKE
export CURSOR_ORGANIZATION_API_KEY=e2e-org-admin-key-FAKE

"$BIN/mockcursor" -trace "$OUT/http-trace.log" -ca-out "$OUT/ca.pem" -proxy-addr "$PROXY" \
  -token "$CURSOR_TOKEN" -team-key "$CURSOR_TEAM_API_KEY" -org-key "$CURSOR_ORGANIZATION_API_KEY" \
  > "$OUT/mock-stderr.log" 2>&1 &
MOCK_PID=$!
trap 'kill $MOCK_PID 2>/dev/null' EXIT
for _ in $(seq 1 50); do [ -s "$OUT/ca.pem" ] && break; sleep 0.1; done
[ -s "$OUT/ca.pem" ] || { echo "mock did not start"; cat "$OUT/mock-stderr.log"; exit 1; }
cat "$OUT/mock-stderr.log"

# The provider hardcodes https://api.cursor.com: tunnel it through the mock's CONNECT proxy and trust the test CA.
export TF_CLI_CONFIG_FILE="$OUT/tofurc"
export TF_IN_AUTOMATION=1
export TF_INPUT=0
export HTTPS_PROXY="http://$PROXY"
export NO_PROXY=
export SSL_CERT_FILE="$OUT/ca.pem"

cd "$OUT/tf"
FAIL=0
mark() { curl -s -o /dev/null -X POST --data "$1" "http://$PROXY/_mock/mark"; echo; echo "################ $1"; }
run() { # run <logname> <expected-exit> <cmd...>
  local name=$1 want=$2; shift 2
  echo "\$ $*" | tee "$OUT/$name.log"
  "$@" 2>&1 | tee -a "$OUT/$name.log"
  local rc=${PIPESTATUS[0]}
  echo "exit=$rc (expected $want)" | tee -a "$OUT/$name.log"
  if [ "$rc" != "$want" ]; then echo "!!!! UNEXPECTED EXIT for $name" | tee -a "$OUT/$name.log"; FAIL=1; fi
}
V="-var alice_permission=admin -var alice_email=erin@acme.com -var late_email=dave@acme.com"

mark "STAGE 0: tofu version / validate"
run 00-version 0 "$TOFU" version
run 00-validate 0 "$TOFU" validate

mark "STAGE A: initial plan (user_email/group_name resolved at plan; late email unknown until apply)"
run A1-plan 0 "$TOFU" plan -out=a.tfplan -no-color
run A2-apply 0 "$TOFU" apply -no-color a.tfplan
run A3-output 0 "$TOFU" output -json
"$TOFU" output -json > "$OUT/A3-output.json"

mark "STAGE B: re-plan with no config change must be a no-op (refresh + re-resolution idempotent)"
run B1-replan-noop 0 "$TOFU" plan -detailed-exitcode -no-color

mark "STAGE C: permission change is an in-place update (upsert), no replace"
run C1-plan 0 "$TOFU" plan -out=c.tfplan -no-color -var alice_permission=admin
run C2-apply 0 "$TOFU" apply -no-color c.tfplan

mark "STAGE D: user_email change forces replacement; new user_ id resolved at plan"
run D1-plan 0 "$TOFU" plan -out=d.tfplan -no-color -var alice_permission=admin -var alice_email=erin@acme.com
run D2-apply 0 "$TOFU" apply -no-color d.tfplan

mark "STAGE E: unknown-at-plan replace: late_email changes, terraform_data.output unknown until apply -> user/id unknown, stale id dropped"
run E1-plan 0 "$TOFU" plan -out=e.tfplan -no-color $V
run E2-apply 0 "$TOFU" apply -no-color e.tfplan
run E3-replan-noop 0 "$TOFU" plan -detailed-exitcode -no-color $V

mark "STAGE F: directory drift: erin's email now resolves to a different user_ id -> plan re-resolves and forces replacement on user"
curl -s -o /dev/null -X POST -H 'Content-Type: application/json' --data @"$HERE/members-rekeyed.json" "http://$PROXY/_mock/members"
run F1-plan 0 "$TOFU" plan -out=f.tfplan -no-color $V
run F2-apply 0 "$TOFU" apply -no-color f.tfplan

mark "STAGE G: import by user_email resolves through Team Admin API; following plan is a no-op"
# Seed a grant that exists in Origin but not in state (frank, read), then import it by email.
curl -s -o /dev/null -x "http://$PROXY" --cacert "$OUT/ca.pem" -H "Authorization: Bearer $CURSOR_TOKEN" -H 'Content-Type: application/json' \
  -X POST https://api.cursor.com/v1/origin/repos/acme/rocket/grants --data '{"user":{"id":"user_01k2ja2000e0080000000000f6"},"permission":"read"}'
cat > import.tf <<'EOF'
resource "cursor_origin_repo_grant" "frank" {
  owner      = "acme"
  repo       = "rocket"
  permission = "read"
  user_email = "frank@acme.com"
}
EOF
run G1-import 0 "$TOFU" import -no-color $V cursor_origin_repo_grant.frank 'acme/rocket:user_email:frank@acme.com'
run G2-replan-noop 0 "$TOFU" plan -detailed-exitcode -no-color $V
run G3-state-show 0 "$TOFU" state show -no-color cursor_origin_repo_grant.frank

mark "STAGE H: destroy is refused while ruleset deletion_protection=true (PR #12 fix), then allowed after it is set to false"
run H1-destroy-refused 1 "$TOFU" plan -destroy -no-color $V
run H2-unprotect 0 "$TOFU" apply -auto-approve -no-color $V -var ruleset_deletion_protection=false
run H3-destroy 0 "$TOFU" destroy -auto-approve -no-color $V -var ruleset_deletion_protection=false

mark "END: mock state after destroy (expect no grants, no rulesets, wireViolations=0)"
curl -s "http://$PROXY/_mock/state" | tee "$OUT/Z-mock-state-after-destroy.json"; echo

echo
echo "DRIVER RESULT: FAIL=$FAIL"
"$HERE/verify.sh" "$OUT/http-trace.log" | tee "$OUT/verify.log"
VERIFY=${PIPESTATUS[0]}
rm -f "$OUT"/tf/*.tfplan
echo "OVERALL: driver FAIL=$FAIL verify exit=$VERIFY"
[ "$FAIL" = 0 ] && [ "$VERIFY" = 0 ]
