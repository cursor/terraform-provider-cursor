package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// jsonObjectType is a string type for JSON objects. Whitespace and key order
// do not count as a difference.
type jsonObjectType struct {
	basetypes.StringType
}

var (
	_ basetypes.StringTypable                    = jsonObjectType{}
	_ basetypes.StringValuableWithSemanticEquals = jsonObjectValue{}
)

func (t jsonObjectType) Equal(o attr.Type) bool {
	_, ok := o.(jsonObjectType)
	return ok
}

func (t jsonObjectType) String() string {
	return "jsonObjectType"
}

func (t jsonObjectType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return jsonObjectValue{StringValue: in}, nil
}

func (t jsonObjectType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	stringValue, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type of %T", attrValue)
	}
	valuable, diags := t.ValueFromString(ctx, stringValue)
	if diags.HasError() {
		return nil, fmt.Errorf("unexpected error converting StringValue to jsonObjectValue: %v", diags)
	}
	return valuable, nil
}

func (t jsonObjectType) ValueType(_ context.Context) attr.Value {
	return jsonObjectValue{}
}

type jsonObjectValue struct {
	basetypes.StringValue
}

func jsonObjectNull() jsonObjectValue {
	return jsonObjectValue{StringValue: basetypes.NewStringNull()}
}

func jsonObjectValueOf(value string) jsonObjectValue {
	return jsonObjectValue{StringValue: basetypes.NewStringValue(value)}
}

func (v jsonObjectValue) Type(_ context.Context) attr.Type {
	return jsonObjectType{}
}

func (v jsonObjectValue) Equal(o attr.Value) bool {
	other, ok := o.(jsonObjectValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

func (v jsonObjectValue) StringSemanticEquals(ctx context.Context, otherV basetypes.StringValuable) (bool, diag.Diagnostics) {
	otherString, diags := otherV.ToStringValue(ctx)
	if diags.HasError() {
		return false, diags
	}
	other, err := canonicalJSONObject(otherString.ValueString())
	if err != nil {
		return false, nil
	}
	current, err := canonicalJSONObject(v.ValueString())
	if err != nil {
		return false, nil
	}
	return current == other, nil
}

func jsonObjectRaw(value jsonObjectValue) (json.RawMessage, error) {
	if value.IsNull() || value.IsUnknown() {
		return nil, nil
	}
	raw := strings.TrimSpace(value.ValueString())
	if raw == "" || raw == "null" {
		return nil, nil
	}
	canonical, err := canonicalJSONObject(raw)
	if err != nil {
		return nil, err
	}
	if canonical == "{}" {
		return nil, nil
	}
	return json.RawMessage(canonical), nil
}

// jsonObjectFromRaw maps an API JSON object onto an optional, non-computed attribute.
// A known prior value is kept, including null, so a server-supplied object is not stored.
// When there is no prior, the API value is stored, or null when it is empty.
func jsonObjectFromRaw(raw json.RawMessage, prior jsonObjectValue, havePrior bool) (jsonObjectValue, error) {
	if havePrior && !prior.IsUnknown() {
		if prior.IsNull() {
			return jsonObjectNull(), nil
		}
		return prior, nil
	}
	canonical, err := canonicalJSONObject(string(raw))
	if err != nil {
		return jsonObjectValue{}, err
	}
	if canonical == "{}" {
		return jsonObjectNull(), nil
	}
	return jsonObjectValueOf(canonical), nil
}

func canonicalJSONObject(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "{}", nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", fmt.Errorf("parameters must be a JSON object: %w", err)
	}
	if dec.More() {
		return "", fmt.Errorf("parameters must be a single JSON object")
	}
	if _, ok := value.(map[string]any); !ok {
		return "", fmt.Errorf("parameters must be a JSON object")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encoding parameters: %w", err)
	}
	return string(encoded), nil
}
