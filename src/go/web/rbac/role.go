package rbac

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"phenix/api/config"
	v1 "phenix/types/version/v1"

	"github.com/mitchellh/mapstructure"
)

var NAME_TO_ROLE_CONFIG = map[string]string{
	"Global Admin":      "global-admin",
	"Global Viewer":     "global-viewer",
	"Experiment Admin":  "experiment-admin",
	"Experiment User":   "experiment-user",
	"Experiment Viewer": "experiment-viewer",
	"VM Viewer":         "vm-viewer",
}

type Role struct {
	Spec *v1.RoleSpec

	mappedPolicies map[string][]Policy
}

func RoleFromConfig(name string) (*Role, error) {
	if rname, ok := NAME_TO_ROLE_CONFIG[name]; ok {
		name = rname
	}

	c, err := config.Get("role/" + name)
	if err != nil {
		return nil, fmt.Errorf("getting role from store: %w", err)
	}

	var role v1.RoleSpec

	if err := mapstructure.Decode(c.Spec, &role); err != nil {
		return nil, fmt.Errorf("decoding role: %w", err)
	}

	return &Role{Spec: &role}, nil
}

func (this *Role) SetExperiments(exp ...string) error {
	// Gracefully handle when no experiments or a single empty experiment is
	// passed, defaulting to allow all.
	switch len(exp) {
	case 0:
		exp = []string{"*"}
	case 1:
		if string(exp[0]) == "" {
			exp[0] = "*"
		}
	}

	for _, policy := range this.Spec.Policies {
		if policy.Experiments != nil {
			return fmt.Errorf("experiments already exist for policy")
		}

		for _, e := range exp {
			// Checking to make sure pattern given in 'e' is valid. Thus, the string
			// provided to match it against is useless.
			if _, err := filepath.Match(e, "useless"); err != nil {
				continue
			}

			policy.Experiments = append(policy.Experiments, e)
		}
	}

	return nil
}

func (this *Role) SetResourceNames(names ...string) error {
	// Gracefully handle when no names or a single empty name is passed,
	// defaulting to allow all.
	switch len(names) {
	case 0:
		names = []string{"*"}
	case 1:
		if names[0] == "" {
			names[0] = "*"
		}
	}

	for _, policy := range this.Spec.Policies {
		if policy.ResourceNames != nil {
			return fmt.Errorf("resource names already exist for policy")
		}

		for _, name := range names {
			tokens := strings.Split(name, "/")

			// A resource name can be prefixed with the resource type it applies to
			// (ie. vms/* or users/*). If that's the case, only add it to this policy
			// if the policy is for said resources.

			if len(tokens) > 1 {
				var included bool

				for _, r := range policy.Resources {
					if strings.HasPrefix(r, tokens[0]) {
						name = tokens[1]
						included = true
						break
					}
				}

				if !included {
					continue
				}
			}

			// Checking to make sure pattern given in 'name' is valid. Thus, the
			// string provided to match it against is useless.
			if _, err := filepath.Match(name, "useless"); err != nil {
				continue
			}

			policy.ResourceNames = append(policy.ResourceNames, name)
		}
	}

	return nil
}

func (this *Role) AddPolicy(r, e, v, names []string) {
	policy := &v1.PolicySpec{
		Experiments:   e,
		Resources:     r,
		ResourceNames: names,
		Verbs:         v,
	}

	this.Spec.Policies = append(this.Spec.Policies, policy)
}

func (this Role) Allowed(resource, exp, verb string, names ...string) bool {
	// Access is allowed if *any* policy included in this role allows access. As
	// such, we need to be sure to check *all* policies before denying access.

	for _, policy := range this.policiesForResource(resource) {
		if !policy.experimentAllowed(exp) {
			continue
		}

		if !policy.verbAllowed(verb) {
			continue
		}

		if len(names) == 0 {
			return true
		}

		for _, n := range names {
			if policy.resourceNameAllowed(n) {
				return true
			}
		}
	}

	return false
}

func (this Role) policiesForResource(resource string) []Policy {
	if err := this.mapPolicies(); err != nil {
		return nil
	}

	var policies []Policy

	for r, p := range this.mappedPolicies {
		if matched, _ := filepath.Match(r, resource); matched {
			policies = append(policies, p...)
			continue
		}
	}

	return policies
}

func (this *Role) mapPolicies() error {
	if this.mappedPolicies != nil {
		return nil
	}

	this.mappedPolicies = make(map[string][]Policy)

	var invalid []string

	for _, policy := range this.Spec.Policies {
		for _, resource := range policy.Resources {
			// Checking to make sure pattern given in 'resource' is valid. Thus, the
			// string provided to match it against is useless.
			if _, err := filepath.Match(resource, "useless"); err != nil {
				invalid = append(invalid, resource)
				continue
			}

			mapped := this.mappedPolicies[resource]
			mapped = append(mapped, Policy{Spec: policy})
			this.mappedPolicies[resource] = mapped
		}
	}

	if len(invalid) != 0 {
		return errors.New("invalid resource(s): " + strings.Join(invalid, ", "))
	}

	return nil
}
