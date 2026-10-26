package openapi

import (
	"sort"

	"github.com/bluebeard63/bosun"
)

// Options configures the generated document: its info block, servers and
// security schemes. The default has title "bosun API", version "1.0.0" and
// no servers or schemes. Override it before Start:
//
//	registry.RegisterInstance[*openapi.Options](app.Reg, &openapi.Options{
//	    Title:   "Notes API",
//	    Version: "2.1.0",
//	    Servers: []openapi.Server{{URL: "https://api.example.com"}},
//	    SecuritySchemes: map[string]openapi.SecurityScheme{
//	        "bearer": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
//	    },
//	})
//
// Routes that carry permissions (bosun.MetaPermissions) list a security
// requirement for each scheme in PermissionSchemes (all schemes when it is
// empty), as alternatives. For oauth2 and openIdConnect schemes the
// requirement's scopes are the route's permissions; OpenAPI 3.0 allows no
// scopes for the other types, so their requirement is empty. Every
// operation with permissions also lists them under x-permissions.
type Options struct {
	Title       string // info.title; "" means "bosun API"
	Version     string // info.version; "" means "1.0.0"
	Description string // info.description, CommonMark

	Servers []Server

	// SecuritySchemes become components.securitySchemes, keyed by name.
	SecuritySchemes map[string]SecurityScheme

	// PermissionSchemes names the schemes a route with permissions
	// requires. Empty means every scheme in SecuritySchemes.
	PermissionSchemes []string
}

var _ = bosun.Default[*Options](func() *Options { return &Options{} })

// Server is one entry of the document's servers list.
type Server struct {
	URL         string
	Description string
}

// SecurityScheme is an OpenAPI 3.0 security scheme object. Set the fields
// that apply to Type: "http" (Scheme, BearerFormat), "apiKey" (In, Name),
// "oauth2" (Flows) or "openIdConnect" (OpenIDConnectURL).
type SecurityScheme struct {
	Type         string
	Description  string
	Scheme       string // http: "bearer", "basic", ...
	BearerFormat string // http bearer: a hint such as "JWT"
	In           string // apiKey: "header", "query" or "cookie"
	Name         string // apiKey: the header, query or cookie name

	// Flows holds the oauth2 flows. Permissions used by routes are added
	// to each flow's Scopes, so you only need to describe the ones you want
	// described.
	Flows            OAuthFlows
	OpenIDConnectURL string
}

// OAuthFlows lists the oauth2 flows a scheme supports; nil ones are left
// out.
type OAuthFlows struct {
	Implicit          *OAuthFlow
	Password          *OAuthFlow
	ClientCredentials *OAuthFlow
	AuthorizationCode *OAuthFlow
}

// OAuthFlow is one oauth2 flow. Scopes maps scope names to descriptions.
type OAuthFlow struct {
	AuthorizationURL string
	TokenURL         string
	RefreshURL       string
	Scopes           map[string]string
}

// usesScopes reports whether a security requirement for this scheme may
// list scopes (OpenAPI 3.0.3, Security Requirement Object).
func (s SecurityScheme) usesScopes() bool {
	return s.Type == "oauth2" || s.Type == "openIdConnect"
}

func (s SecurityScheme) object(perms []string) map[string]any {
	out := map[string]any{"type": s.Type}
	set := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	set("description", s.Description)
	set("scheme", s.Scheme)
	set("bearerFormat", s.BearerFormat)
	set("in", s.In)
	set("name", s.Name)
	set("openIdConnectUrl", s.OpenIDConnectURL)
	if s.Type == "oauth2" {
		flows := map[string]any{}
		for name, f := range map[string]*OAuthFlow{
			"implicit":          s.Flows.Implicit,
			"password":          s.Flows.Password,
			"clientCredentials": s.Flows.ClientCredentials,
			"authorizationCode": s.Flows.AuthorizationCode,
		} {
			if f == nil {
				continue
			}
			scopes := map[string]string{}
			for _, p := range perms {
				scopes[p] = ""
			}
			for k, v := range f.Scopes {
				scopes[k] = v
			}
			flow := map[string]any{"scopes": scopes}
			if f.AuthorizationURL != "" {
				flow["authorizationUrl"] = f.AuthorizationURL
			}
			if f.TokenURL != "" {
				flow["tokenUrl"] = f.TokenURL
			}
			if f.RefreshURL != "" {
				flow["refreshUrl"] = f.RefreshURL
			}
			flows[name] = flow
		}
		out["flows"] = flows
	}
	return out
}

// info returns the document's info object.
func (o *Options) info() map[string]any {
	title, version := "bosun API", "1.0.0"
	if o != nil && o.Title != "" {
		title = o.Title
	}
	if o != nil && o.Version != "" {
		version = o.Version
	}
	info := map[string]any{"title": title, "version": version}
	if o != nil && o.Description != "" {
		info["description"] = o.Description
	}
	return info
}

// servers returns the servers list, or nil when none are configured.
func (o *Options) servers() []any {
	if o == nil {
		return nil
	}
	var out []any
	for _, s := range o.Servers {
		srv := map[string]any{"url": s.URL}
		if s.Description != "" {
			srv["description"] = s.Description
		}
		out = append(out, srv)
	}
	return out
}

// requiredSchemes returns the names of the schemes a route with
// permissions requires, sorted, skipping names that aren't configured.
func (o *Options) requiredSchemes() []string {
	if o == nil {
		return nil
	}
	var names []string
	if len(o.PermissionSchemes) == 0 {
		for name := range o.SecuritySchemes {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	for _, name := range o.PermissionSchemes {
		if _, ok := o.SecuritySchemes[name]; ok {
			names = append(names, name)
		}
	}
	return names
}

// security returns the operation's security requirements for perms: one
// alternative per required scheme. Nil when perms is empty or no scheme
// is configured.
func (o *Options) security(perms []string) []any {
	if len(perms) == 0 {
		return nil
	}
	var out []any
	for _, name := range o.requiredSchemes() {
		scopes := []string{}
		if o.SecuritySchemes[name].usesScopes() {
			scopes = perms
		}
		out = append(out, map[string]any{name: scopes})
	}
	return out
}

// securitySchemes returns components.securitySchemes, or nil when none are
// configured. perms is every permission used by a route, added to oauth2
// flow scopes.
func (o *Options) securitySchemes(perms []string) map[string]any {
	if o == nil || len(o.SecuritySchemes) == 0 {
		return nil
	}
	out := map[string]any{}
	for name, s := range o.SecuritySchemes {
		out[name] = s.object(perms)
	}
	return out
}

// permissions returns the route's bosun.MetaPermissions, de-duplicated in
// first-seen order.
func permissions(rt bosun.RouteInfo) []string {
	ps, _ := rt.Meta[bosun.MetaPermissions].([]string)
	var out []string
	seen := map[string]bool{}
	for _, p := range ps {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
