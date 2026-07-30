package scim

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestDiscoveryDocuments(t *testing.T) {
	config := ServiceProviderConfig(5000)
	if !config.Patch.Supported || !config.Filter.Supported || config.Filter.MaxResults != 1000 ||
		config.Bulk.Supported || config.Sort.Supported || config.ETag.Supported || config.ChangePassword.Supported {
		t.Fatalf("config=%#v", config)
	}
	if len(config.AuthenticationSchemes) != 1 || config.AuthenticationSchemes[0].Type != "oauthbearertoken" {
		t.Fatalf("authenticationSchemes=%#v", config.AuthenticationSchemes)
	}

	resourceTypes := ResourceTypes()
	if resourceTypes.TotalResults != 2 ||
		resourceTypes.Resources[0].Endpoint != "/Users" || resourceTypes.Resources[0].Schema != UserSchema ||
		resourceTypes.Resources[1].Endpoint != "/Groups" || resourceTypes.Resources[1].Schema != GroupSchema {
		t.Fatalf("resourceTypes=%#v", resourceTypes)
	}

	schemas := Schemas()
	if schemas.TotalResults != 2 || schemas.Resources[0].ID != UserSchema || schemas.Resources[1].ID != GroupSchema {
		t.Fatalf("schemas=%#v", schemas)
	}
	for _, document := range []any{config, resourceTypes, schemas} {
		data, err := json.Marshal(document)
		if err != nil || !json.Valid(data) {
			t.Fatalf("json=%s err=%v", data, err)
		}
	}
	assertJSONSnapshot(t, config, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"],"patch":{"supported":true},"bulk":{"supported":false,"maxOperations":0,"maxPayloadSize":0},"filter":{"supported":true,"maxResults":1000},"changePassword":{"supported":false},"sort":{"supported":false},"etag":{"supported":false},"authenticationSchemes":[{"type":"oauthbearertoken","name":"Bearer Token","description":"Authentication using the configured SCIM bearer token.","specUri":"https://www.rfc-editor.org/rfc/rfc6750","primary":true}]}`)
	assertJSONSnapshot(t, resourceTypes, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:ListResponse"],"totalResults":2,"startIndex":1,"itemsPerPage":2,"Resources":[{"schemas":["urn:ietf:params:scim:schemas:core:2.0:ResourceType"],"id":"User","name":"User","endpoint":"/Users","description":"User Account","schema":"urn:ietf:params:scim:schemas:core:2.0:User","schemaExtensions":[]},{"schemas":["urn:ietf:params:scim:schemas:core:2.0:ResourceType"],"id":"Group","name":"Group","endpoint":"/Groups","description":"Group","schema":"urn:ietf:params:scim:schemas:core:2.0:Group","schemaExtensions":[]}]}`)
	if got := schemaSnapshot(schemas.Resources); got != "User:userName:string:true:readWrite,externalId:string:true:readWrite,displayName:string:false:readWrite,active:boolean:false:readWrite,name:complex:false:readWrite[formatted,familyName,givenName],emails:complex:false:readWrite[value,type,primary]|Group:displayName:string:true:readWrite,externalId:string:false:readWrite,members:complex:false:readWrite[value,$ref,display]" {
		t.Fatalf("schema snapshot=%s", got)
	}
}

func TestDiscoveryDocumentsMatchSupportedSurface(t *testing.T) {
	user, ok := Schema(UserSchema)
	if !ok || attribute(user.Attributes, "password") != nil ||
		attribute(user.Attributes, "roles") != nil || attribute(user.Attributes, "entitlements") != nil {
		t.Fatalf("user schema=%#v ok=%v", user, ok)
	}
	group, ok := ResourceTypeByID("Group")
	if !ok || group.Endpoint != "/Groups" || len(group.SchemaExtensions) != 0 {
		t.Fatalf("group resource type=%#v ok=%v", group, ok)
	}
	if _, ok := Schema("urn:example:extension"); ok {
		t.Fatal("unsupported schema was advertised")
	}
}

func attribute(attributes []SchemaAttribute, name string) *SchemaAttribute {
	for i := range attributes {
		if attributes[i].Name == name {
			return &attributes[i]
		}
	}
	return nil
}

func assertJSONSnapshot(t *testing.T, value any, want string) {
	t.Helper()
	got, err := json.Marshal(value)
	if err != nil || string(got) != want {
		t.Fatalf("json=%s err=%v", got, err)
	}
}

func schemaSnapshot(documents []SchemaDocument) string {
	var result []string
	for _, document := range documents {
		var attributes []string
		for _, attribute := range document.Attributes {
			entry := attribute.Name + ":" + attribute.Type + ":" + strconv.FormatBool(attribute.Required) + ":" + attribute.Mutability
			if len(attribute.SubAttributes) > 0 {
				var sub []string
				for _, attribute := range attribute.SubAttributes {
					sub = append(sub, attribute.Name)
				}
				entry += "[" + strings.Join(sub, ",") + "]"
			}
			attributes = append(attributes, entry)
		}
		result = append(result, document.Name+":"+strings.Join(attributes, ","))
	}
	return strings.Join(result, "|")
}
