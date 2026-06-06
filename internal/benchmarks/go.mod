module github.com/trendvidia/protocompile/internal/benchmarks

go 1.25.6

require (
	github.com/jhump/protoreflect v1.14.1 // MUST NOT be updated to v1.15 or higher
	github.com/stretchr/testify v1.11.1
	github.com/trendvidia/protocompile v0.14.1
	google.golang.org/protobuf v1.36.11
)

require golang.org/x/sync v0.20.0

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/petermattis/goid v0.0.0-20260113132338-7c7de50cc741 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/tidwall/btree v1.8.1 // indirect
	golang.org/x/exp v0.0.0-20250911091902-df9299821621 // indirect
	golang.org/x/net v0.44.0 // indirect
	google.golang.org/genproto v0.0.0-20200526211855-cb27e3aa2013 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Internal sub-module pointing at the parent — the parent has no published
// version under github.com/trendvidia/protocompile. External consumers do
// not need a replace; this one is only because internal/benchmarks lives
// in the same repo as the parent module.
replace github.com/trendvidia/protocompile => ../..
