package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const replaceRoute = "POST /namespaces/acme/inbound-ip-allowlist/entries:replace"

func TestOriginInboundIPAllowlistReplaceRequest(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	ctx := context.Background()
	client := mock.client()

	result, err := client.replaceOriginInboundIPAllowlistEntries(ctx, "acme", originInboundIPAllowlistReplace{
		Entries: []originInboundIPAllowlistEntryAdd{{CIDR: "198.51.100.7"}, {CIDR: "203.0.113.0/24", Description: "Office", Enabled: true}},
	})
	if err != nil {
		t.Fatalf("replace error: %v", err)
	}
	if body := mock.lastBody(replaceRoute); body != `{"entries":[{"cidr":"198.51.100.7","enabled":false},{"cidr":"203.0.113.0/24","description":"Office","enabled":true}]}` {
		t.Fatalf("replace body = %s", body)
	}
	if result.AddedCount != 2 || len(result.Allowlist.Entries) != 2 || result.Allowlist.Etag != mock.etag("acme") {
		t.Fatalf("result = %#v", result)
	}

	etag := result.Allowlist.Etag
	if _, err := client.replaceOriginInboundIPAllowlistEntries(ctx, "acme", originInboundIPAllowlistReplace{Etag: etag, AllowEmpty: true}); err != nil {
		t.Fatalf("empty replace error: %v", err)
	}
	if body := mock.lastBody(replaceRoute); body != fmt.Sprintf(`{"entries":[],"etag":%q,"allowEmpty":true}`, etag) {
		t.Fatalf("empty replace body = %s", body)
	}
	if len(mock.entries("acme")) != 0 {
		t.Fatalf("entries = %#v, want none", mock.entries("acme"))
	}
	if _, err := client.replaceOriginInboundIPAllowlistEntries(ctx, "acme", originInboundIPAllowlistReplace{}); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("empty replace without allowEmpty error = %v, want HTTP 400", err)
	}
}

func TestOriginInboundIPAllowlistReplaceRejectsMismatchedResponses(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := testOriginClient(server)
	want := originInboundIPAllowlistReplace{Entries: []originInboundIPAllowlistEntryAdd{{CIDR: "203.0.113.0/24", Description: "Office", Enabled: true}}}
	for raw, msg := range map[string]string{
		`{}`:               "without an allowlist",
		`{"allowlist":{}}`: "returned 0 inbound IP allowlist entries after replacing them with 1",
		`{"allowlist":{"entries":[{"id":"nsip_1","cidr":"198.51.100.7","description":"Office","enabled":true}]}}`: "did not return inbound IP allowlist entry 203.0.113.0/24",
		`{"allowlist":{"entries":[{"id":"nsip_1","cidr":"203.0.113.0/24","enabled":true}]}}`:                      `with description "" and enabled true, want "Office" and true`,
		`{"allowlist":{"entries":[{"cidr":"203.0.113.0/24"}]}}`:                                                   "without an id",
	} {
		body = raw
		if _, err := client.replaceOriginInboundIPAllowlistEntries(context.Background(), "acme", want); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("body %s: error = %v, want %q", raw, err, msg)
		}
	}
	body = `{"allowlist":{"entries":[{"id":"nsip_1","cidr":"203.0.113.0/24","description":"Office","enabled":true}],"etag":"e1"}}`
	if result, err := client.replaceOriginInboundIPAllowlistEntries(context.Background(), "acme", want); err != nil || result.Allowlist.Etag != "e1" {
		t.Fatalf("result = %#v, %v", result, err)
	}
}

func TestOriginInboundIPAllowlistEntriesCreate(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	stale := mock.add("acme", "192.0.2.1", "not in config", true)

	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	plan := sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}, inboundEntries{"2001:db8::/32", "", false})
	got := createInboundEntries(t, res, plan)

	if mock.count(replaceRoute) != 1 {
		t.Fatalf("replace calls = %d, want 1", mock.count(replaceRoute))
	}
	if body := mock.lastBody(replaceRoute); body != `{"entries":[{"cidr":"2001:db8::/32","enabled":false},{"cidr":"203.0.113.0/24","description":"Office","enabled":true}]}` {
		t.Fatalf("create body = %s", body)
	}
	if got.ID.ValueString() != "acme" || got.Etag.ValueString() != mock.etag("acme") || !got.DeletionProtection.ValueBool() {
		t.Fatalf("state = %#v", got)
	}
	entries := inboundEntriesOf(t, got)
	if len(entries) != 2 || entries["203.0.113.0/24"].Description.ValueString() != "Office" || entries["2001:db8::/32"].Enabled.ValueBool() {
		t.Fatalf("entries = %#v", entries)
	}
	for cidr, entry := range entries {
		if !strings.HasPrefix(entry.ID.ValueString(), "nsip_") {
			t.Fatalf("entry %s id = %s", cidr, entry.ID)
		}
	}
	for _, entry := range mock.entries("acme") {
		if entry.ID == stale.ID {
			t.Fatal("create must remove entries the configuration does not list")
		}
	}
}

func TestOriginInboundIPAllowlistEntriesCreateEmptySetAllowsEmpty(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.add("acme", "192.0.2.1", "", true)

	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	got := createInboundEntries(t, res, sampleInboundEntriesModel(t))
	if body := mock.lastBody(replaceRoute); body != `{"entries":[],"allowEmpty":true}` {
		t.Fatalf("body = %s", body)
	}
	if len(inboundEntriesOf(t, got)) != 0 || len(mock.entries("acme")) != 0 {
		t.Fatal("an empty entry set must remove every entry")
	}
}

func TestOriginInboundIPAllowlistEntriesCreateNamesRejectedEntries(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.setCaller("203.0.113.10")
	mock.setEnabled("acme", true)
	mock.add("acme", "203.0.113.0/24", "", true)

	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	resp := createInboundEntriesResponse(t, res, sampleInboundEntriesModel(t, inboundEntries{"198.51.100.7", "", true}))
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "exclude your own address") {
		t.Fatalf("diagnostics = %v, want the lockout rejection surfaced", resp.Diagnostics)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, originStatusError{Code: 3, Message: "invalid inbound IP allowlist entries", Details: []originStatusDetail{{
			Type:            "type.googleapis.com/google.rpc.BadRequest",
			FieldViolations: []originFieldViolation{{Field: "entries[1]", Description: "cidr must not be private"}},
		}}})
	}))
	defer server.Close()
	res = &originInboundIPAllowlistEntriesResource{client: testOriginClient(server)}
	resp = createInboundEntriesResponse(t, res, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "", true}, inboundEntries{"10.0.0.0/8", "", true}))
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "entries[1] (cidr 203.0.113.0/24): cidr must not be private") {
		t.Fatalf("diagnostics = %v, want the violation mapped to the sorted request's cidr", resp.Diagnostics)
	}
}

func TestOriginInboundIPAllowlistEntriesReadRefreshesDrift(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	office := mock.add("acme", "203.0.113.0/24", "Office, east wing", false)
	vpn := mock.add("acme", "198.51.100.7", "VPN", true)

	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	prior := sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true})
	prior.Etag = types.StringValue("stale")
	got := readInboundEntries(t, res, prior)
	entries := inboundEntriesOf(t, got)
	if len(entries) != 2 || entries["203.0.113.0/24"].ID.ValueString() != office.ID || entries["203.0.113.0/24"].Enabled.ValueBool() || entries["203.0.113.0/24"].Description.ValueString() != "Office, east wing" {
		t.Fatalf("entries = %#v", entries)
	}
	if entries["198.51.100.7"].ID.ValueString() != vpn.ID {
		t.Fatal("read must surface entries added outside Terraform")
	}
	if got.Etag.ValueString() != mock.etag("acme") {
		t.Fatalf("etag = %q, want %q", got.Etag, mock.etag("acme"))
	}
}

func TestOriginInboundIPAllowlistEntriesReadHiddenNamespaceKeepsState(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	mock.hide("acme")

	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	state := inboundEntriesState(t, res, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}))
	resp := &resource.ReadResponse{State: state}
	res.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not found or not visible") {
		t.Fatalf("diagnostics = %v, want the namespace 404 surfaced", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("read removed state when the namespace could not be read")
	}
}

func TestOriginInboundIPAllowlistEntriesTreatSpellingsAsDistinctEntries(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	stored := mock.add("acme", "10.0.0.0/8", "Corp", true)

	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	state := readInboundEntries(t, res, sampleInboundEntriesModel(t))
	plan := modifyInboundEntriesPlan(t, res, *state, sampleInboundEntriesModel(t, inboundEntries{"10.0.0.1/8", "Corp", true}))
	if id := inboundEntriesOf(t, &plan)["10.0.0.1/8"].ID; !id.IsUnknown() {
		t.Fatalf("planned id = %s, want unknown for a new spelling", id)
	}
	got := inboundEntriesOf(t, updateInboundEntries(t, res, *state, plan))
	if len(got) != 1 || got["10.0.0.1/8"].ID.ValueString() == stored.ID {
		t.Fatalf("entries = %#v, want 10.0.0.0/8 replaced by a new 10.0.0.1/8 entry", got)
	}
	if entries := mock.entries("acme"); len(entries) != 1 || entries[0].CIDR != "10.0.0.1/8" {
		t.Fatalf("stored = %#v", entries)
	}
}

func TestOriginInboundIPAllowlistEntriesUpdateSendsEtagAndKeepsIDs(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	state := createInboundEntries(t, res, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}, inboundEntries{"198.51.100.7", "VPN", true}))
	before := inboundEntriesOf(t, state)
	etag := state.Etag.ValueString()

	plan := sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", false}, inboundEntries{"2001:db8::/32", "", true})
	plan = modifyInboundEntriesPlan(t, res, *state, plan)
	got := updateInboundEntries(t, res, *state, plan)

	if mock.count(replaceRoute) != 2 {
		t.Fatalf("replace calls = %d, want one per apply", mock.count(replaceRoute))
	}
	if body := mock.lastBody(replaceRoute); !strings.Contains(body, fmt.Sprintf(`"etag":%q`, etag)) {
		t.Fatalf("update body = %s, want the etag from state", body)
	}
	after := inboundEntriesOf(t, got)
	if len(after) != 2 || after["203.0.113.0/24"].ID != before["203.0.113.0/24"].ID || after["203.0.113.0/24"].Enabled.ValueBool() {
		t.Fatalf("after = %#v, want the kept entry updated in place", after)
	}
	if _, ok := after["198.51.100.7"]; ok {
		t.Fatal("an entry dropped from the configuration must be removed")
	}
	if got.Etag.ValueString() == etag || got.Etag.ValueString() != mock.etag("acme") {
		t.Fatalf("etag = %q, want the new etag", got.Etag)
	}
}

func TestOriginInboundIPAllowlistEntriesUpdateRejectsStaleEtag(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	state := createInboundEntries(t, res, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}))
	mock.add("acme", "192.0.2.1", "added elsewhere", true)

	plan := modifyInboundEntriesPlan(t, res, *state, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", false}))
	planValue := tfsdk.Plan{Schema: inboundEntriesSchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Update(ctx, resource.UpdateRequest{Plan: planValue, State: inboundEntriesState(t, res, *state)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the stale etag to be rejected")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "changed after Terraform last read them") || !strings.Contains(detail, "-refresh-only") || !strings.Contains(detail, "HTTP 409") {
		t.Fatalf("detail = %q, want a refresh hint and the 409", detail)
	}
	if len(mock.entries("acme")) != 2 {
		t.Fatal("a rejected replace must not change the entries")
	}
}

func TestOriginInboundIPAllowlistEntriesUpdateOfDeletionProtectionSkipsAPI(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	state := createInboundEntries(t, res, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}))
	plan := *state
	plan.DeletionProtection = types.BoolValue(false)
	plan = modifyInboundEntriesPlan(t, res, *state, plan)
	got := updateInboundEntries(t, res, *state, plan)
	if mock.count(replaceRoute) != 1 {
		t.Fatal("changing only deletion_protection must not call the API")
	}
	if got.DeletionProtection.ValueBool() || !got.Entry.Equal(state.Entry) || got.Etag != state.Etag {
		t.Fatalf("state = %#v", got)
	}
}

func TestOriginInboundIPAllowlistEntriesDelete(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	ctx := context.Background()
	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	state := createInboundEntries(t, res, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}))

	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: inboundEntriesState(t, res, *state)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want deletion_protection to block delete", resp.Diagnostics)
	}
	if mock.count(replaceRoute) != 1 {
		t.Fatal("delete request was sent while protected")
	}

	unprotected := *state
	unprotected.DeletionProtection = types.BoolValue(false)
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: inboundEntriesState(t, res, unprotected)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unprotected delete: %v", resp.Diagnostics)
	}
	if body := mock.lastBody(replaceRoute); body != `{"entries":[],"allowEmpty":true}` {
		t.Fatalf("delete body = %s", body)
	}
	if len(mock.entries("acme")) != 0 {
		t.Fatal("delete must remove every entry")
	}

	mock.hide("acme")
	resp = &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: inboundEntriesState(t, res, unprotected)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("deleting from a namespace that is gone must succeed: %v", resp.Diagnostics)
	}
}

func TestOriginInboundIPAllowlistEntriesPlan(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()

	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	state := createInboundEntries(t, res, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}, inboundEntries{"198.51.100.7", "VPN", true}))
	ids := inboundEntriesOf(t, state)

	same := modifyInboundEntriesPlan(t, res, *state, sampleInboundEntriesModel(t, inboundEntries{"198.51.100.7", "VPN", true}, inboundEntries{"203.0.113.0/24", "Office", true}))
	if !same.Entry.Equal(state.Entry) || !same.Etag.Equal(state.Etag) || same.ID.ValueString() != "acme" {
		t.Fatalf("plan = %#v, want no change when the configuration matches state", same)
	}

	changed := modifyInboundEntriesPlan(t, res, *state, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", false}, inboundEntries{"192.0.2.1", "", true}))
	if !changed.Etag.IsUnknown() {
		t.Fatal("etag must be unknown when the entries change")
	}
	planned := inboundEntriesOf(t, &changed)
	if planned["203.0.113.0/24"].ID != ids["203.0.113.0/24"].ID {
		t.Fatalf("kept entry id = %s, want %s", planned["203.0.113.0/24"].ID, ids["203.0.113.0/24"].ID)
	}
	if !planned["192.0.2.1"].ID.IsUnknown() {
		t.Fatal("a new entry's id must be unknown until apply")
	}

	unprotected := *state
	unprotected.DeletionProtection = types.BoolValue(false)
	moved := sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}, inboundEntries{"198.51.100.7", "VPN", true})
	moved.Namespace, moved.DeletionProtection = types.StringValue("rocket"), types.BoolValue(false)
	moved = modifyInboundEntriesPlan(t, res, unprotected, moved)
	if moved.ID.ValueString() != "rocket" || !moved.Etag.IsUnknown() {
		t.Fatalf("moved plan = %#v", moved)
	}
	for cidr, entry := range inboundEntriesOf(t, &moved) {
		if !entry.ID.IsUnknown() {
			t.Fatalf("entry %s id must be unknown when the namespace changes", cidr)
		}
	}
}

func TestOriginInboundIPAllowlistEntriesPlanFillsDefaultsFromConfig(t *testing.T) {
	ctx := context.Background()
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	sch := inboundEntriesSchema(t, res)
	state := createInboundEntries(t, res, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}, inboundEntries{"198.51.100.7", "", true}))
	ids := inboundEntriesOf(t, state)

	written := func(entries ...originInboundIPAllowlistEntriesEntryModel) originInboundIPAllowlistEntriesModel {
		set, diags := types.SetValueFrom(ctx, originInboundIPAllowlistEntriesEntryType, entries)
		if diags.HasError() {
			t.Fatal(diags)
		}
		return originInboundIPAllowlistEntriesModel{ID: types.StringNull(), Namespace: types.StringValue("acme"), Entry: set, Etag: types.StringNull(), DeletionProtection: types.BoolNull()}
	}
	entry := func(cidr string, description types.String, enabled types.Bool) originInboundIPAllowlistEntriesEntryModel {
		return originInboundIPAllowlistEntriesEntryModel{ID: types.StringNull(), CIDR: types.StringValue(cidr), Description: description, Enabled: enabled}
	}
	config := written(entry("203.0.113.0/24", types.StringValue("Office"), types.BoolNull()), entry("198.51.100.7", types.StringNull(), types.BoolNull()))
	configValue := inboundEntriesConfig(t, res, config)
	// The proposed plan carries the prior ids with defaults the framework would have applied over configured values.
	proposed := written(
		originInboundIPAllowlistEntriesEntryModel{ID: ids["203.0.113.0/24"].ID, CIDR: types.StringValue("203.0.113.0/24"), Description: types.StringValue(""), Enabled: types.BoolUnknown()},
		originInboundIPAllowlistEntriesEntryModel{ID: types.StringUnknown(), CIDR: types.StringValue("198.51.100.7"), Description: types.StringUnknown(), Enabled: types.BoolUnknown()},
	)
	proposed.DeletionProtection = types.BoolValue(true)
	planValue := tfsdk.Plan{Schema: sch}
	if diags := planValue.Set(ctx, &proposed); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, Config: configValue, State: inboundEntriesState(t, res, *state)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got originInboundIPAllowlistEntriesModel
	if diags := resp.Plan.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	if !got.Entry.Equal(state.Entry) || !got.Etag.Equal(state.Etag) {
		t.Fatalf("plan entries = %v, want the configured description kept, omitted values defaulted, and no change", got.Entry)
	}

	create := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, Config: configValue, State: emptyState(ctx, sch)}, create)
	if create.Diagnostics.HasError() {
		t.Fatal(create.Diagnostics)
	}
	if diags := create.Plan.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	planned := inboundEntriesOf(t, &got)
	if planned["203.0.113.0/24"].Description.ValueString() != "Office" || !planned["198.51.100.7"].Enabled.ValueBool() || planned["198.51.100.7"].Description.ValueString() != "" || !planned["203.0.113.0/24"].ID.IsUnknown() || !got.Etag.IsUnknown() {
		t.Fatalf("create plan = %#v", planned)
	}
}

func TestOriginInboundIPAllowlistEntriesPlanRespectsDeletionProtection(t *testing.T) {
	ctx := context.Background()
	res := &originInboundIPAllowlistEntriesResource{}
	sch := inboundEntriesSchema(t, res)
	protected := sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true})
	protected.Etag = types.StringValue("e1")

	nullPlan := tfsdk.Plan{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	resp := &resource.ModifyPlanResponse{Plan: nullPlan}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan, State: inboundEntriesState(t, res, protected)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "deletion_protection") {
		t.Fatalf("diagnostics = %v, want the destroy plan refused while protected", resp.Diagnostics)
	}

	modify := func(state, plan originInboundIPAllowlistEntriesModel) *resource.ModifyPlanResponse {
		planValue := tfsdk.Plan{Schema: sch}
		if diags := planValue.Set(ctx, &plan); diags.HasError() {
			t.Fatal(diags)
		}
		resp := &resource.ModifyPlanResponse{Plan: planValue}
		res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, Config: inboundEntriesConfig(t, res, plan), State: inboundEntriesState(t, res, state)}, resp)
		return resp
	}
	moved := protected
	moved.Namespace = types.StringValue("rocket")
	if resp := modify(protected, moved); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "changing namespace") {
		t.Fatalf("diagnostics = %v, want the namespace change refused while protected", resp.Diagnostics)
	}
	unknown := protected
	unknown.Namespace = types.StringUnknown()
	if resp := modify(protected, unknown); !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "namespace is not known until apply") {
		t.Fatalf("diagnostics = %v, want an unknown namespace refused while protected", resp.Diagnostics)
	}
	unknownEntries := protected
	unknownEntries.Entry = types.SetUnknown(originInboundIPAllowlistEntriesEntryType)
	resp = modify(protected, unknownEntries)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unknown entries must plan: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistEntriesModel
	if diags := resp.Plan.Get(ctx, &got); diags.HasError() || !got.Etag.IsUnknown() {
		t.Fatalf("plan = %#v, %v, want an unknown etag", got, diags)
	}
}

func TestOriginInboundIPAllowlistEntriesImportState(t *testing.T) {
	mock := newInboundIPAllowlistMock(t)
	defer mock.Close()
	office := mock.add("acme", "203.0.113.0/24", "Office", true)
	mock.add("acme", "2001:db8::/32", "", false)

	ctx := context.Background()
	res := &originInboundIPAllowlistEntriesResource{client: mock.client()}
	sch := inboundEntriesSchema(t, res)
	resp := &resource.ImportStateResponse{State: emptyState(ctx, sch)}
	res.ImportState(ctx, resource.ImportStateRequest{ID: "acme"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import: %v", resp.Diagnostics)
	}
	var imported originInboundIPAllowlistEntriesModel
	if diags := resp.State.Get(ctx, &imported); diags.HasError() {
		t.Fatal(diags)
	}
	if imported.Namespace.ValueString() != "acme" || imported.ID.ValueString() != "acme" || !imported.DeletionProtection.ValueBool() || !imported.Entry.IsNull() {
		t.Fatalf("imported = %#v", imported)
	}
	got := readInboundEntries(t, res, imported)
	entries := inboundEntriesOf(t, got)
	if len(entries) != 2 || entries["203.0.113.0/24"].ID.ValueString() != office.ID || entries["2001:db8::/32"].Enabled.ValueBool() || got.Etag.ValueString() != mock.etag("acme") {
		t.Fatalf("read after import = %#v", entries)
	}

	same := modifyInboundEntriesPlan(t, res, *got, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}, inboundEntries{"2001:db8::/32", "", false}))
	if !same.Entry.Equal(got.Entry) || !same.Etag.Equal(got.Etag) {
		t.Fatal("a configuration matching the imported entries must plan no change")
	}

	for _, id := range []string{"", "acme:x", "acme/rocket", " acme"} {
		resp := &resource.ImportStateResponse{State: emptyState(ctx, sch)}
		res.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatalf("import %q must be rejected", id)
		}
	}
}

func TestValidateOriginInboundIPAllowlistEntries(t *testing.T) {
	ctx := context.Background()
	if err := validateOriginInboundIPAllowlistEntries(ctx, sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office", true}, inboundEntries{"2001:db8::1", "", true})); err != nil {
		t.Fatalf("valid model rejected: %v", err)
	}
	if err := validateOriginInboundIPAllowlistEntries(ctx, sampleInboundEntriesModel(t, inboundEntries{"10.0.0.1/8", "", true}, inboundEntries{"10.0.0.0/8", "", true})); err != nil {
		t.Fatalf("different spellings of one range must be distinct entries: %v", err)
	}
	if err := validateOriginInboundIPAllowlistEntries(ctx, sampleInboundEntriesModel(t)); err != nil {
		t.Fatalf("empty set rejected: %v", err)
	}
	unknown := sampleInboundEntriesModel(t)
	unknown.Entry = types.SetUnknown(originInboundIPAllowlistEntriesEntryType)
	if err := validateOriginInboundIPAllowlistEntries(ctx, unknown); err != nil {
		t.Fatalf("unknown set rejected: %v", err)
	}

	many := make([]inboundEntries, 0, maxOriginInboundIPAllowlistEntries+1)
	for i := 0; i <= maxOriginInboundIPAllowlistEntries; i++ {
		many = append(many, inboundEntries{fmt.Sprintf("10.%d.%d.0/24", i/256, i%256), "", true})
	}
	if err := validateOriginInboundIPAllowlistEntries(ctx, sampleInboundEntriesModel(t, many[:maxOriginInboundIPAllowlistEntries]...)); err != nil {
		t.Fatalf("%d entries rejected: %v", maxOriginInboundIPAllowlistEntries, err)
	}

	cases := []struct {
		name    string
		model   originInboundIPAllowlistEntriesModel
		wantErr string
	}{
		{"too many", sampleInboundEntriesModel(t, many...), "at most 1000"},
		{"ipv4 /0", sampleInboundEntriesModel(t, inboundEntries{"0.0.0.0/0", "", true}), "entire address space"},
		{"not an address", sampleInboundEntriesModel(t, inboundEntries{"office", "", true}), "must be an IPv4 or IPv6 address"},
		{"padded description", sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "Office ", true}), "description must not have"},
		{"long description", sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", strings.Repeat("a", 256), true}), "at most 255"},
	}
	for _, tc := range cases {
		if err := validateOriginInboundIPAllowlistEntries(ctx, tc.model); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
	badNamespace := sampleInboundEntriesModel(t)
	badNamespace.Namespace = types.StringValue("acme:x")
	if err := validateOriginInboundIPAllowlistEntries(ctx, badNamespace); err == nil || !strings.Contains(err.Error(), "namespace must not contain") {
		t.Fatalf("error = %v", err)
	}
}

func TestOriginInboundIPAllowlistEntriesSchema(t *testing.T) {
	ctx := context.Background()
	sch := inboundEntriesSchema(t, &originInboundIPAllowlistEntriesResource{})
	if diags := sch.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema implementation: %v", diags)
	}
	if len(sch.Attributes["namespace"].(schema.StringAttribute).PlanModifiers) != 1 {
		t.Fatal("namespace must require replace")
	}
	block := sch.Blocks["entry"].(schema.SetNestedBlock)
	enabled, description := block.NestedObject.Attributes["enabled"].(schema.BoolAttribute), block.NestedObject.Attributes["description"].(schema.StringAttribute)
	if enabled.Default != nil || description.Default != nil || !enabled.Optional || !enabled.Computed || !description.Optional || !description.Computed {
		t.Fatal("entry.enabled and entry.description must be optional and computed, defaulted by ModifyPlan; schema defaults inside set blocks overwrite configured values")
	}
	if !block.NestedObject.Attributes["id"].(schema.StringAttribute).Computed || !sch.Attributes["etag"].(schema.StringAttribute).Computed {
		t.Fatal("entry.id and etag must be computed")
	}
	if _, ok := sch.Attributes["enabled"]; ok {
		t.Fatal("the list-level enabled flag belongs to cursor_origin_inbound_ip_allowlist")
	}
	protection := sch.Attributes["deletion_protection"].(schema.BoolAttribute)
	if !protection.Optional || !protection.Computed || protection.Default == nil {
		t.Fatal("deletion_protection must be optional and default to true")
	}
}

func TestAccOriginInboundIPAllowlistEntries(t *testing.T) {
	namespace := os.Getenv("CURSOR_ACC_ORIGIN_NAMESPACE")
	token := os.Getenv(envToken)
	if os.Getenv("TF_ACC") == "" || namespace == "" || token == "" {
		t.Skipf("set TF_ACC, %s, and CURSOR_ACC_ORIGIN_NAMESPACE (a scratch team namespace whose allowlist entries may be replaced) to run against the live Origin API", envToken)
	}
	ctx := context.Background()
	endpoint := os.Getenv(envEndpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	client, err := newAPIClient(ctx, endpoint, token, "acc-test", adminKeys{})
	if err != nil {
		t.Fatalf("newAPIClient: %v", err)
	}
	res := &originInboundIPAllowlistEntriesResource{client: client}
	plan := sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "tf acc office", true}, inboundEntries{"2001:db8::/32", "tf acc vpn", false})
	plan.Namespace, plan.DeletionProtection = types.StringValue(namespace), types.BoolValue(false)
	resp := createInboundEntriesResponse(t, res, plan)
	if resp.Diagnostics.HasError() && strings.Contains(fmt.Sprint(resp.Diagnostics), "no Origin API route matches") {
		t.Skip("ReplaceInboundIpAllowlistEntries is not deployed on this Origin API yet")
	}
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	var state originInboundIPAllowlistEntriesModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatal(diags)
	}
	t.Cleanup(func() {
		resp := &resource.DeleteResponse{}
		res.Delete(ctx, resource.DeleteRequest{State: inboundEntriesState(t, res, state)}, resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("delete: %v", resp.Diagnostics)
		}
	})
	if got := readInboundEntries(t, res, state); !got.Entry.Equal(state.Entry) || got.Etag != state.Etag {
		t.Fatalf("read after create = %#v, want %#v", got, state)
	}
	next := sampleInboundEntriesModel(t, inboundEntries{"203.0.113.0/24", "tf acc office", false})
	next.Namespace, next.DeletionProtection = plan.Namespace, plan.DeletionProtection
	updated := updateInboundEntries(t, res, state, modifyInboundEntriesPlan(t, res, state, next))
	if len(inboundEntriesOf(t, updated)) != 1 || updated.Etag == state.Etag {
		t.Fatalf("update = %#v", updated)
	}
	state = *updated
}

type inboundEntries struct {
	cidr, description string
	enabled           bool
}

func sampleInboundEntriesModel(t *testing.T, entries ...inboundEntries) originInboundIPAllowlistEntriesModel {
	t.Helper()
	elements := make([]originInboundIPAllowlistEntriesEntryModel, 0, len(entries))
	for _, entry := range entries {
		elements = append(elements, originInboundIPAllowlistEntriesEntryModel{
			ID:          types.StringUnknown(),
			CIDR:        types.StringValue(entry.cidr),
			Description: types.StringValue(entry.description),
			Enabled:     types.BoolValue(entry.enabled),
		})
	}
	set, diags := types.SetValueFrom(context.Background(), originInboundIPAllowlistEntriesEntryType, elements)
	if diags.HasError() {
		t.Fatal(diags)
	}
	return originInboundIPAllowlistEntriesModel{
		ID:                 types.StringUnknown(),
		Namespace:          types.StringValue("acme"),
		Entry:              set,
		Etag:               types.StringUnknown(),
		DeletionProtection: types.BoolValue(true),
	}
}

func inboundEntriesOf(t *testing.T, model *originInboundIPAllowlistEntriesModel) map[string]originInboundIPAllowlistEntriesEntryModel {
	t.Helper()
	entries, diags := inboundEntriesFromSet(context.Background(), model.Entry)
	if diags.HasError() {
		t.Fatal(diags)
	}
	out := make(map[string]originInboundIPAllowlistEntriesEntryModel, len(entries))
	for _, entry := range entries {
		out[entry.CIDR.ValueString()] = entry
	}
	if len(out) != len(entries) {
		cidrs := make([]string, 0, len(entries))
		for _, entry := range entries {
			cidrs = append(cidrs, entry.CIDR.ValueString())
		}
		sort.Strings(cidrs)
		t.Fatalf("duplicate cidrs in %v", cidrs)
	}
	return out
}

func inboundEntriesSchema(t *testing.T, res *originInboundIPAllowlistEntriesResource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	res.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func inboundEntriesState(t *testing.T, res *originInboundIPAllowlistEntriesResource, model originInboundIPAllowlistEntriesModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: inboundEntriesSchema(t, res)}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}

// Config is the model as written: computed values null.
func inboundEntriesConfig(t *testing.T, res *originInboundIPAllowlistEntriesResource, model originInboundIPAllowlistEntriesModel) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	model.ID, model.Etag = types.StringNull(), types.StringNull()
	if !model.Entry.IsUnknown() && !model.Entry.IsNull() {
		entries, diags := inboundEntriesFromSet(ctx, model.Entry)
		if diags.HasError() {
			t.Fatal(diags)
		}
		for i := range entries {
			entries[i].ID = types.StringNull()
		}
		if model.Entry, diags = types.SetValueFrom(ctx, originInboundIPAllowlistEntriesEntryType, entries); diags.HasError() {
			t.Fatal(diags)
		}
	}
	state := inboundEntriesState(t, res, model)
	return tfsdk.Config{Schema: state.Schema, Raw: state.Raw}
}

func createInboundEntriesResponse(t *testing.T, res *originInboundIPAllowlistEntriesResource, plan originInboundIPAllowlistEntriesModel) *resource.CreateResponse {
	t.Helper()
	ctx := context.Background()
	planValue := tfsdk.Plan{Schema: inboundEntriesSchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Create(ctx, resource.CreateRequest{Plan: planValue}, resp)
	return resp
}

func createInboundEntries(t *testing.T, res *originInboundIPAllowlistEntriesResource, plan originInboundIPAllowlistEntriesModel) *originInboundIPAllowlistEntriesModel {
	t.Helper()
	resp := createInboundEntriesResponse(t, res, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create diagnostics: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistEntriesModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatal(diags)
	}
	return &got
}

func modifyInboundEntriesPlan(t *testing.T, res *originInboundIPAllowlistEntriesResource, state, plan originInboundIPAllowlistEntriesModel) originInboundIPAllowlistEntriesModel {
	t.Helper()
	ctx := context.Background()
	planValue := tfsdk.Plan{Schema: inboundEntriesSchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.ModifyPlanResponse{Plan: planValue}
	res.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: planValue, Config: inboundEntriesConfig(t, res, plan), State: inboundEntriesState(t, res, state)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("modify plan diagnostics: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistEntriesModel
	if diags := resp.Plan.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	return got
}

func updateInboundEntries(t *testing.T, res *originInboundIPAllowlistEntriesResource, state, plan originInboundIPAllowlistEntriesModel) *originInboundIPAllowlistEntriesModel {
	t.Helper()
	ctx := context.Background()
	planValue := tfsdk.Plan{Schema: inboundEntriesSchema(t, res)}
	if diags := planValue.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: planValue.Schema}}
	res.Update(ctx, resource.UpdateRequest{Plan: planValue, State: inboundEntriesState(t, res, state)}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update diagnostics: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistEntriesModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	return &got
}

func readInboundEntries(t *testing.T, res *originInboundIPAllowlistEntriesResource, model originInboundIPAllowlistEntriesModel) *originInboundIPAllowlistEntriesModel {
	t.Helper()
	ctx := context.Background()
	state := inboundEntriesState(t, res, model)
	resp := &resource.ReadResponse{State: state}
	res.Read(ctx, resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diagnostics: %v", resp.Diagnostics)
	}
	var got originInboundIPAllowlistEntriesModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatal(diags)
	}
	return &got
}
