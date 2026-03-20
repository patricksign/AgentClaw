package escalation

// ResolvedQuerier abstracts the resolved-error-pattern store.
// Implementations live in the infrastructure layer.
type ResolvedQuerier interface {
	Search(question, role string) ([]CachedAnswer, error)
	Save(question, answer, role string) error
}

// CachedAnswer is a previously resolved question/answer pair.
type CachedAnswer struct {
	Summary         string `json:"summary"`
	OccurrenceCount int    `json:"occurrence_count"`
}

// Cache wraps a ResolvedQuerier with a minimum-occurrence threshold.
type Cache struct {
	resolved ResolvedQuerier
}

// NewCache creates a cache backed by the given querier.
func NewCache(resolved ResolvedQuerier) *Cache {
	return &Cache{resolved: resolved}
}

// Check returns a cached answer only if the question has been seen at least twice.
func (c *Cache) Check(question, role string) (answer string, hit bool) {
	if c.resolved == nil {
		return "", false
	}
	results, err := c.resolved.Search(question, role)
	if err != nil || len(results) == 0 {
		return "", false
	}
	for _, r := range results {
		if r.OccurrenceCount >= 2 {
			return r.Summary, true
		}
	}
	return "", false
}

// Save persists a question/answer pair for future cache lookups.
func (c *Cache) Save(question, answer, role string) {
	if c.resolved == nil {
		return
	}
	_ = c.resolved.Save(question, answer, role)
}
