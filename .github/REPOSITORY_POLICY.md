# Maintainer and release policy

These files are proposed GitHub settings. Committing them does not enable rules.
Apply and verify the settings when the repository's visibility or plan supports
them; public repositories support rulesets on GitHub Free.

`master-review.json` allows only `@mintyleaf` (GitHub user ID `17470408`) to bypass
the PR and CI requirements. The maintainer can push directly to `master`, with CI
running afterward. Other contributors must use PRs, pass the `test` and `windows` checks from
GitHub Actions, resolve review threads, and receive a current CODEOWNER approval.
`.github/CODEOWNERS` assigns every path to `@mintyleaf`. Repository/organization
administrators can edit the settings themselves; review those access grants
separately. No general admin-role bypass is granted by this policy.

`master-history.json` separately blocks force pushes, branch deletion, and merge
commits for everyone, including the maintainer. The PR bypass does not bypass
this ruleset. Squash or rebase contribution PRs.

After release authorization, create both rulesets through GitHub settings or the
REST API (use the returned IDs to update existing rulesets rather than creating
duplicates):

```sh
gh api --method POST repos/antgpu/connector/rulesets \
  -H 'X-GitHub-Api-Version: 2026-03-10' \
  --input .github/rulesets/master-review.json
gh api --method POST repos/antgpu/connector/rulesets \
  -H 'X-GitHub-Api-Version: 2026-03-10' \
  --input .github/rulesets/master-history.json
gh api repos/antgpu/connector/rules/branches/master
```

Verify both rulesets are active, the sole review bypass resolves to `@mintyleaf`,
and the `test` check's integration ID resolves to GitHub Actions (`15368`).
Verify a non-maintainer cannot push to `master` and a contribution PR requires
the expected approval/checks before treating the configuration as enforced.

Pushes do not publish connector updates. Publishing remains a separate manual
workflow with an explicit confirmation, successful checks, signed artifacts,
and the `connector-production` environment. Configure its allowed deployment
branch as `master`, required reviewer `@mintyleaf`, self-review allowed for solo
maintenance, and admin bypass disabled. Keep signing/publishing credentials in
that environment's secrets. The upload SSH key must use a forced command that
only accepts checksummed, immutable connector artifacts; it must not grant a
shell or forwarding. The server controls the artifact root. Organization admins
retain their current access and can change repository/environment protections. Enable the publishing workflow only for an approved
production rollout. This release confirmation remains required even for direct
maintainer pushes.

References: [repository rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository),
[REST rules API](https://docs.github.com/en/rest/repos/rules),
[deployment environments](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments).
