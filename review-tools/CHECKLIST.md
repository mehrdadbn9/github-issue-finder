## Daily Code Review Checklist

### Morning (10 minutes)
- [ ] Run `go run review_finder.go`
- [ ] Pick 2-3 PRs with 0 comments and small changes
- [ ] Review code for:
  - Logic errors
  - Security issues
  - Code style consistency
  - Documentation gaps
  - Test coverage

### Best PRs for Reviews:
1. **Documentation PRs** - Easy to review
2. **Size/XS or Size/S PRs** - Small code changes
3. **"needs-triage" labeled** - No one has reviewed yet
4. **Dependency updates** - Quick security checks

### What to Comment On:

**Constructive Comments:**
```
@author Great PR! A few suggestions:

1. Consider adding error handling for edge case X
2. This variable name could be more descriptive
3. Documentation is missing for this function

LGTM overall! 👍
```

**Approval:**
- If code looks good: Add "LGTM" comment
- If has issues: List them constructively
- Always be respectful and helpful

### Evening (15 minutes)
- [ ] Follow up on your PR comments
- [ ] Reply to any discussions on your reviews
- [ ] Update your review if needed based on author feedback

### Weekly Goals:
- Review at least 10 PRs per week
- Comment on at least 5 issues
- Submit 1 PR yourself (even small improvements)

### Monthly Goals:
- Review 40+ PRs
- Comment on 20+ issues
- Submit 2-3 PRs
- Join project discussions