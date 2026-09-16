package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	principalFamilyUserEmail = "user_email"
	principalFamilyGroupName = "group_name"
)

var (
	originGrantUserAttrTypes      = map[string]attr.Type{"id": types.StringType}
	originGrantGroupAttrTypes     = map[string]attr.Type{"id": types.StringType}
	originGrantTeamGroupAttrTypes = map[string]attr.Type{"kind": types.StringType}
)

const (
	originGrantUserDescription      = "User ID (user_...)."
	originGrantGroupDescription     = "Cursor org group public ID (grp_...). The Organization Admin API returns it as publicId; its internal g_ id is not accepted."
	originGrantTeamGroupDescription = "members or admins: every member, or every admin, of the team that owns the resource. Rejected on user-owned resources."
	originGrantExactlyOne           = "Set exactly one of user_email, group_name, user, group, or team_group."
)

// Principal attributes of a grant resource. user and group hold the resolved ID when user_email or group_name is set.
type originGrantPrincipal struct {
	UserEmail types.String
	GroupName types.String
	User      types.Object
	Group     types.Object
	TeamGroup types.Object
}

type originGrantUserModel struct {
	ID types.String `tfsdk:"id"`
}

type originGrantGroupModel struct {
	ID types.String `tfsdk:"id"`
}

type originGrantTeamGroupModel struct {
	Kind types.String `tfsdk:"kind"`
}

type originGrantElementModel struct {
	ID         types.String               `tfsdk:"id"`
	Permission types.String               `tfsdk:"permission"`
	User       *originGrantUserModel      `tfsdk:"user"`
	Group      *originGrantGroupModel     `tfsdk:"group"`
	TeamGroup  *originGrantTeamGroupModel `tfsdk:"team_group"`
}

func originGrantPrincipalSchema() map[string]schema.Attribute {
	resolved := []planmodifier.Object{
		objectplanmodifier.RequiresReplaceIfConfigured(),
		objectplanmodifier.UseStateForUnknown(),
	}
	return map[string]schema.Attribute{
		"user_email": schema.StringAttribute{
			Optional:      true,
			Description:   "Email of a team member. Resolved to the member's user ID through the Team Admin API at plan and apply, which needs the provider team_api_key; the match is case-insensitive, ignores members removed from the team, and must be unique. " + originGrantExactlyOne + " Changing it replaces the grant.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"group_name": schema.StringAttribute{
			Optional:      true,
			Description:   "Name of a Cursor org group. Resolved to the group's publicId (grp_...) through the Organization Admin API at plan and apply, which needs the provider organization_api_key; the match is exact and must be unique. " + originGrantExactlyOne + " Changing it replaces the grant.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"user": schema.SingleNestedAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Grant to one user by ID. Computed from user_email when that is set. " + originGrantExactlyOne + " Changing the ID, including a changed resolution of user_email, replaces the grant.",
			PlanModifiers: resolved,
			Attributes: map[string]schema.Attribute{
				"id": schema.StringAttribute{
					Required:    true,
					Description: originGrantUserDescription,
				},
			},
		},
		"group": schema.SingleNestedAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Grant to a Cursor org group by public ID. Computed from group_name when that is set. " + originGrantExactlyOne + " Changing the ID, including a changed resolution of group_name, replaces the grant.",
			PlanModifiers: resolved,
			Attributes: map[string]schema.Attribute{
				"id": schema.StringAttribute{
					Required:    true,
					Description: originGrantGroupDescription,
				},
			},
		},
		"team_group": schema.SingleNestedAttribute{
			Optional:      true,
			Description:   "Grant to a team floor: every member or every admin of the owning team. " + originGrantExactlyOne + " Changing it replaces the grant.",
			PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
			Attributes: map[string]schema.Attribute{
				"kind": schema.StringAttribute{
					Required:    true,
					Description: originGrantTeamGroupDescription,
				},
			},
		},
	}
}

func originGrantsDataSchema(description string) dsschema.ListNestedAttribute {
	return dsschema.ListNestedAttribute{
		Computed:    true,
		Description: description,
		NestedObject: dsschema.NestedAttributeObject{
			Attributes: map[string]dsschema.Attribute{
				"id": dsschema.StringAttribute{
					Computed:    true,
					Description: "Composite grant ID in the form accepted by the matching grant resource import.",
				},
				"permission": dsschema.StringAttribute{
					Computed:    true,
					Description: "Permission preset. custom marks a grant whose policy is not a public preset.",
				},
				"user": dsschema.SingleNestedAttribute{
					Computed:    true,
					Description: "Set when the grant is for one user.",
					Attributes: map[string]dsschema.Attribute{
						"id": dsschema.StringAttribute{
							Computed:    true,
							Description: originGrantUserDescription,
						},
					},
				},
				"group": dsschema.SingleNestedAttribute{
					Computed:    true,
					Description: "Set when the grant is for a Cursor org group.",
					Attributes: map[string]dsschema.Attribute{
						"id": dsschema.StringAttribute{
							Computed:    true,
							Description: originGrantGroupDescription,
						},
					},
				},
				"team_group": dsschema.SingleNestedAttribute{
					Computed:    true,
					Description: "Set when the grant is a team floor.",
					Attributes: map[string]dsschema.Attribute{
						"kind": dsschema.StringAttribute{
							Computed:    true,
							Description: originGrantTeamGroupDescription,
						},
					},
				},
			},
		},
	}
}

func principalObject(attrTypes map[string]attr.Type, field, value string) types.Object {
	return types.ObjectValueMust(attrTypes, map[string]attr.Value{field: types.StringValue(value)})
}

func principalField(object types.Object, field string) types.String {
	if object.IsNull() {
		return types.StringNull()
	}
	if object.IsUnknown() {
		return types.StringUnknown()
	}
	value, ok := object.Attributes()[field].(types.String)
	if !ok {
		return types.StringNull()
	}
	return value
}

// family picks the principal source. Config must set exactly one attribute; plan and state may also carry the resolved user or group.
func (p originGrantPrincipal) family(config bool) (string, error) {
	set := 0
	family := ""
	mark := func(name string) {
		set++
		family = name
	}
	// Outside config, an unknown object is only an unresolved computed value, never a configured principal.
	objectSet := func(object types.Object) bool {
		return !object.IsNull() && (config || !object.IsUnknown())
	}
	if !p.UserEmail.IsNull() {
		mark(principalFamilyUserEmail)
	}
	if !p.GroupName.IsNull() {
		mark(principalFamilyGroupName)
	}
	if !config && set == 1 {
		return family, nil
	}
	if objectSet(p.User) {
		mark(originGrantKindUser)
	}
	if objectSet(p.Group) {
		mark(originGrantKindGroup)
	}
	if objectSet(p.TeamGroup) {
		mark(originGrantKindTeamGroup)
	}
	if set != 1 {
		return "", fmt.Errorf("set exactly one of user_email, group_name, user, group, or team_group")
	}
	return family, nil
}

func (p originGrantPrincipal) validate(config bool) error {
	family, err := p.family(config)
	if err != nil {
		return err
	}
	switch family {
	case principalFamilyUserEmail:
		if err := requireNonEmptyUnpadded(p.UserEmail, "user_email"); err != nil {
			return err
		}
		if !p.UserEmail.IsUnknown() && !strings.Contains(p.UserEmail.ValueString(), "@") {
			return fmt.Errorf("user_email %q is not an email address", p.UserEmail.ValueString())
		}
	case principalFamilyGroupName:
		if err := requireNonEmptyUnpadded(p.GroupName, "group_name"); err != nil {
			return err
		}
	case originGrantKindUser:
		return validatePrincipalID(principalField(p.User, "id"), "user.id", originUserIDPrefix)
	case originGrantKindGroup:
		return validatePrincipalID(principalField(p.Group, "id"), "group.id", originGroupIDPrefix)
	case originGrantKindTeamGroup:
		return requireKnownEnum(principalField(p.TeamGroup, "kind"), "team_group.kind", originTeamGroupMembers, originTeamGroupAdmins)
	}
	return nil
}

func validatePrincipalID(value types.String, name, prefix string) error {
	if err := requireNonEmptyUnpadded(value, name); err != nil {
		return err
	}
	if value.IsUnknown() {
		return nil
	}
	id := value.ValueString()
	if err := rejectBroadPrincipalID(id, name); err != nil {
		return err
	}
	if !strings.HasPrefix(id, prefix) {
		hint := ""
		if prefix == originGroupIDPrefix && strings.HasPrefix(id, "g_") {
			hint = "; g_ is the Organization Admin internal id, use the group's publicId or set group_name"
		}
		if prefix == originUserIDPrefix {
			hint = "; set user_email to resolve a user by email"
		}
		return fmt.Errorf("%s %q must start with %s%s", name, id, prefix, hint)
	}
	return nil
}

// key resolves the principal. With resolve, user_email and group_name are always looked up through
// client; otherwise a stored ID wins so apply matches the plan.
func (p originGrantPrincipal) key(ctx context.Context, client *apiClient, resolve bool) (originGrantKey, error) {
	if err := p.validate(false); err != nil {
		return originGrantKey{}, err
	}
	family, _ := p.family(false)
	switch family {
	case principalFamilyUserEmail:
		if id := principalField(p.User, "id"); !resolve && !id.IsNull() && !id.IsUnknown() && id.ValueString() != "" {
			return originGrantKey{kind: originGrantKindUser, value: id.ValueString()}, nil
		}
		if p.UserEmail.IsUnknown() {
			return originGrantKey{}, fmt.Errorf("user_email is incomplete")
		}
		if client == nil {
			return originGrantKey{}, fmt.Errorf("user_email %q has not been resolved to a user ID", p.UserEmail.ValueString())
		}
		id, err := client.resolveTeamMemberID(ctx, p.UserEmail.ValueString())
		if err != nil {
			return originGrantKey{}, fmt.Errorf("resolving user_email %q: %w", p.UserEmail.ValueString(), err)
		}
		return originGrantKey{kind: originGrantKindUser, value: id}, nil
	case principalFamilyGroupName:
		if id := principalField(p.Group, "id"); !resolve && !id.IsNull() && !id.IsUnknown() && id.ValueString() != "" {
			return originGrantKey{kind: originGrantKindGroup, value: id.ValueString()}, nil
		}
		if p.GroupName.IsUnknown() {
			return originGrantKey{}, fmt.Errorf("group_name is incomplete")
		}
		if client == nil {
			return originGrantKey{}, fmt.Errorf("group_name %q has not been resolved to a group ID", p.GroupName.ValueString())
		}
		id, err := client.resolveOrganizationGroupID(ctx, p.GroupName.ValueString())
		if err != nil {
			return originGrantKey{}, fmt.Errorf("resolving group_name %q: %w", p.GroupName.ValueString(), err)
		}
		return originGrantKey{kind: originGrantKindGroup, value: id}, nil
	case originGrantKindUser:
		return knownKey(originGrantKindUser, principalField(p.User, "id"), "user.id")
	case originGrantKindGroup:
		return knownKey(originGrantKindGroup, principalField(p.Group, "id"), "group.id")
	default:
		return knownKey(originGrantKindTeamGroup, principalField(p.TeamGroup, "kind"), "team_group.kind")
	}
}

func knownKey(kind string, value types.String, name string) (originGrantKey, error) {
	if value.IsUnknown() || value.IsNull() {
		return originGrantKey{}, fmt.Errorf("%s is incomplete", name)
	}
	return originGrantKey{kind: kind, value: value.ValueString()}, nil
}

// withKey stores the resolved principal and clears the objects the family does not use.
func (p originGrantPrincipal) withKey(key originGrantKey) originGrantPrincipal {
	p.User = types.ObjectNull(originGrantUserAttrTypes)
	p.Group = types.ObjectNull(originGrantGroupAttrTypes)
	p.TeamGroup = types.ObjectNull(originGrantTeamGroupAttrTypes)
	switch key.kind {
	case originGrantKindUser:
		p.User = principalObject(originGrantUserAttrTypes, "id", key.value)
	case originGrantKindGroup:
		p.Group = principalObject(originGrantGroupAttrTypes, "id", key.value)
	default:
		p.TeamGroup = principalObject(originGrantTeamGroupAttrTypes, "kind", key.value)
	}
	return p
}

// withUnknownKey clears the stored principal and marks the object that kind resolves into as unknown until apply.
func (p originGrantPrincipal) withUnknownKey(kind string) originGrantPrincipal {
	p.User = types.ObjectNull(originGrantUserAttrTypes)
	p.Group = types.ObjectNull(originGrantGroupAttrTypes)
	p.TeamGroup = types.ObjectNull(originGrantTeamGroupAttrTypes)
	switch kind {
	case originGrantKindUser:
		p.User = types.ObjectUnknown(originGrantUserAttrTypes)
	case originGrantKindGroup:
		p.Group = types.ObjectUnknown(originGrantGroupAttrTypes)
	}
	return p
}

// storedKey reads the principal already in state or plan without calling any API.
func (p originGrantPrincipal) storedKey() (originGrantKey, error) {
	return p.key(context.Background(), nil, false)
}

// withoutCopiedPrincipal drops the user or group UseStateForUnknown copied from prior so a family
// switch is not seen as two principals. team_group is not computed and is never copied.
func (p originGrantPrincipal) withoutCopiedPrincipal(prior originGrantPrincipal) originGrantPrincipal {
	family, err := prior.family(false)
	if err != nil {
		return p
	}
	switch family {
	case principalFamilyUserEmail, originGrantKindUser:
		p.User = types.ObjectNull(originGrantUserAttrTypes)
	case principalFamilyGroupName, originGrantKindGroup:
		p.Group = types.ObjectNull(originGrantGroupAttrTypes)
	}
	return p
}

// plan re-resolves the principal when it can and reports whether the resolved key differs from prior state.
// While user_email or group_name is unknown, the object it resolves into is unknown as well: UseStateForUnknown
// has copied the previous principal's ID into the plan, and apply trusts a known stored ID over resolving again.
func (p originGrantPrincipal) plan(ctx context.Context, client *apiClient, prior *originGrantPrincipal) (originGrantPrincipal, *originGrantKey, bool, error) {
	family, err := p.family(false)
	if err != nil && prior != nil {
		// UseStateForUnknown copies the prior user/group into a plan that already has another principal.
		stripped := p.withoutCopiedPrincipal(*prior)
		family, err = stripped.family(false)
		if err == nil {
			p = stripped
		}
	}
	if err != nil {
		// Family is still unknown (an unresolved ID object) or invalid; leave the plan unchanged.
		return p, nil, false, nil
	}
	switch {
	case family == principalFamilyUserEmail && p.UserEmail.IsUnknown():
		return p.withUnknownKey(originGrantKindUser), nil, false, nil
	case family == principalFamilyGroupName && p.GroupName.IsUnknown():
		return p.withUnknownKey(originGrantKindGroup), nil, false, nil
	}
	key, err := p.key(ctx, client, true)
	if err != nil {
		return p, nil, false, err
	}
	replace := false
	if prior != nil {
		if priorKey, err := prior.storedKey(); err == nil && priorKey != key {
			replace = true
		}
	}
	return p.withKey(key), &key, replace, nil
}

// importPrincipal builds the principal for an import ID, resolving user_email and group_name through client.
func importPrincipal(ctx context.Context, client *apiClient, kind, value string) (originGrantPrincipal, originGrantKey, error) {
	p := originGrantPrincipal{
		UserEmail: types.StringNull(),
		GroupName: types.StringNull(),
		User:      types.ObjectNull(originGrantUserAttrTypes),
		Group:     types.ObjectNull(originGrantGroupAttrTypes),
		TeamGroup: types.ObjectNull(originGrantTeamGroupAttrTypes),
	}
	switch kind {
	case principalFamilyUserEmail:
		p.UserEmail = types.StringValue(value)
	case principalFamilyGroupName:
		p.GroupName = types.StringValue(value)
	case originGrantKindUser:
		p.User = principalObject(originGrantUserAttrTypes, "id", value)
	case originGrantKindGroup:
		p.Group = principalObject(originGrantGroupAttrTypes, "id", value)
	default:
		p.TeamGroup = principalObject(originGrantTeamGroupAttrTypes, "kind", value)
	}
	key, err := p.key(ctx, client, true)
	if err != nil {
		return originGrantPrincipal{}, originGrantKey{}, err
	}
	return p.withKey(key), key, nil
}

// Owner and repository slugs also form the composite ID, so its separators are rejected.
func requireIDSafeSlug(value types.String, name string) error {
	if err := requireNonEmptyUnpadded(value, name); err != nil {
		return err
	}
	if !value.IsUnknown() && strings.ContainsAny(value.ValueString(), "/:") {
		return fmt.Errorf("%s must not contain / or :", name)
	}
	return nil
}

func validateOriginGrantPermission(value types.String, allowed ...string) error {
	if !value.IsNull() && !value.IsUnknown() && value.ValueString() == originPermissionCustom {
		return fmt.Errorf("permission %q is read-only; the Origin API reports it for a grant whose policy is not a public preset and rejects it on write. Set one of %s to replace that policy", originPermissionCustom, strings.Join(allowed, ", "))
	}
	return requireKnownEnum(value, "permission", allowed...)
}

func originGrantElementsFromWire(resource string, grants []originGrant, fromWire func(string) string) ([]originGrantElementModel, error) {
	out := make([]originGrantElementModel, 0, len(grants))
	for _, grant := range grants {
		key, err := originGrantKeyFromWire(grant)
		if err != nil {
			return nil, err
		}
		element := originGrantElementModel{
			ID:         types.StringValue(originGrantID(resource, key)),
			Permission: types.StringValue(fromWire(grant.Permission)),
		}
		switch key.kind {
		case originGrantKindUser:
			element.User = &originGrantUserModel{ID: types.StringValue(key.value)}
		case originGrantKindGroup:
			element.Group = &originGrantGroupModel{ID: types.StringValue(key.value)}
		default:
			element.TeamGroup = &originGrantTeamGroupModel{Kind: types.StringValue(key.value)}
		}
		out = append(out, element)
	}
	return out, nil
}
