package egress

import (
	"sort"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Capability is the unforgeable proof that its holder asked the registry
// for permission to write on a class. Its only field is unexported and
// its only constructor is Registry.Capability, so no caller outside this
// package can build a non-zero one, and the zero value names no class and
// is refused.
//
// It is a value type on purpose: a holder can copy it freely, and there
// is nothing on it to mutate into a different class.
type Capability struct {
	class EgressClass
}

// Class reports the class the capability admits. The zero Capability
// reports the empty class, which no registry entry can hold.
func (c Capability) Class() EgressClass { return c.class }

// Registry holds the registered classes. The zero value is not usable;
// build one with NewRegistry. A Registry is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	classes map[EgressClass]InterceptConfig
}

// NewRegistry returns an empty registry. An empty registry admits
// nothing: every class is unknown until a registrant registers it.
func NewRegistry() *Registry {
	return &Registry{classes: make(map[EgressClass]InterceptConfig)}
}

// Register records cfg for class. A duplicate registration is an error
// rather than an overwrite: two registrants disagreeing about one class's
// policy is a defect, and letting the last init win would make which
// policy applies depend on link order.
func (r *Registry) Register(class EgressClass, cfg InterceptConfig) error {
	if class == "" {
		return cascade.New(cascade.KindInvalidInput, "egress: a class identifier must not be empty")
	}
	if cfg.Owner == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"egress: class %q must name the ticket that owns it", string(class))
	}
	for _, tier := range cfg.AllowedTiers {
		if tier.Resolve() != tier {
			return cascade.Newf(cascade.KindInvalidInput,
				"egress: class %q lists unknown tier %q", string(class), string(tier))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.classes[class]; exists {
		return cascade.Wrapf(cascade.KindConflict, ErrDuplicateClass,
			"egress: class %q is already registered", string(class))
	}
	r.classes[class] = cfg
	return nil
}

// MustRegister registers class at package init and panics on error. It is
// used only by classes.go, where a failed registration means the process
// has no policy for a path that is about to be written to; continuing
// past that is worse than not starting.
func (r *Registry) MustRegister(class EgressClass, cfg InterceptConfig) {
	if err := r.Register(class, cfg); err != nil {
		panic(err)
	}
}

// Lookup returns the class's config. ok is false for an unregistered
// class.
func (r *Registry) Lookup(class EgressClass) (cfg InterceptConfig, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok = r.classes[class]
	return cfg, ok
}

// Classes returns every registered class, sorted. Sorted because a
// diagnostic listing that reorders between runs is a diff nobody can read.
func (r *Registry) Classes() []EgressClass {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]EgressClass, 0, len(r.classes))
	for class := range r.classes {
		out = append(out, class)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Capability issues the capability for class. An unregistered class
// returns ErrUnknownClass and a zero Capability; a disabled class returns
// ErrClassDisabled, so a disabled path cannot even acquire the token, let
// alone use it.
func (r *Registry) Capability(class EgressClass) (Capability, error) {
	cfg, ok := r.Lookup(class)
	if !ok {
		return Capability{}, unknownClass(class)
	}
	if !cfg.Enabled {
		return Capability{}, disabledClass(class, cfg.Owner)
	}
	return Capability{class: class}, nil
}
