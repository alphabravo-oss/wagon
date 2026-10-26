package store

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Registries and chart repositories named in a request are checked against
// the same address policy as Add File urls (HAULER_UI_FETCH_ALLOW_CIDRS)
// before a job is created. hauler connects to them, not Wagon.

// imageRegistry returns the registry part of an image reference, or "" when
// the reference has none and hauler uses Docker Hub. Like go-containerregistry,
// the first path part is the registry only when it contains '.' or ':'.
func imageRegistry(ref string) string {
	first, _, ok := strings.Cut(ref, "/")
	if !ok || !strings.ContainsAny(first, ".:") {
		return ""
	}
	return first
}

// checkRegistry checks a registry such as "registry.example.com:5000".
func (h *Handler) checkRegistry(ctx context.Context, field, registry string) error {
	if registry == "" {
		return nil
	}
	// go-containerregistry refuses a registry that is not a URI authority.
	u, err := url.Parse("//" + registry)
	if err != nil || u.Host != registry || u.Hostname() == "" {
		return fmt.Errorf("%s: %q is not a valid registry", field, registry)
	}
	return h.checkHost(ctx, field, u.Hostname())
}

// checkRepoURL checks the host of a chart repository URL such as
// "https://charts.example.com" or "oci://registry.example.com/charts", or of a
// chart name that is a URL. A value without a host, such as a repository or
// chart name, is left to hauler.
func (h *Handler) checkRepoURL(ctx context.Context, field, repoURL string) error {
	u, err := url.Parse(repoURL)
	if err != nil {
		return fmt.Errorf("%s: not a valid URL", field)
	}
	if u.Hostname() == "" {
		return nil
	}
	return h.checkHost(ctx, field, u.Hostname())
}

func (h *Handler) checkHost(ctx context.Context, field, host string) error {
	if err := h.Fetcher.CheckHost(ctx, host); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}
