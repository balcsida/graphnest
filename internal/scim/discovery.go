package scim

const (
	serviceProviderConfigSchema = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	resourceTypeSchema          = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	schemaSchema                = "urn:ietf:params:scim:schemas:core:2.0:Schema"
	maxDiscoveryResults         = 1000
)

type Supported struct {
	Supported bool `json:"supported"`
}

type FilterConfig struct {
	Supported  bool `json:"supported"`
	MaxResults int  `json:"maxResults"`
}

type BulkConfig struct {
	Supported      bool `json:"supported"`
	MaxOperations  int  `json:"maxOperations"`
	MaxPayloadSize int  `json:"maxPayloadSize"`
}

type AuthenticationScheme struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SpecURI     string `json:"specUri"`
	Primary     bool   `json:"primary"`
}

type ServiceProviderConfigDocument struct {
	Schemas               []string               `json:"schemas"`
	Patch                 Supported              `json:"patch"`
	Bulk                  BulkConfig             `json:"bulk"`
	Filter                FilterConfig           `json:"filter"`
	ChangePassword        Supported              `json:"changePassword"`
	Sort                  Supported              `json:"sort"`
	ETag                  Supported              `json:"etag"`
	AuthenticationSchemes []AuthenticationScheme `json:"authenticationSchemes"`
}

func ServiceProviderConfig(maxResults int) ServiceProviderConfigDocument {
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > maxDiscoveryResults {
		maxResults = maxDiscoveryResults
	}
	return ServiceProviderConfigDocument{
		Schemas:        []string{serviceProviderConfigSchema},
		Patch:          Supported{Supported: true},
		Filter:         FilterConfig{Supported: true, MaxResults: maxResults},
		Bulk:           BulkConfig{},
		ChangePassword: Supported{},
		Sort:           Supported{},
		ETag:           Supported{},
		AuthenticationSchemes: []AuthenticationScheme{{
			Type:        "oauthbearertoken",
			Name:        "Bearer Token",
			Description: "Authentication using the configured SCIM bearer token.",
			SpecURI:     "https://www.rfc-editor.org/rfc/rfc6750",
			Primary:     true,
		}},
	}
}

type ResourceTypeDocument struct {
	Schemas          []string          `json:"schemas"`
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Endpoint         string            `json:"endpoint"`
	Description      string            `json:"description"`
	Schema           string            `json:"schema"`
	SchemaExtensions []SchemaExtension `json:"schemaExtensions"`
}

type SchemaExtension struct {
	Schema   string `json:"schema"`
	Required bool   `json:"required"`
}

func ResourceTypes() ListResponse[ResourceTypeDocument] {
	resources := []ResourceTypeDocument{userResourceType(), groupResourceType()}
	return NewListResponse(resources, len(resources), 1)
}

func ResourceTypeByID(id string) (ResourceTypeDocument, bool) {
	switch id {
	case "User":
		return userResourceType(), true
	case "Group":
		return groupResourceType(), true
	default:
		return ResourceTypeDocument{}, false
	}
}

func userResourceType() ResourceTypeDocument {
	return ResourceTypeDocument{
		Schemas: []string{resourceTypeSchema}, ID: "User", Name: "User",
		Endpoint: "/Users", Description: "User Account", Schema: UserSchema,
		SchemaExtensions: []SchemaExtension{},
	}
}

func groupResourceType() ResourceTypeDocument {
	return ResourceTypeDocument{
		Schemas: []string{resourceTypeSchema}, ID: "Group", Name: "Group",
		Endpoint: "/Groups", Description: "Group", Schema: GroupSchema,
		SchemaExtensions: []SchemaExtension{},
	}
}

type SchemaDocument struct {
	Schemas     []string          `json:"schemas"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Attributes  []SchemaAttribute `json:"attributes"`
}

type SchemaAttribute struct {
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	MultiValued     bool              `json:"multiValued"`
	Description     string            `json:"description"`
	Required        bool              `json:"required"`
	CaseExact       bool              `json:"caseExact"`
	Mutability      string            `json:"mutability"`
	Returned        string            `json:"returned"`
	Uniqueness      string            `json:"uniqueness"`
	CanonicalValues []string          `json:"canonicalValues,omitempty"`
	SubAttributes   []SchemaAttribute `json:"subAttributes,omitempty"`
	ReferenceTypes  []string          `json:"referenceTypes,omitempty"`
}

func Schemas() ListResponse[SchemaDocument] {
	resources := []SchemaDocument{userSchemaDocument(), groupSchemaDocument()}
	return NewListResponse(resources, len(resources), 1)
}

func Schema(id string) (SchemaDocument, bool) {
	switch id {
	case UserSchema:
		return userSchemaDocument(), true
	case GroupSchema:
		return groupSchemaDocument(), true
	default:
		return SchemaDocument{}, false
	}
}

func userSchemaDocument() SchemaDocument {
	return SchemaDocument{
		Schemas: []string{schemaSchema}, ID: UserSchema, Name: "User", Description: "User Account",
		Attributes: []SchemaAttribute{
			attr("userName", "string", false, true, false, "readWrite", "default", "server"),
			attr("externalId", "string", false, true, true, "readWrite", "default", "server"),
			attr("displayName", "string", false, false, false, "readWrite", "default", "none"),
			attr("active", "boolean", false, false, false, "readWrite", "default", "none"),
			complexAttr("name", false, []SchemaAttribute{
				attr("formatted", "string", false, false, false, "readWrite", "default", "none"),
				attr("familyName", "string", false, false, false, "readWrite", "default", "none"),
				attr("givenName", "string", false, false, false, "readWrite", "default", "none"),
				attr("middleName", "string", false, false, false, "readWrite", "default", "none"),
				attr("honorificPrefix", "string", false, false, false, "readWrite", "default", "none"),
				attr("honorificSuffix", "string", false, false, false, "readWrite", "default", "none"),
			}),
			complexAttr("emails", true, []SchemaAttribute{
				attr("value", "string", false, true, false, "readWrite", "default", "none"),
				{Name: "type", Type: "string", MultiValued: false, Description: "Email type", Required: false, CaseExact: false, Mutability: "readWrite", Returned: "default", Uniqueness: "none", CanonicalValues: []string{"work", "home", "other"}},
				attr("primary", "boolean", false, false, false, "readWrite", "default", "none"),
			}),
		},
	}
}

func groupSchemaDocument() SchemaDocument {
	member := complexAttr("members", true, []SchemaAttribute{
		attr("value", "string", false, true, true, "readWrite", "default", "none"),
		{Name: "$ref", Type: "reference", MultiValued: false, Description: "Member URI", Required: false, CaseExact: true, Mutability: "readOnly", Returned: "default", Uniqueness: "none", ReferenceTypes: []string{"User"}},
		attr("display", "string", false, false, false, "readOnly", "default", "none"),
	})
	return SchemaDocument{
		Schemas: []string{schemaSchema}, ID: GroupSchema, Name: "Group", Description: "Group",
		Attributes: []SchemaAttribute{
			attr("displayName", "string", false, true, false, "readWrite", "default", "server"),
			attr("externalId", "string", false, false, true, "readWrite", "default", "server"),
			member,
		},
	}
}

func attr(name, kind string, multi, required, caseExact bool, mutability, returned, uniqueness string) SchemaAttribute {
	return SchemaAttribute{
		Name: name, Type: kind, MultiValued: multi, Description: name, Required: required,
		CaseExact: caseExact, Mutability: mutability, Returned: returned, Uniqueness: uniqueness,
	}
}

func complexAttr(name string, multi bool, sub []SchemaAttribute) SchemaAttribute {
	attribute := attr(name, "complex", multi, false, false, "readWrite", "default", "none")
	attribute.SubAttributes = sub
	return attribute
}
