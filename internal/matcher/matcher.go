package matcher

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
)

// ProfileResolver looks up a PackingProfile by name from the informer cache.
type ProfileResolver struct {
	reader client.Reader
}

// NewProfileResolver creates a resolver backed by the given reader.
func NewProfileResolver(reader client.Reader) *ProfileResolver {
	return &ProfileResolver{reader: reader}
}

// Resolve returns the PackingProfile with the given name, or an error if not found.
func (r *ProfileResolver) Resolve(ctx context.Context, name string) (*v1alpha1.PackingProfile, error) {
	profile := &v1alpha1.PackingProfile{}
	err := r.reader.Get(ctx, client.ObjectKey{Name: name}, profile)
	if err != nil {
		return nil, fmt.Errorf("resolve profile %q: %w", name, err)
	}
	return profile, nil
}

// testScheme returns a scheme with PackingProfile registered.
// Exported for testing only within this package.
func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}
