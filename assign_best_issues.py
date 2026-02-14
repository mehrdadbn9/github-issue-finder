#!/usr/bin/env python3
"""This script is intentionally disabled.

Previously this file attempted to auto-assign issues using a long-lived
personal access token. That behavior caused unexpected assignments in remote
projects and leaked credentials. The automation has been removed.

If you still need issue assignment helpers, build them on top of vetted
workflows (for example, GitHub Actions with review gates) and scoped tokens.
"""

from __future__ import annotations

import sys
import textwrap


def main() -> int:
    message = textwrap.dedent(
        """
        Issue auto-assignment has been disabled to protect upstream projects and
        credentials. Replace this script with an audited workflow that requires
        human confirmation before reassigning issues.
        """
    ).strip()

    sys.stderr.write(message + "\n")
    return 1


if __name__ == "__main__":
    sys.exit(main())
