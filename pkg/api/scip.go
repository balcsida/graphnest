package api

type SCIPNavigationRequest struct {
	RepositoryID   int64  `json:"repository_id"`
	Path           string `json:"path"`
	Commit         string `json:"commit,omitempty"`
	Line           int    `json:"line"`
	Character      int    `json:"character"`
	CharacterUTF8  *int   `json:"character_utf8,omitempty"`
	CharacterUTF16 *int   `json:"character_utf16,omitempty"`
	CharacterUTF32 *int   `json:"character_utf32,omitempty"`
	Operation      string `json:"operation"`
}

type SCIPNavigationResponse struct {
	Locations []SCIPLocation `json:"locations"`
	Truncated bool           `json:"truncated"`
}

type SCIPLocation struct {
	RepositoryID     int64  `json:"repository_id"`
	RepositoryName   string `json:"repository_name"`
	WebURL           string `json:"web_url"`
	Commit           string `json:"commit"`
	Path             string `json:"path"`
	Symbol           string `json:"symbol"`
	StartLine        int    `json:"start_line"`
	StartCharacter   int    `json:"start_character"`
	EndLine          int    `json:"end_line"`
	EndCharacter     int    `json:"end_character"`
	PositionEncoding string `json:"position_encoding"`
	Roles            int32  `json:"roles"`
	Approximate      bool   `json:"approximate"`
}

type RepositoryPackages struct {
	Provides  []string `json:"provides"`
	DependsOn []string `json:"depends_on"`
}

type DependencyRefreshResponse struct {
	Available bool `json:"available"`
	Packages  int  `json:"packages"`
}
