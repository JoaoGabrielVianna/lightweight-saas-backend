package docs

import "embed"

// MarkdownFS is the embedded markdown corpus surfaced by the in-app docs
// viewer (web/admin/static/js/views/docs.js).
//
// Why embed instead of serving from disk:
//
//   - The Dockerfile only copies web/ into the runtime image, so a
//     filesystem path like docs/ is not present at runtime in containers.
//   - A symlink under web/admin/static/docs → ../../../docs works on the
//     host but dangles inside the container (because the target /app/docs
//     does not exist) AND is materialised as a 13-byte text file on
//     Windows clones that don't have core.symlinks enabled.
//   - go:embed sidesteps both — the markdown travels with the binary on
//     every platform, with no filesystem assumptions at runtime.
//
// Scope of the embed: every .md file under the directories we expose in
// the viewer, plus the top-level entry points. docs/evidence/ is
// intentionally excluded (~6.5 MB of screenshots and JSON dumps that are
// not user-facing reading material and would bloat the binary).
//
// Adding a new doc category is two steps: drop the .md file into one of
// the listed directories, and — if it's a brand-new directory — add a
// matching //go:embed line below.

//go:embed *.md
//go:embed getting-started/*.md
//go:embed architecture/*.md
//go:embed operations/*.md
//go:embed security/*.md
//go:embed audit/*.md
//go:embed release/*.md
//go:embed validation/*.md
//go:embed roadmap/*.md
//go:embed ui/*.md
//go:embed archive/*.md
var MarkdownFS embed.FS
