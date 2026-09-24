package graphprotocol

const (
	TypeKnowledgeRecorded = "recorded"
	TypeKnowledgeUnknown  = "unknown"
)

type TypeRelationsRequest struct {
	Scope      Scope  `json:"scope"`
	Occurrence string `json:"occurrence"`
}

// RelatedEntities groups repeated edge occurrences without losing evidence.
type RelatedEntities struct {
	Entity   Entity     `json:"entity"`
	Relation string     `json:"relation"`
	Edges    []Evidence `json:"edges"`
}

type TypeRelationsResponse struct {
	Status            string            `json:"status"`
	TypeKnowledge     string            `json:"type_knowledge"`
	Types             []RelatedEntities `json:"types,omitempty"`
	Users             []RelatedEntities `json:"users,omitempty"`
	Returners         []RelatedEntities `json:"returners,omitempty"`
	ReferencedTypes   []RelatedEntities `json:"referenced_types,omitempty"`
	RecordedOverrides []RelatedEntities `json:"recorded_overrides,omitempty"`
	Analysis          *AnalysisState    `json:"analysis,omitempty"`
	Generations       []Generation      `json:"generations"`
	Boundaries        []Boundary        `json:"boundaries,omitempty"`
	Partial           bool              `json:"partial"`
}

type TypeHierarchyRequest struct {
	Scope      Scope  `json:"scope"`
	Occurrence string `json:"occurrence"`
}

type HierarchyEntry struct {
	Entity         Entity   `json:"entity"`
	Depth          int      `json:"depth"`
	ParentID       string   `json:"parent_id"`
	Relation       string   `json:"relation"`
	Edge           Evidence `json:"edge"`
	Synthesized    bool     `json:"synthesized"`
	Via            string   `json:"via,omitempty"`
	RegisteredAt   string   `json:"registered_at,omitempty"`
	HiddenSubtypes int      `json:"hidden_subtypes"`
}

// HierarchyRows keeps every bounded query row while stating the viewer cap.
type HierarchyRows struct {
	Items     []HierarchyEntry `json:"items"`
	Total     int              `json:"total"`
	Shown     int              `json:"shown"`
	Truncated bool             `json:"truncated"`
}

type DerivedOverride struct {
	Member             Entity `json:"member"`
	BaseMember         Entity `json:"base_member"`
	BaseType           Entity `json:"base_type"`
	Relation           string `json:"relation"`
	SignatureUncertain bool   `json:"signature_uncertain"`
}

type TypeHierarchyResponse struct {
	Status             string            `json:"status"`
	Focus              Entity            `json:"focus"`
	Ancestors          HierarchyRows     `json:"ancestors"`
	Descendants        HierarchyRows     `json:"descendants"`
	DirectSubtypes     int               `json:"direct_subtypes"`
	DirectImplementers int               `json:"direct_implementers"`
	Bounded            bool              `json:"bounded"`
	Polymorphic        bool              `json:"polymorphic"`
	DerivedOverrides   []DerivedOverride `json:"derived_overrides,omitempty"`
	Analysis           *AnalysisState    `json:"analysis,omitempty"`
	Generations        []Generation      `json:"generations"`
	Boundaries         []Boundary        `json:"boundaries,omitempty"`
	Partial            bool              `json:"partial"`
}
