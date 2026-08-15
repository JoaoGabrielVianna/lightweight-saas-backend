"""apply-mutation.py — apply one text mutation to one file, or fail loudly.

Separate from the shell script that drives it for a mundane but decisive
reason: the mutations are Python fragments, the driver is a bash script, and a
fragment containing a heredoc terminator would silently truncate the script
carrying it. Keeping the fragments in their own files removes that class of
bug entirely.

A fragment that matches nothing is an ERROR, not a no-op. A mutation that did
not apply proves nothing, and reporting it as caught is exactly the false
confidence this tooling exists to prevent.
"""

import re  # noqa: F401  (fragments may use it)
import sys

target, fragment_path = sys.argv[1], sys.argv[2]

src = open(target).read()
before = src

exec(compile(open(fragment_path).read(), fragment_path, "exec"))  # noqa: S102

if src == before:
    sys.stderr.write("mutation %s matched nothing in %s\n" % (fragment_path, target))
    sys.exit(3)

open(target, "w").write(src)
