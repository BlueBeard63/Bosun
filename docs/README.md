# Bosun docs

Bosun is a dependency-light Go web framework built on the Go 1.22 `net/http` router. It gives you typed handlers, a dependency-injection registry, and a set of optional modules, without codegen or a heavy runtime. Start with the tutorial, then jump to the guide for whatever you are building.

## Start here

The [getting started](./getting-started.md) tutorial takes you from an empty directory to a running service with routing, injection, a database, middleware, and errors. The [API overview](./api-overview.md) is the reference for every public symbol.

## Core concepts

[Controllers](./controllers.md) own groups of routes. [Services](./services.md) hold your logic and are wired by dependency injection, and [service options](./service-options.md) define a configuration value once and inject it everywhere. [Middleware](./middleware.md) wraps handlers, and the [auth and permissions](./auth-and-permissions.md) guide builds the common authentication and role-check chain. [Typed handlers](./typed-handlers.md) explain request binding and response encoding, [errors](./errors.md) cover status mapping, and [convert](./convert.md) maps a model to a response DTO.

## Routing

[Route groups](./routing-groups.md) share a prefix and middleware across related routes, and [routing internals](./routing-internals.md) explain path syntax, wildcards, middleware ordering, and route inspection.

## Inputs

[Forms](./forms.md) covers URL-encoded and multipart fields, and [files](./files.md) covers uploads, streaming, and downloads.

## Data layer

The [repo module](./repo.md) provides a generic `Repo[T]` with a query builder and transactions. The [GORM](./database-gorm.md) and [sqlc](./database-sqlc.md) guides show the hand-rolled patterns for each.

## Operations

[Config and hot reload](./config.md) binds typed options to files, environment, and databases. [Testing](./testing.md) drives an app from a Go test with stubs.
