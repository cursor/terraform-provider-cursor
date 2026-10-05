# Automation compatibility

The automation extension adds Slack `channels`, `git_label`, and Slack
`top_level_only` without requiring existing configurations to adopt them.

- Existing `channel = "C123"` configurations continue to work.
- `channel` and `channels` may coexist and are preserved independently. Cursor
  routes using populated `channels`; the legacy scalar is not added to that list.
- `channels = []` falls back to `channel`. State normalization retains this
  empty-list representation only if the API reports equivalent routing; it does
  not hide extra destinations or a changed scalar.
- Omitting `channels` preserves an imported server list on unrelated edits.
  Changing a legacy scalar replaces that list only when `channels` is omitted.
- To intentionally reduce multi-channel routing, explicitly set
  `channels = ["C123"]`. Removing `channels` alone is not a reset operation.
- Omitted `top_level_only` preserves the server value/default. Use explicit
  `false` to turn off an existing top-level-only restriction.
- Git label triggers must enable at least one added/removed event and at least
  one PR/issue object type. Omitted flags resolve from existing state or to the
  API's false value for newly added triggers.

Run the opt-in, local-only Terraform CLI regression:

```sh
TF_ACC=1 go test ./internal/provider -run TestTerraformWorkflowUpgrade -v
```

It requires Terraform, Git, Go, and the `v0.7.0` Git tag. The test builds the
released provider, imports state against a localhost mock API, verifies a
no-change upgrade plan, applies empty-list fallback, and applies new fields
whose values come from initially unknown `terraform_data` outputs. It then
asserts no-change plans after apply. It strips live Cursor credentials and
Terraform flags and uses temporary files and test-only provider overrides.

The regular tests additionally check that name/prompt/scope updates preserve
private-worker routing, authentication restrictions, completion reactions,
thread behavior, memory settings, and the complete trigger/action definition.
