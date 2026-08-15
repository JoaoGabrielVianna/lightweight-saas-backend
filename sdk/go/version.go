package lightweight

import (
	"runtime/debug"
	"strings"
	"sync"
)

// userAgentProduct is the stable identifier this package sends.
//
// The PRODUCT half is a constant and the VERSION half is not, deliberately. A
// hard-coded version string is a lie the moment it is written: it says
// "lightweight-go/1.2.0" from every build, including the one someone is
// bisecting, and it has to be remembered at release time or it silently stops
// being true. The module's own recorded version cannot drift, because the
// toolchain writes it.
const userAgentProduct = "lightweight-go"

// devVersion is what is reported when there is no recorded module version:
// tests, `go run` inside this repository, and any build where this package is
// the main module. Saying so plainly is more useful to an operator reading an
// access log than inventing a number.
const devVersion = "dev"

var (
	userAgentOnce  sync.Once
	userAgentValue string
)

// defaultUserAgent returns `lightweight-go/<version>`.
//
// The version is read from the build's own dependency record, so a consumer that
// did `go get …/sdk/go@v0.1.0` reports v0.1.0 without anyone having edited a
// constant. Computed once: debug.ReadBuildInfo walks the whole module graph, and
// doing that per client construction would be a measurable cost for a value that
// cannot change within a process.
func defaultUserAgent() string {
	userAgentOnce.Do(func() {
		userAgentValue = userAgentProduct + "/" + sdkVersion()
	})
	return userAgentValue
}

// Version reports the version of this SDK, as recorded by the Go toolchain.
//
// Returns "dev" when there is no recorded version — which is the case whenever
// this module is the main module rather than a dependency, so a test binary and
// a locally-replaced build both report "dev" rather than a version that was
// never released.
func Version() string { return sdkVersion() }

// modulePath is this module's import path; the build record is searched for it.
const modulePath = "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"

func sdkVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return devVersion
	}
	for _, dep := range info.Deps {
		if dep.Path != modulePath {
			continue
		}
		// A replaced module reports the replacement's version, which is the
		// honest answer: that is the code actually running.
		if dep.Replace != nil {
			return normalizeVersion(dep.Replace.Version)
		}
		return normalizeVersion(dep.Version)
	}
	return devVersion
}

// normalizeVersion turns the toolchain's empty/placeholder values into
// devVersion, so the User-Agent never contains a bare "lightweight-go/" or the
// literal "(devel)" with its parentheses — neither of which is a legal token in
// a User-Agent header.
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "(devel)" {
		return devVersion
	}
	return v
}
