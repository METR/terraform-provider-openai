package apikey_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	provider "github.com/openai/terraform-provider-openai/internal/provider"
)

func dynamicValue(t *testing.T, typ tftypes.Object, set map[string]tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	fields := make(map[string]tftypes.Value)
	for name, fieldType := range typ.AttributeTypes {
		fields[name] = tftypes.NewValue(fieldType, nil)
	}
	for name, value := range set {
		fields[name] = value
	}
	value, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, fields))
	if err != nil {
		t.Fatal(err)
	}
	return &value
}

func failOnErrors(t *testing.T, diagnostics []*tfprotov6.Diagnostic) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("%s: %s", diagnostic.Summary, diagnostic.Detail)
		}
	}
}

// readKeys configures the provider against baseURL and reads typeName, returning
// each key's attributes as strings ("" for null).
func readKeys(t *testing.T, baseURL string, typeName string, config map[string]tftypes.Value) []map[string]string {
	t.Helper()
	ctx := context.Background()
	server := providerserver.NewProtocol6(provider.New("test")())()
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	providerType := schemas.Provider.ValueType().(tftypes.Object)
	configured, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: dynamicValue(t, providerType, map[string]tftypes.Value{
		"admin_api_key": tftypes.NewValue(tftypes.String, "test"),
		"base_url":      tftypes.NewValue(tftypes.String, baseURL+"/v1"),
	})})
	if err != nil {
		t.Fatal(err)
	}
	failOnErrors(t, configured.Diagnostics)

	typ := schemas.DataSourceSchemas[typeName].ValueType().(tftypes.Object)
	read, err := server.ReadDataSource(ctx, &tfprotov6.ReadDataSourceRequest{TypeName: typeName, Config: dynamicValue(t, typ, config)})
	if err != nil {
		t.Fatal(err)
	}
	failOnErrors(t, read.Diagnostics)
	state, err := read.State.Unmarshal(typ)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]tftypes.Value
	if err := state.As(&fields); err != nil {
		t.Fatal(err)
	}
	var keys []tftypes.Value
	if err := fields["api_keys"].As(&keys); err != nil {
		t.Fatal(err)
	}
	result := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		var attributes map[string]tftypes.Value
		if err := key.As(&attributes); err != nil {
			t.Fatal(err)
		}
		flat := map[string]string{}
		for name, value := range attributes {
			if value.IsNull() {
				continue
			}
			if value.Type().Is(tftypes.String) {
				var s string
				_ = value.As(&s)
				flat[name] = s
			}
		}
		result = append(result, flat)
	}
	return result
}

func TestAdminAPIKeysReadsEveryPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/organization/admin_api_keys" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after") {
		case "":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"organization.admin_api_key","id":"key_a","name":"first","created_at":1,"last_used_at":2,"owner":{"type":"user","id":"user_a","name":"Ada"}}],"first_id":"key_a","last_id":"key_a","has_more":true}`))
		case "key_a":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"organization.admin_api_key","id":"key_b","name":"second","created_at":3,"last_used_at":null,"owner":{"type":"service_account","id":"user_b","name":"svc"}}],"first_id":"key_b","last_id":"key_b","has_more":false}`))
		default:
			http.Error(w, "unexpected cursor", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	keys := readKeys(t, server.URL, "openai_admin_api_keys", nil)
	if len(keys) != 2 || keys[0]["id"] != "key_a" || keys[1]["id"] != "key_b" {
		t.Fatalf("keys = %v", keys)
	}
	if keys[0]["owner_type"] != "user" || keys[0]["owner_id"] != "user_a" || keys[1]["owner_name"] != "svc" {
		t.Fatalf("owners = %v", keys)
	}
}

func TestProjectAPIKeysReadsNestedOwners(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/organization/projects/proj_1/api_keys" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"object":"organization.project.api_key","id":"key_u","name":"mine","created_at":1,"owner":{"type":"user","user":{"id":"user_u","name":"Ada"}}},
			{"object":"organization.project.api_key","id":"key_s","name":"bot","created_at":2,"owner":{"type":"service_account","service_account":{"id":"svc_s","name":"bot"}}}
		],"has_more":false}`))
	}))
	defer server.Close()

	keys := readKeys(t, server.URL, "openai_project_api_keys", map[string]tftypes.Value{
		"project_id": tftypes.NewValue(tftypes.String, "proj_1"),
	})
	if len(keys) != 2 {
		t.Fatalf("keys = %v", keys)
	}
	if keys[0]["owner_type"] != "user" || keys[0]["owner_id"] != "user_u" || keys[0]["owner_name"] != "Ada" {
		t.Fatalf("user-owned key = %v", keys[0])
	}
	if keys[1]["owner_type"] != "service_account" || keys[1]["owner_id"] != "svc_s" || keys[1]["owner_name"] != "bot" {
		t.Fatalf("service-account key = %v", keys[1])
	}
}
