// Command lwprobe is an external backend.
//
// It exists to answer one question that no test inside this module can answer
// honestly: can an application that knows nothing about how LIGHTWEIGHT is built
// consume it in production?
//
// Everything in internal/ has access to the answer. A handler test can build a
// principal, a resolver test can reach a provider, and an integration test can
// read the database to check what happened. Each of those proves a component
// works. None of them proves the PUBLIC CONTRACT works, because none of them is
// restricted to it.
//
// So this program is restricted to it, structurally rather than by discipline:
//
//   - It imports nothing from this module. Not one package.
//     TestLwprobe_ImportsNothingInternal fails the build if that changes.
//   - It speaks HTTP and JSON, over the documented routes, with the documented
//     headers.
//   - The client type holds exactly the three values a consumer is promised it
//     needs — base URL, workspace id, API key — and there is nowhere else for a
//     Keycloak URL, a realm name or a connection id to hide.
//
// # Three modes
//
//	-mode contract  the M2M contract: the happy path, then the error matrix
//	-mode bench     representative HTTP latency and throughput
//	-mode negative  the negative authorization matrix (Slice 14 / KI-018)
//
// contract and bench are driven by scripts/m2m-harness.sh; negative is driven by
// scripts/negative-authz-e2e.sh. Both scripts do the operator-side setup
// (realms, workspaces, connections, projects, credentials) that a real installer
// would do once, by hand or through the console — an attacker's-eye program must
// not be able to perform operator actions, and this one structurally cannot.
//
// The negative mode runs in two phases against ONE long-lived process:
//
//	-phase warm    the credentials about to be cut off currently work
//	-phase matrix  after the operator has archived, retired and revoked
//
// which is what makes "the transition took effect with no restart" provable
// rather than asserted.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	mode := flag.String("mode", "contract", "contract | bench | negative")
	phase := flag.String("phase", "", "negative mode only: warm | matrix")
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration: "+err.Error())
		os.Exit(2)
	}
	if *phase != "" {
		cfg.NegativePhase = *phase
	}

	switch *mode {
	case "contract":
		os.Exit(runContract(cfg))
	case "bench":
		os.Exit(runBench(cfg))
	case "negative":
		os.Exit(runNegative(cfg))
	default:
		fmt.Fprintf(os.Stderr, "unknown -mode %q (want contract, bench or negative)\n", *mode)
		os.Exit(2)
	}
}
