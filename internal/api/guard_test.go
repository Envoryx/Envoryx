package api

import (
	"net/http"
	"testing"

	"github.com/envoryx/envoryx/internal/auth"
)

// guard lets a confined principal through the routes of instanceRoutesForConfined and
// instanceRoutesForConfinedUsers without holding it to their level, and through those of
// routesForAnyProject with its level in any project. Only read routes belong in the first
// two; anything above read in any of them must be named here with its reason, so adding
// an operate or admin route to one of the lists fails until someone decides it.
var guardListsAboveRead = map[string]string{
	// The database browser checks every request's database against the principal's own
	// projects (dbToolProxy); the level comes from those projects.
	"routesForAnyProject GET /api/v1/dbtool": "operate",
	"routesForAnyProject /dbtool/":           "operate",
}

func TestGuardListsHoldOnlyReadRoutes(t *testing.T) {
	a := New(Deps{})
	a.Mount(http.NewServeMux(), func(h http.Handler) http.Handler { return h })
	kinds := map[string]string{}
	for _, r := range a.Routes() {
		kinds[r.Pattern] = r.Kind
	}
	seen := map[string]bool{}
	for name, list := range map[string]map[string]bool{
		"instanceRoutesForConfined":      instanceRoutesForConfined,
		"instanceRoutesForConfinedUsers": instanceRoutesForConfinedUsers,
		"routesForAnyProject":            routesForAnyProject,
	} {
		for pattern := range list {
			key := name + " " + pattern
			kind, ok := kinds[pattern]
			if !ok {
				t.Errorf("%s lists %q, which is no registered route", name, pattern)
				continue
			}
			want := string(auth.ScopeRead)
			if allowed, named := guardListsAboveRead[key]; named {
				want = allowed
				seen[key] = true
			}
			if kind != want {
				t.Errorf("%s lists %q, a %s route: guard lets confined principals through these lists by more than their level in the route's project. Keep them to read routes, or name the route in guardListsAboveRead with the reason it is safe", name, pattern, kind)
			}
		}
	}
	for key := range guardListsAboveRead {
		if !seen[key] {
			t.Errorf("guardListsAboveRead names %q, which is in none of the lists any more", key)
		}
	}
}
