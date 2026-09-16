#!/usr/bin/env bash
# Asserts the wire contract over the mock's HTTP trace:
#   * every Origin grant write (POST/DELETE .../grants) names the principal by user_ / grp_ id or teamGroup kind
#   * no Origin request body ever carries an email, a group name, or an internal g_ id
#   * Origin calls use Bearer; Team Admin uses Basic(team key); Org Admin uses Basic(org key); nothing else
#   * no 4xx/5xx from the mock (a 400 would be a WIRE CONTRACT VIOLATION)
set -u
TRACE=${1:-http-trace.log}
FLAT=${TRACE%.log}.flat
FAIL=0
ok()   { echo "PASS  $*"; }
bad()  { echo "FAIL  $*"; FAIL=1; }

# Flatten each request block "#NNN ... blank line" into one line: "#NNN|METHOD|URL|auth|ua|request|response"
flat() {
  awk '
    /^#[0-9]+ / { if (n) print rec; n=1; split($0,a," "); rec=a[1]"|"a[3]"|"a[4]; next }
    /^     auth: /       { sub(/^     auth: /,"");       rec=rec"|"$0; next }
    /^     user-agent: / { sub(/^     user-agent: /,""); rec=rec"|"$0; next }
    /^     request:  /   { sub(/^     request:  /,"");   rec=rec"|"$0; next }
    /^     response: /   { sub(/^     response: /,"");   rec=rec"|"$0; next }
    END { if (n) print rec }
  ' "$TRACE"
}
flat > $FLAT

# Only requests made by the provider (curl seeding/smoke calls are excluded by user-agent).
PROVIDER=$(awk -F'|' '$5 ~ /^terraform-provider-cursor\// ' $FLAT)
total=$(printf '%s\n' "$PROVIDER" | grep -c .)
echo "provider requests in trace: $total"
printf '%s\n' "$PROVIDER" | awk -F'|' '{print $2" "$3}' | sed -E 's/\?.*//; s#/rulesets/[^ ]+#/rulesets/{id}#' | sort | uniq -c | sort -rn | sed 's/^/      /'
echo

# 1. Grant writes: bodies must be id-shaped
WRITES=$(printf '%s\n' "$PROVIDER" | awk -F'|' '($2=="POST"||$2=="DELETE") && $3 ~ /\/grants$/')
nw=$(printf '%s\n' "$WRITES" | grep -c .)
echo "Origin grant writes (POST/DELETE .../grants): $nw"
printf '%s\n' "$WRITES" | awk -F'|' '{printf "      %s %-6s %-55s %s\n", $1, $2, substr($3,24), $6}'
echo
badw=$(printf '%s\n' "$WRITES" | awk -F'|' '$6 !~ /^\{"(user":\{"id":"user_|group":\{"id":"grp_|teamGroup":\{"kind":"(members|admins)")/' | grep -c .)
[ "$badw" = 0 ] && ok "every grant write body starts with a user_ id, a grp_ id, or a teamGroup kind" || bad "$badw grant write bodies are not id-shaped"

# 2. No Origin request body carries an email / group name / internal id
ORIGIN=$(printf '%s\n' "$PROVIDER" | awk -F'|' '$3 ~ /^https:\/\/api\.cursor\.com\/v1\/origin\//')
leak=$(printf '%s\n' "$ORIGIN" | awk -F'|' '$6 ~ /@|Engineering|Security|engineering|"g_/' | grep -c .)
[ "$leak" = 0 ] && ok "no Origin request body contains an email address, a group name, or a g_ internal id" || bad "$leak Origin request bodies leak email/group name/g_ id"
grep -q "WIRE CONTRACT VIOLATION" "$TRACE" && bad "mock recorded a WIRE CONTRACT VIOLATION" || ok "mock recorded zero wire-contract violations"

# 3. Auth per API
nbo=$(printf '%s\n' "$ORIGIN" | awk -F'|' '$4 != "Bearer(<provider token, redacted>)"' | grep -c .)
[ "$nbo" = 0 ] && ok "all $(printf '%s\n' "$ORIGIN" | grep -c .) Origin requests carry Authorization: Bearer <provider token>" || bad "$nbo Origin requests without the provider bearer token"
TEAM=$(printf '%s\n' "$PROVIDER" | awk -F'|' '$3 ~ /^https:\/\/api\.cursor\.com\/teams\/members/')
nbt=$(printf '%s\n' "$TEAM" | awk -F'|' '$4 != "Basic(team_api_key as username, empty password)"' | grep -c .)
[ "$nbt" = 0 ] && ok "all $(printf '%s\n' "$TEAM" | grep -c .) Team Admin requests use Basic(team_api_key, empty password)" || bad "$nbt Team Admin requests with wrong auth"
ORG=$(printf '%s\n' "$PROVIDER" | awk -F'|' '$3 ~ /^https:\/\/api\.cursor\.com\/organizations\/groups/')
nbg=$(printf '%s\n' "$ORG" | awk -F'|' '$4 != "Basic(organization_api_key as username, empty password)"' | grep -c .)
[ "$nbg" = 0 ] && ok "all $(printf '%s\n' "$ORG" | grep -c .) Organization Admin requests use Basic(organization_api_key, empty password)" || bad "$nbg Org Admin requests with wrong auth"
nq=$(printf '%s\n' "$ORG" | awk -F'|' '$3 !~ /\?name=(Engineering|Security)$/' | grep -c .)
[ "$nq" = 0 ] && ok "every Organization Admin lookup used the exact ?name= filter" || bad "$nq Org Admin lookups without ?name="
other=$(printf '%s\n' "$PROVIDER" | awk -F'|' '$3 !~ /^https:\/\/api\.cursor\.com\/(v1\/origin\/|teams\/members|organizations\/groups)/' | grep -c .)
[ "$other" = 0 ] && ok "provider reached no host or path other than api.cursor.com Origin/Team Admin/Org Admin" || bad "$other requests to unexpected routes"

# 4. Status codes
nerr=$(printf '%s\n' "$PROVIDER" | awk -F'|' '{split($7,s," "); if (s[1]+0 >= 400) print}' | grep -c .)
[ "$nerr" = 0 ] && ok "no 4xx/5xx responses to any provider request" || bad "$nerr provider requests got 4xx/5xx"

# 5. Team Admin resolution never used the removed duplicate seat, and the late/unknown case resolved at apply
grep -q '"user":{"id":"user_01k2ja2000e0080000000000b9"' "$TRACE" && bad "removed member user_...b9 reached the wire" || ok "removed duplicate seat (isRemoved=true, same email) never reached the wire"
printf '%s\n' "$WRITES" | grep -q 'POST.*rocket/grants|.*{"user":{"id":"user_01k2ja2000e0080000000000d4"},"permission":"read"}' && ok "unknown-at-plan email resolved at apply and posted as user_...d4" || bad "expected POST for user_...d4 (late grant after replace)"

echo
echo "VERIFY RESULT: FAIL=$FAIL"
exit $FAIL
