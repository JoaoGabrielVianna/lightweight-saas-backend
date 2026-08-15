// The SDK is a SEPARATE MODULE, and that is the point rather than a packaging
// preference.
//
// A client library's central claim is "importing me costs you nothing you did
// not ask for". Inside the server module that claim is a convention: one
// autocomplete adds `internal/identityruntime`, one `go get` adds a transitive
// dependency, and nothing fails. Here both are structural. There is no `require`
// block, so the dependency claim is checked by the absence of a go.sum rather
// than by a reviewer noticing; and reaching back into the server would need a
// require line pointing at the parent module, which is not something anyone adds
// by accident.
//
// The go directive is BELOW the server's on purpose. The server chooses its own
// toolchain; a consumer of this package should not have to. 1.23 is the floor
// because Audit.All is a range-over-func iterator.
module github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go

go 1.23.0
