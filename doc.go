// Package brute reserves the module root for documentation and ad-hoc developer helpers.
//
// WHY: The executable lives under cmd/aagent, but `go test ./...` still visits the
// module root. Keeping a tiny buildable package here prevents ignored scratch
// files from making the root package fail during repository-wide test runs.
package brute
