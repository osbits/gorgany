package auth

import (
	"sync"
	"testing"
)

// TestDbSessionEntityIdentityIsRaceFree covers the two accessors the user-id and
// last-activity tests do not: the identifier and the creation stamp.
//
// The policy on this type is that mu guards every column, not the subset somebody has
// happened to see a race on — which is how GetLastActivity ended up reading a field
// SetLastActivity wrote under the lock. GetId in particular is called on nearly every code
// path that touches a session, so an unguarded read of it is the widest of the set even
// though nothing in the framework calls SetId today. Meaningful only under -race.
func TestDbSessionEntityIdentityIsRaceFree(t *testing.T) {
	entity := liveRow("sid", "user-1")

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			entity.SetId("sid")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = entity.GetId()
			_ = entity.GetCreatedAt()
		}
	}()

	wg.Wait()
}
