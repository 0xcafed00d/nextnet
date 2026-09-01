package version

// Version is replaced by -ldflags for release builds.
var Version = "dev"

func String() string {
	return Version
}
