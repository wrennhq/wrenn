package validate

import (
	"fmt"
	"regexp"
	"strings"
)

// slugRe matches team slugs: lowercase alphanumerics in dash-separated groups,
// starting and ending with an alphanumeric. 3–40 characters. This is stricter
// than SafeName because slugs appear in the "<slug>/<name>" template reference
// syntax and must never contain a slash, dot, or uppercase letter.
var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// reservedSlugs are names the platform keeps for itself. They cannot be chosen
// by a team. "platform" is the sentinel platform team; "wrenn" is the brand;
// "0" is the base36 form of the platform team ID and a common shorthand.
var reservedSlugs = map[string]bool{
	"platform": true,
	"wrenn":    true,
	"0":        true,
}

// SlugFormat validates only the shape of a slug: 3–40 characters of lowercase
// alphanumerics in dash-separated groups. It does NOT reject reserved words —
// it is used where an existing slug appears, such as the "<slug>/<name>"
// template reference, where "platform/<name>" is a legitimate reference.
func SlugFormat(slug string) error {
	if slug == "" {
		return fmt.Errorf("slug must not be empty")
	}
	if len(slug) < 3 || len(slug) > 40 {
		return fmt.Errorf("slug must be 3–40 characters")
	}
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("slug may only contain lowercase letters, numbers, and dashes (no leading, trailing, or repeated dashes)")
	}
	return nil
}

// TeamSlug validates a slug a team wants to adopt. It applies SlugFormat and
// additionally rejects reserved words. Callers should lower-case and TrimSpace
// first.
func TeamSlug(slug string) error {
	if err := SlugFormat(slug); err != nil {
		return err
	}
	if reservedSlugs[slug] {
		return fmt.Errorf("slug %q is reserved", slug)
	}
	return nil
}

// TemplateRef splits a template reference into an optional team slug and a
// template name. Accepted forms are "<name>" (own team, then platform) and
// "<slug>/<name>" (explicit owning team). It validates both components and
// returns an error for empty parts, extra slashes, or invalid characters.
//
// A returned empty slug means no slug was supplied.
func TemplateRef(ref string) (slug, name string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("template reference must not be empty")
	}

	switch strings.Count(ref, "/") {
	case 0:
		if err := SafeName(ref); err != nil {
			return "", "", err
		}
		return "", ref, nil
	case 1:
		slug, name, _ = strings.Cut(ref, "/")
		if err := SlugFormat(slug); err != nil {
			return "", "", fmt.Errorf("invalid team slug in reference: %w", err)
		}
		if err := SafeName(name); err != nil {
			return "", "", fmt.Errorf("invalid template name in reference: %w", err)
		}
		return slug, name, nil
	default:
		return "", "", fmt.Errorf("template reference may contain at most one '/'")
	}
}
