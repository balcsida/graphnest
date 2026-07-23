package api

type SCIPNavigationRequest struct {
	RepositoryID int64  `json:"repository_id"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	Character    int    `json:"character"`
	Operation    string `json:"operation"`
}

type SCIPNavigationResponse struct {
	Locations []SCIPLocation `json:"locations"`
	Truncated bool           `json:"truncated"`
}

type SCIPLocation struct {
	RepositoryID     int64  `json:"repository_id"`
	RepositoryName   string `json:"repository_name"`
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
