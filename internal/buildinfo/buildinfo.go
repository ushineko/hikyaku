/*
Package buildinfo carries the version stamped in at link time.
*/
package buildinfo

// version is set by the Makefile with -ldflags -X; a plain `go build` says dev.
var version = "dev" //nolint:gochecknoglobals // set by the linker

// Version returns the version this binary was built as.
func Version() string { return version }
