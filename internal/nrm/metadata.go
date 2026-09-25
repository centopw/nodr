package nrm

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/centopw/nodr/internal/diag"
)

// ReservedPrefix is the key prefix of labels and annotations that nodr
// itself defines.
const ReservedPrefix = "nodr/"

// Labels and annotations defined by nodr.
const (
	// LabelEnvironment groups resources into environments such as prod or
	// lab.
	LabelEnvironment = "nodr/environment"
	// AnnotationDescription is a free-text description of a resource.
	AnnotationDescription = "nodr/description"
	// AnnotationBindingPrefix starts annotations that bind one aspect of a
	// resource to an engine, for example nodr/binding.provision.
	AnnotationBindingPrefix = "nodr/binding."
)

const (
	maxNameLength   = 63
	maxPrefixLength = 253
)

var (
	nameRE          = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	qualifiedNameRE = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	dnsSubdomainRE  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	labelValueRE    = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`)
)

// ValidateName checks that name is a valid resource name: lowercase letters,
// digits and hyphens, starting and ending with a letter or digit, at most 63
// characters.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("must not be empty")
	case len(name) > maxNameLength:
		return fmt.Errorf("must be at most %d characters", maxNameLength)
	case !nameRE.MatchString(name):
		return errors.New("must contain only lowercase letters, digits and hyphens, and start and end with a letter or digit")
	}
	return nil
}

// NewUID returns a new resource UID. UIDs are ULIDs: unique, sortable by
// creation time, and stable when a resource is renamed.
func NewUID() string {
	return ulid.Make().String()
}

func validateMetadata(d *Document, diags *diag.List) {
	md := d.Metadata
	if err := ValidateName(md.Name); err != nil {
		diags.Errorf(d.File, d.LineOf("metadata", "name"), "metadata.name", "%v", err)
	}
	if md.UID != "" {
		if _, err := ulid.ParseStrict(md.UID); err != nil {
			diags.Errorf(d.File, d.LineOf("metadata", "uid"), "metadata.uid", "must be a ULID assigned by nodr: %v", err)
		}
	}
	for _, k := range sortedKeys(md.Labels) {
		path := "metadata.labels." + k
		line := d.LineOf("metadata", "labels", k)
		if err := validateKey(k); err != nil {
			diags.Errorf(d.File, line, path, "invalid key: %v", err)
		} else if strings.HasPrefix(k, ReservedPrefix) && k != LabelEnvironment {
			diags.Errorf(d.File, line, path, "the %s prefix is reserved; the only label nodr defines is %s", ReservedPrefix, LabelEnvironment)
		}
		if v := md.Labels[k]; len(v) > maxNameLength || !labelValueRE.MatchString(v) {
			diags.Errorf(d.File, line, path, "invalid value %q: use at most %d letters, digits, '-', '_' or '.', starting and ending with a letter or digit", v, maxNameLength)
		}
	}
	for _, k := range sortedKeys(md.Annotations) {
		path := "metadata.annotations." + k
		line := d.LineOf("metadata", "annotations", k)
		if err := validateKey(k); err != nil {
			diags.Errorf(d.File, line, path, "invalid key: %v", err)
		} else if strings.HasPrefix(k, ReservedPrefix) && !knownAnnotation(k) {
			diags.Errorf(d.File, line, path, "the %s prefix is reserved; nodr defines %s and %s<aspect>", ReservedPrefix, AnnotationDescription, AnnotationBindingPrefix)
		}
	}
}

func knownAnnotation(k string) bool {
	if k == AnnotationDescription {
		return true
	}
	aspect, ok := strings.CutPrefix(k, AnnotationBindingPrefix)
	return ok && nameRE.MatchString(aspect)
}

// validateKey checks a label or annotation key: an optional DNS subdomain
// prefix followed by a slash, and a name.
func validateKey(k string) error {
	prefix, name, hasPrefix := strings.Cut(k, "/")
	if !hasPrefix {
		name, prefix = prefix, ""
	}
	if hasPrefix && (prefix == "" || len(prefix) > maxPrefixLength || !dnsSubdomainRE.MatchString(prefix)) {
		return fmt.Errorf("prefix %q must be a lowercase DNS subdomain", prefix)
	}
	if name == "" || len(name) > maxNameLength || !qualifiedNameRE.MatchString(name) {
		return fmt.Errorf("name %q must be at most %d letters, digits, '-', '_' or '.', starting and ending with a letter or digit", name, maxNameLength)
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
