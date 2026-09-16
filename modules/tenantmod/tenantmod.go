// Package tenantmod adds multi-tenancy to a Bosun app. A middleware resolves the
// current tenant from each request and puts it on the context; a scoped repo
// decorator confines every read and write to that tenant; and a carrier
// propagates the tenant across the event bus. Tenant scoping is advisory: it
// works through repomod.Repo[T], so code that reaches a raw *gorm.DB bypasses it.
//
//	var _ = bosun.Controller[API]("/api", bosun.Use[tenantmod.Middleware]())
//	// in main, scope a repo to a tenant column:
//	tenantmod.Bind[User, gormrepomod.GormRepo[User]](app.Reg, "tenant_id")
package tenantmod

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/bluebeard63/bosun/modules/repomod"
	"github.com/bluebeard63/bosun/registry"
)

// Header is the default request header carrying the tenant id.
const Header = "X-Tenant-ID"

// ErrNoTenant is returned by a scoped repo when no tenant is on the context.
var ErrNoTenant = errors.New("tenantmod: no tenant in context")

// Tenant identifies the current tenant.
type Tenant struct {
	ID string
}

// WithTenant returns a copy of ctx carrying the tenant.
func WithTenant(ctx context.Context, id string) context.Context {
	return bosun.WithValue(ctx, &Tenant{ID: id})
}

// FromContext returns the tenant on ctx, or false if none is set.
func FromContext(ctx context.Context) (*Tenant, bool) {
	t := bosun.Value[Tenant](ctx)
	if t == nil || t.ID == "" {
		return nil, false
	}
	return t, true
}

func tenantID(ctx context.Context) (string, error) {
	if t, ok := FromContext(ctx); ok {
		return t.ID, nil
	}
	return "", ErrNoTenant
}

// --- resolver + middleware ---

// Resolver extracts the tenant from a request. The default reads the
// X-Tenant-ID header; override it by registering your own Resolver (from a JWT
// claim or subdomain, for example).
type Resolver interface {
	Resolve(r *http.Request) (*Tenant, error)
}

// HeaderResolver reads the tenant from the X-Tenant-ID header.
type HeaderResolver struct{}

func (HeaderResolver) Resolve(r *http.Request) (*Tenant, error) {
	if id := r.Header.Get(Header); id != "" {
		return &Tenant{ID: id}, nil
	}
	return nil, nil
}

var (
	_ = bosun.Service[HeaderResolver]()
	_ = bosun.DefaultBind[Resolver, HeaderResolver]()
)

// Middleware resolves the tenant and attaches it to the request context.
type Middleware struct {
	Resolver Resolver // injected
}

func (m *Middleware) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, err := m.Resolver.Resolve(r)
		if err != nil {
			http.Error(w, "tenant resolution failed", http.StatusBadRequest)
			return
		}
		if t != nil && t.ID != "" {
			r = r.WithContext(bosun.WithValue(r.Context(), t))
		}
		next.ServeHTTP(w, r)
	})
}

var _ = bosun.Middleware[Middleware]()

// --- scoped repo ---

// Scope wraps a repo so every operation is confined to the tenant on the
// context: reads and queries filter by the tenant column, creates stamp it, and
// gets/deletes for another tenant behave as not-found.
func Scope[T any](inner repomod.Repo[T], column string) repomod.Repo[T] {
	return &scoped[T]{base: inner, column: column}
}

type scoped[T any] struct {
	base   repomod.Repo[T]
	column string
}

func (s *scoped[T]) Get(ctx context.Context, id any) (*T, error) {
	tid, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.base.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if got, ok := fieldValue(v, s.column); !ok || got != tid {
		return nil, repomod.ErrNotFound // belongs to another tenant
	}
	return v, nil
}

func (s *scoped[T]) Create(ctx context.Context, entity *T) error {
	tid, err := tenantID(ctx)
	if err != nil {
		return err
	}
	setField(entity, s.column, tid)
	return s.base.Create(ctx, entity)
}

func (s *scoped[T]) Update(ctx context.Context, entity *T) error {
	tid, err := tenantID(ctx)
	if err != nil {
		return err
	}
	setField(entity, s.column, tid)
	return s.base.Update(ctx, entity)
}

func (s *scoped[T]) Delete(ctx context.Context, id any) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err // not found or wrong tenant
	}
	return s.base.Delete(ctx, id)
}

func (s *scoped[T]) Query() repomod.Query[T] {
	return &scopedQuery[T]{base: s.base.Query(), column: s.column}
}

func (s *scoped[T]) Tx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.base.Tx(ctx, fn)
}

type scopedQuery[T any] struct {
	base   repomod.Query[T]
	column string
}

func (q *scopedQuery[T]) with(b repomod.Query[T]) repomod.Query[T] {
	return &scopedQuery[T]{base: b, column: q.column}
}

func (q *scopedQuery[T]) Where(query any, args ...any) repomod.Query[T] {
	return q.with(q.base.Where(query, args...))
}
func (q *scopedQuery[T]) Order(spec string) repomod.Query[T] { return q.with(q.base.Order(spec)) }
func (q *scopedQuery[T]) Limit(n int) repomod.Query[T]       { return q.with(q.base.Limit(n)) }
func (q *scopedQuery[T]) Offset(n int) repomod.Query[T]      { return q.with(q.base.Offset(n)) }

func (q *scopedQuery[T]) filtered(ctx context.Context) (repomod.Query[T], error) {
	tid, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	return q.base.Where(q.column+" = ?", tid), nil
}

func (q *scopedQuery[T]) One(ctx context.Context) (*T, error) {
	b, err := q.filtered(ctx)
	if err != nil {
		return nil, err
	}
	return b.One(ctx)
}
func (q *scopedQuery[T]) All(ctx context.Context) ([]T, error) {
	b, err := q.filtered(ctx)
	if err != nil {
		return nil, err
	}
	return b.All(ctx)
}
func (q *scopedQuery[T]) Count(ctx context.Context) (int64, error) {
	b, err := q.filtered(ctx)
	if err != nil {
		return 0, err
	}
	return b.Count(ctx)
}

// Bind registers a tenant-scoped repomod.Repo[T] backed by the concrete driver
// repo Impl (for example gormrepomod.GormRepo[T]), which must already be
// registered. It uses registry.Register so it wins over the driver's default
// binding. Call it in main after the driver's For[T].
func Bind[T any, Impl any](reg *registry.Registry, column string) {
	registry.Register[repomod.Repo[T]](reg, func(r *registry.Registry) (repomod.Repo[T], error) {
		impl, err := registry.Resolve[*Impl](r)
		if err != nil {
			return nil, err
		}
		base, ok := any(impl).(repomod.Repo[T])
		if !ok {
			return nil, errors.New("tenantmod: Impl does not implement repomod.Repo[T]")
		}
		return Scope[T](base, column), nil
	})
}

// --- bus carrier ---

type tenantCarrier struct{}

func (tenantCarrier) Inject(ctx context.Context, h map[string]string) {
	if t, ok := FromContext(ctx); ok {
		h[Header] = t.ID
	}
}
func (tenantCarrier) Extract(ctx context.Context, h map[string]string) context.Context {
	if id := h[Header]; id != "" {
		return WithTenant(ctx, id)
	}
	return ctx
}

var _ = eventmod.RegisterCarrier(tenantCarrier{})

// --- reflection helpers ---

func fieldFor(v reflect.Value, column string) reflect.Value {
	want := normColumn(column)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if col := gormColumn(sf); col != "" && col == column {
			return v.Field(i)
		}
		// Compare names ignoring underscores and case so "TenantID" matches
		// the column "tenant_id" (GORM keeps acronyms like ID together).
		if normColumn(sf.Name) == want {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

func normColumn(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

func setField(entity any, column, val string) {
	v := reflect.ValueOf(entity)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return
	}
	f := fieldFor(v.Elem(), column)
	if f.IsValid() && f.CanSet() && f.Kind() == reflect.String {
		f.SetString(val)
	}
}

func fieldValue(entity any, column string) (string, bool) {
	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	f := fieldFor(v, column)
	if f.IsValid() && f.Kind() == reflect.String {
		return f.String(), true
	}
	return "", false
}

func gormColumn(sf reflect.StructField) string {
	tag := sf.Tag.Get("gorm")
	for _, part := range strings.Split(tag, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), ":"); ok && strings.EqualFold(k, "column") {
			return v
		}
	}
	return ""
}

