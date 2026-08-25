package enrichment

import "github.com/balcsida/graphnest/internal/graphartifact"

const ProtocolVersion = 1

type Response struct {
	Version  int                    `json:"version"`
	Artifact graphartifact.Artifact `json:"artifact"`
}
