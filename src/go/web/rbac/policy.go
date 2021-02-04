package rbac

import (
	"path/filepath"
	"strings"

	v1 "phenix/types/version/v1"
)

type Policy struct {
	Spec *v1.PolicySpec
}

func (this Policy) experimentAllowed(exp string) bool {
	// If other code passes in an empty string for the experiment, it's meant to
	// represent a resource that isn't scoped by an experiment. If a (nefarious)
	// user passes in an empty string, then they won't be able to act on any
	// experiments at all. Either way, allowing an empty experiment should not be
	// a security issue.
	if exp == "" {
		return true
	}

	// Will default to false. If an experiment name was provided, but this policy
	// isn't scoped by experiments, then this function will return false.
	var allowed bool

	for _, e := range this.Spec.Experiments {
		negate := strings.HasPrefix(e, "!")
		e = strings.Replace(e, "!", "", 1)

		if matched, _ := filepath.Match(e, exp); matched {
			if negate {
				return false
			}

			allowed = true
		}
	}

	return allowed
}

func (this Policy) resourceNameAllowed(name string) bool {
	var allowed bool

	for _, n := range this.Spec.ResourceNames {
		negate := strings.HasPrefix(n, "!")
		n = strings.Replace(n, "!", "", 1)

		if matched, _ := filepath.Match(n, name); matched {
			if negate {
				return false
			}

			allowed = true
		}
	}

	return allowed
}

func (this Policy) verbAllowed(verb string) bool {
	for _, v := range this.Spec.Verbs {
		if v == "*" || v == verb {
			return true
		}
	}

	return false
}
