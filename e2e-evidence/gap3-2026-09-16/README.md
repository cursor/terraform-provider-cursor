# Gap3 pre-merge testing evidence (Origin ruleset + grants stack)

Evidence-only snapshot. Nothing here is shipped with the provider; it exists so the pre-merge
run for the Gap3 public stack can be inspected and reproduced.

| Item | Value |
| --- | --- |
| Stack tip tested | PR #13 `cursor/gap3-origin-grants-principals` @ `aab5ba08376f995aba4048a77f8c65c33f340746` |
| Stack base | PR #12 `cursor/gap3-origin-client-repo-ruleset` @ `39791000360978aae1ae7630bf6b361b8c56948a` (also unit-tested alone, `unit/gotest-pr12-tip.log`) |
| Toolchain | go1.25.0 linux/amd64, OpenTofu v1.10.6 (GitHub release, SHA256 verified) |
| Live Origin | **not used**. All API traffic went to a local mock of `api.cursor.com`. No live apply was performed. |

## unit/

`gofmt -l .` (clean), `go vet ./...`, `go build ./...`, `go test ./... -count=1 -v` (149 PASS / 0 FAIL),
`go test -race ./...`, plus a focused verbose run of the Origin ruleset, grant, principal and Admin API
resolution tests (84 PASS), all on the stack tip. `tests-*.txt` list test names on `main` (67),
the #12 tip (103) and the #13 tip (149); `tests-added-by-pr13.txt` is the 46-test delta of #13.

## harness/

Self-contained mock e2e. `mock/main.go` (stdlib only) stands in for `https://api.cursor.com`:
the public Origin API under `/v1/origin` (repo, repo/owner grants, rulesets), the Team Admin API
(`GET /teams/members`, HTTP Basic) and the Organization Admin API (`GET /organizations/groups?name=`,
HTTP Basic). It terminates TLS with a throwaway CA for `api.cursor.com` and exposes an HTTP CONNECT
proxy; `run.sh` points `HTTPS_PROXY` / `SSL_CERT_FILE` at it, so the provider's hardcoded host is
intercepted without any code change. The mock rejects (HTTP 400, counted as a wire violation) any
grant body that is not `user_` / `grp_` / `teamGroup` shaped or that contains an email, a group name
or an internal `g_` id.

```
PROVIDER_SRC=/path/to/terraform-provider-cursor GO=go TOFU=tofu ./harness/run.sh
```

Stages driven through OpenTofu with `dev_overrides`:

| Stage | What | Expected |
| --- | --- | --- |
| A | plan + apply of ruleset, 4 repo grants, 3 owner grants, data sources | `user_email` / `group_name` resolved to `user_` / `grp_` **at plan**; a `user_email` fed from `terraform_data.output` is unknown at plan and resolved at apply |
| B | re-plan | `No changes.` |
| C | `permission` write → admin | in-place update (upsert POST), no replace |
| D | `user_email` alice → erin | `-/+` replace, new `user_` id already in the plan |
| E | `terraform_data` input change → `user_email` unknown at plan | `-/+` replace with `user` and `id` `(known after apply)` (stale prior id dropped), resolved at apply |
| F | directory drift (same email, new `user_` id in Team Admin) | plan re-resolves, `user.id` change forces replacement |
| G | `tofu import ... 'acme/rocket:user_email:frank@acme.com'` | import resolves via Team Admin; following plan `No changes.` |
| H | destroy with ruleset `deletion_protection=true`, then `false` | destroy plan refused (exit 1); after unprotecting, `Destroy complete! 10 destroyed` |

`verify.sh` flattens the HTTP trace and asserts the wire contract (see `e2e-run/verify.log`).

## e2e-run/

Logs of the canonical run: per-stage `tofu` output, `A3-output.json`, the full redacted
`http-trace.log` (215 provider requests; credentials are labelled, never printed), `verify.log`
(11/11 PASS), and the mock's state after destroy (no grants, no rulesets, `wireViolations: 0`).
