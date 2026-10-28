package plss

import "errors"

// ErrPackCoverage means a structured description requests a state other than
// the downloaded pack's state. A missing polygon within that state instead
// returns ErrNotFound. Neither error causes a network request.
var ErrPackCoverage = errors.New("requested state is outside this offline pack's coverage")

// ErrNoGeometry means BLM published the requested cadastral record without a
// polygon. The record remains available for audit, but cannot yield a location.
var ErrNoGeometry = errors.New("published PLSS record has no polygon geometry")

// PackMetadata records the source and contents of an offline WGS84 pack.
// Counts counts spatial features and UnlocatedCounts counts source records
// without geometry, using township, section and intersected keys. Their sum is
// the source record count. CreatedAt is UTC RFC3339 and refers to the download,
// not the source's cadastral survey date.
type PackMetadata struct {
	Version         int            `json:"version"`
	State           string         `json:"state"`
	Source          string         `json:"source"`
	CreatedAt       string         `json:"created_at"`
	Counts          map[string]int `json:"counts"`
	UnlocatedCounts map[string]int `json:"unlocated_counts,omitempty"`
}

// UnlocatedRecord preserves a source record whose geometry was absent. It is
// never included in spatial lookup and is not assigned an invented coordinate.
type UnlocatedRecord struct {
	Layer      string         `json:"layer"`
	Attributes map[string]any `json:"attributes"`
	Reason     string         `json:"reason"`
}
