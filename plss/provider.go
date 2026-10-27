package plss

import "context"

// Provider resolves cadastral descriptions and coordinates. Both Client (BLM
// HTTP service) and Pack (local data only) implement this interface.
type Provider interface {
	Lookup(context.Context, float64, float64) ([]Match, error)
	Find(context.Context, Description) ([]Match, error)
	FindID(context.Context, string, string) ([]Match, error)
}
