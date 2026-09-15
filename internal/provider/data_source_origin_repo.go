package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type originRepoDataSource struct {
	client *apiClient
}

type originRepoDataSourceModel struct {
	ID                  types.String           `tfsdk:"id"`
	Owner               types.String           `tfsdk:"owner"`
	Name                types.String           `tfsdk:"name"`
	FullName            types.String           `tfsdk:"full_name"`
	OwnerID             types.String           `tfsdk:"owner_id"`
	OwnerType           types.String           `tfsdk:"owner_type"`
	DefaultBranch       types.String           `tfsdk:"default_branch"`
	Visibility          types.String           `tfsdk:"visibility"`
	CloneURL            types.String           `tfsdk:"clone_url"`
	CreatedAt           types.String           `tfsdk:"created_at"`
	UpdatedAt           types.String           `tfsdk:"updated_at"`
	PushedAt            types.String           `tfsdk:"pushed_at"`
	AllowMergeCommit    types.Bool             `tfsdk:"allow_merge_commit"`
	AllowSquashMerge    types.Bool             `tfsdk:"allow_squash_merge"`
	DeleteBranchOnMerge types.Bool             `tfsdk:"delete_branch_on_merge"`
	Mirror              *originRepoMirrorModel `tfsdk:"mirror"`
}

type originRepoMirrorModel struct {
	Source   types.String `tfsdk:"source"`
	SourceID types.String `tfsdk:"source_id"`
	Status   types.String `tfsdk:"status"`
}

func NewOriginRepoDataSource() datasource.DataSource {
	return &originRepoDataSource{}
}

func (d *originRepoDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_origin_repo"
}

func (d *originRepoDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an Origin repository from the public Origin API. Look up by owner and name, or by the repository's stable ID. The public API does not support updating or deleting repositories, so this is a data source rather than a managed resource. Uses the provider auth token.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Stable Origin repository ID (for example repo_01...). Set this to look up the repository without owner and name. When looking up by owner and name, the ID is read from the API.",
			},
			"owner": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Owner slug, the namespace the repository belongs to. Required with name unless id is set. Compared case-insensitively with the stored slug.",
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Repository name within the owner. Required with owner unless id is set. Compared case-insensitively with the stored name.",
			},
			"full_name": schema.StringAttribute{
				Computed:    true,
				Description: "Combined owner and repository name, such as acme/rocket.",
			},
			"owner_id": schema.StringAttribute{
				Computed:    true,
				Description: "Stable Origin owner identifier.",
			},
			"owner_type": schema.StringAttribute{
				Computed:    true,
				Description: "Owner namespace type: team or user. Null when the API omits it.",
			},
			"default_branch": schema.StringAttribute{
				Computed:    true,
				Description: "Repository default branch name.",
			},
			"visibility": schema.StringAttribute{
				Computed:    true,
				Description: "Repository visibility: internal or private.",
			},
			"clone_url": schema.StringAttribute{
				Computed:    true,
				Description: "HTTPS clone URL. Output-only; the path shape is not guaranteed.",
			},
			"created_at": schema.StringAttribute{
				Computed:    true,
				Description: "RFC 3339 repository creation timestamp.",
			},
			"updated_at": schema.StringAttribute{
				Computed:    true,
				Description: "RFC 3339 repository update timestamp.",
			},
			"pushed_at": schema.StringAttribute{
				Computed:    true,
				Description: "RFC 3339 timestamp of the most recent push. Null until the first push.",
			},
			"allow_merge_commit": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether pull requests may land as merge commits.",
			},
			"allow_squash_merge": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether pull requests may land as squash merges.",
			},
			"delete_branch_on_merge": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the head branch is deleted automatically on merge.",
			},
			"mirror": schema.SingleNestedAttribute{
				Computed:    true,
				Description: "Mirror metadata. Null for a native repository and before a mirror's initial sync is ready.",
				Attributes: map[string]schema.Attribute{
					"source": schema.StringAttribute{
						Computed:    true,
						Description: "Mirror source. Currently github.",
					},
					"source_id": schema.StringAttribute{
						Computed:    true,
						Description: "Opaque repository identifier assigned by the source.",
					},
					"status": schema.StringAttribute{
						Computed:    true,
						Description: "Effective mirror direction during a transition: inbound or outbound.",
					},
				},
			},
		},
	}
}

func (d *originRepoDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*apiClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *apiClient, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *originRepoDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config originRepoDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Origin API client is unavailable.")
		return
	}

	lookup, err := resolveOriginRepoLookup(config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Origin repository lookup", err.Error())
		return
	}

	repo, err := d.client.getOriginRepo(ctx, lookup.owner, lookup.name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Origin repository", err.Error())
		return
	}
	if err := lookup.matches(repo); err != nil {
		resp.Diagnostics.AddError("Origin repository does not match lookup", err.Error())
		return
	}

	state := originRepoToModel(repo, lookup)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

type originRepoLookup struct {
	owner      string
	name       string
	configured originRepoIdentity
}

type originRepoIdentity struct {
	id    string
	owner string
	name  string
}

func resolveOriginRepoLookup(config originRepoDataSourceModel) (originRepoLookup, error) {
	id, hasID := configuredString(config.ID)
	owner, hasOwner := configuredString(config.Owner)
	name, hasName := configuredString(config.Name)

	if hasOwner != hasName {
		return originRepoLookup{}, fmt.Errorf("set both owner and name, or set id")
	}
	if !hasID && !hasOwner {
		return originRepoLookup{}, fmt.Errorf("set owner and name, or set id")
	}
	if hasOwner && owner == originRepoIDOwner {
		return originRepoLookup{}, fmt.Errorf("owner %q is reserved; look up by id instead", originRepoIDOwner)
	}

	lookup := originRepoLookup{
		configured: originRepoIdentity{
			id:    id,
			owner: owner,
			name:  name,
		},
	}
	if hasID {
		lookup.owner = originRepoIDOwner
		lookup.name = id
		return lookup, nil
	}
	lookup.owner = owner
	lookup.name = name
	return lookup, nil
}

func (l originRepoLookup) matches(repo *originRepo) error {
	if repo == nil || repo.Owner == nil {
		return fmt.Errorf("Origin API returned an empty repository")
	}
	if l.configured.id != "" && repo.ID != l.configured.id {
		return fmt.Errorf("repository id %q does not match requested id %q", repo.ID, l.configured.id)
	}
	if l.configured.owner != "" && !strings.EqualFold(repo.Owner.Slug, l.configured.owner) {
		return fmt.Errorf("repository owner %q does not match requested owner %q", repo.Owner.Slug, l.configured.owner)
	}
	if l.configured.name != "" && !strings.EqualFold(repo.Name, l.configured.name) {
		return fmt.Errorf("repository name %q does not match requested name %q", repo.Name, l.configured.name)
	}
	return nil
}

func originRepoToModel(repo *originRepo, lookup originRepoLookup) originRepoDataSourceModel {
	owner := repo.Owner.Slug
	name := repo.Name
	// Keep the configured casing when it refers to the same repository so a
	// case-insensitive lookup does not plan a difference on every refresh.
	if lookup.configured.owner != "" && strings.EqualFold(owner, lookup.configured.owner) {
		owner = lookup.configured.owner
	}
	if lookup.configured.name != "" && strings.EqualFold(name, lookup.configured.name) {
		name = lookup.configured.name
	}
	state := originRepoDataSourceModel{
		ID:                  types.StringValue(repo.ID),
		Owner:               types.StringValue(owner),
		Name:                types.StringValue(name),
		FullName:            stringOrNull(repo.FullName),
		OwnerID:             stringOrNull(repo.Owner.ID),
		OwnerType:           stringOrNull(repo.Owner.Type),
		DefaultBranch:       stringOrNull(repo.DefaultBranch),
		Visibility:          stringOrNull(repo.Visibility),
		CloneURL:            stringOrNull(repo.CloneURL),
		CreatedAt:           stringOrNull(repo.CreatedAt),
		UpdatedAt:           stringOrNull(repo.UpdatedAt),
		PushedAt:            stringOrNull(repo.PushedAt),
		AllowMergeCommit:    boolPtrOrNull(repo.AllowMergeCommit),
		AllowSquashMerge:    boolPtrOrNull(repo.AllowSquashMerge),
		DeleteBranchOnMerge: boolPtrOrNull(repo.DeleteBranchOnMerge),
	}
	if repo.Mirror != nil {
		state.Mirror = &originRepoMirrorModel{
			Source:   stringOrNull(repo.Mirror.Source),
			SourceID: stringOrNull(repo.Mirror.SourceID),
			Status:   stringOrNull(repo.Mirror.Status),
		}
	}
	return state
}

func configuredString(value types.String) (string, bool) {
	if value.IsNull() || value.IsUnknown() {
		return "", false
	}
	trimmed := strings.TrimSpace(value.ValueString())
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

func boolPtrOrNull(value *bool) types.Bool {
	if value == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*value)
}
