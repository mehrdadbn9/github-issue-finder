# Issue Assignment Workflow

Unreviewed scripts were previously committing and assigning issues on remote
projects without confirmation. To prevent accidental changes, the automation is
now disabled. Follow the steps below when you need to assign an issue to
yourself or teammates:

1. **Identify the issue** using the scoring output from the application or the
   `filter_and_assign.py` report. Confirm the issue is still open and unassigned.
2. **Discuss intent** with maintainers (comment on the issue or join the
   project chat) before requesting assignment.
3. **Use the GitHub UI** or a reviewed workflow (for example, a GitHub Action
   that routes through required reviewers) to request the assignment. Avoid
   personal access tokens stored in scripts.
4. **Confirm assignment** via the issue page. If the maintainer declines, drop
   the request and update your records.
5. **Clean up credentials**: store tokens in a secrets manager or `.env` file
   that is excluded from version control. Rotate any tokens that were exposed in
   previous scripts.

If you need automation in the future, keep it scoped:

- Use short-lived tokens and repository-scoped permissions.
- Require manual approval (for example, via pull request checks) before an
  action can assign issues.
- Log every assignment request with timestamp and the initiating user for audit
  purposes.

These safeguards ensure we protect upstream communities while still speeding up
our contribution workflow.
