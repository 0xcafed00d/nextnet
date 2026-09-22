package version

// Version is replaced by -ldflags by the project build scripts. Direct builds
// that bypass those scripts retain the development fallback.
var Version = "dev"

func String() string {
	return Version
}
