# gitforgery — Design & Architecture

Status: proposal (draft v1) · Repo: `sauryagur/gitforgery` (Go, Bubble Tea template)

## 1. Vision

`gitforgery` is a Go CLI + Terminal UI for **crafting git history**, not just filtering it:

1. **Forge (rewrite)** — apply a declarative *forgery recipe* to an existing repo's history:
   author/committer identities, dates, messages, per-commit or by rules.
2. **Fabricate (generate)** — synthesize a plausible history from nothing (demo repos,
   decoy/honeypot repos, migration-tool load tests).
3. **Audit (detect)** — scan a repo for the residue a rewrite or crafted history leaves behind.

Positioning: `git filter-repo` (the current standard) is a *content* filter —
strip blobs, paths, refs. `gitforgery` targets **identity/date/message fabrication**
with a repeatable recipe format, a preview-first workflow, and a TUI. The template's
Bubble Tea stack is the TUI; the core rewrite is plumbing, not spinners.

The repo today is the stock `bubbletea-app-template` (module
`github.com/charmbracelet/bubbletea-app-template`, bubbles v1.0.0 / bubbletea v1.3.10 /
lipgloss v1.1.0). Milestone M0 renames the module and replaces the demo app.

## 2. What "forgery" means here (honest framing)

Git's commit object is: `tree`, `parent(s)`, author, committer, optional
signature/encoding headers, message. Nothing in the object binds the author/committer
fields to a real-world identity — anyone can write any identity and any date.
Git *never validates* identities, and only the committer date is "now" by
convention, not by enforcement (`GIT_COMMITTER_DATE`, `GIT_AUTHOR_DATE`,
`git commit --date` are trusted inputs). That is the forge surface we automate.

Boundaries — what this tool does **not** and cannot do:

- **Signatures cannot be forged.** A `gpgsig` (OpenPGP/X.509/SSH) header signs the
  commit bytes; any rewrite invalidates it. The tool strips invalid signatures and may
  *re-sign* with the user's own key, which is just signing.
- **SHA-1 collisions are out of scope for *creating***: real chosen-prefix collisions
  cost ~$45k of compute (Karpman–Leurent–Peyrin, *SHA-1 is a Shambles*, USENIX 2020;
  the original 2017 SHAttered demonstration cost more). Git itself ships collision
  detection (sha1dc) and refuses to operate on colliding objects. We instead ship an
  **audit** mode that detects the conditions a collision attack needs.
- **Detection is always possible out-of-band.** Even a perfect object-level rewrite
  leaves: reflog entries (old SHAs), unreachable old objects until `gc --prune`,
  invalidated signatures, GitHub "Verified" badge absence, commit-graph state,
  committer-after-author-date anomalies. The tool documents these in `--help` and in
  `audit`, which reports them.

Legitimate uses: GDPR/identity scrubbing, demo repositories, honeypots for
supply-chain research, testing migration tools, teaching. The tool is not a fraud
instrument — see §11.

## 3. Core mechanics (verified facts)

### 3.1 Objects are content-addressed; rewriting cascades

A commit's SHA-1 (or SHA-256) is the hash of its serialized bytes, including parent
hashes. Changing author/date/message on one commit changes its hash and every
descendant's hash. Trees and blobs are untouched by metadata-only rewrites — only
commit objects are re-created. This is why a metadata-only rewrite is cheap: the same
tree/blob OIDs are reused.

### 3.2 The sanctioned rewrite transport: `fast-export` → transform → `fast-import`

From `git-fast-export(1)` (docs for git 2.51, which is current):

> You can use it … as a format that can be edited before being fed to
> `git fast-import` in order to do history rewrites (an ability relied on by tools
> like *git filter-repo*).

The stream is a line/verb format (`blob`, `commit`, `tag`, `reset`, …) containing
author/committer lines with full timestamps, so **date and identity rewriting is a
pure text transform on the stream**. Options we rely on (all verified in the man
page):

| Option | Purpose |
|---|---|
| `--show-original-ids` | emits `original-oid <sha>` per commit/blob → old→new mapping without marks |
| `--export-marks` / `--import-marks` | persistent mark table (old sha → `:mark`); enables incremental & mapping |
| `--reference-excluded-parents` | partial range (`main~10..main`) without rewriting ancestors — parents referenced by sha |
| `--no-data` | skip blobs entirely; only valid if target already has the objects (fast partial rewrites) |
| `--signed-commits=strip` | default for commits — signatures are invalid after any edit |
| `--signed-tags=abort` | default for tags — must be overridden (`strip`) for tag-bearing repos |
| `--reencode=yes/no/abort` | message `encoding` header handling; default `abort` on non-UTF-8 commits |
| `--use-done-feature` | well-formed `feature done` … `done` stream |
| `--anonymize` | git's own built-in anonymizer (bug-report use); a lower bound for our `audit`/`anonymize` later |

Known limitation (from the man page): fast-import cannot tag trees (e.g. linux.git) —
edge case we flag, not fix.

### 3.3 Re-signing

After import, `git fast-import` stores at most one signature per hash algorithm
(both old and new `gpgsig` stream forms exist). Re-signing is a separate, interactive
step (`git commit --amend -S`, `git tag -s`) because it may prompt for a passphrase —
the core rewrite stays non-interactive; `gitforgery re-sign` is a follow-up subcommand.

### 3.4 Library options (Go ecosystem)

| Approach | Pros | Cons |
|---|---|---|
| **git CLI pipeline** (chosen) | byte-for-byte faithful to git; handles SHA-256 repos natively; zero ODB divergence; git is a sensible dependency for a git tool | requires `git` ≥ 2.45 (for `--signed-commits`) |
| **go-git** (planning/inspection only) | pure Go; read commit metadata, resolve revs, build DAGs, compute preview hashes | no SHA-256 repo support (confirmed broken for go-git-using tools, e.g. gitui/Gitnuro); no sha1dc unless v5.4+ sha1cd default — use `plumbing/hash` default, never the native `sha1.New` fallback (go-git docs: only safe against trustworthy servers) |
| **libgit2/git2go** | faithful; sha1dc; sha256 support recent | CGO — worse builds/releases; little gain over the CLI for a CLI tool |

Decision: **git CLI pipeline for execution, go-git for inspection/planning.**
If "no git binary" ever becomes a requirement, the `stream` package (§5) is written
against our own parser so a pure-Go fast-import emitter can replace the CLI later
(big job; see Open Questions).

## 4. CLI surface

```
gitforgery plan   --recipe r.yaml [--repo .] [--range …]
    # Read-only: resolve range, match rules, print old→new hash table + diff stats.
    # Exit 0 with no changes if recipe is a no-op.

gitforgery apply  --recipe r.yaml [--repo .] [--to out.git | --in-place]
    [--backup refs/forgery/original] [--yes] [--force] [--signed-tags=strip]
    # Export → transform → import → map refs → backed-up update → verify.

gitforgery fabricate --recipe demo.yaml --out repo.git [--seed N]
    # Create a brand-new repo with synthetic history (fresh `git init`, fast-import).

gitforgery audit  [--repo .] [--full]
    # Forgery-indicator scan (§9). Read-only.

gitforgery re-sign [--repo .] [--gpg-key KEY] [--ssh-key PATH]
    # Re-sign commits/tags after an apply that stripped signatures. Interactive.

gitforgery tui   [--recipe r.yaml]
    # Interactive recipe editor + preview + apply (§8).
```

Bare `gitforgery` prints help; no hidden mutate-by-default behavior. Every mutating
command requires `--yes` or a confirming TUI keystroke unless `--force`.

## 5. Architecture

```
cmd/gitforgery/            # cobra root; thin
internal/recipe/           # YAML recipe: parse, validate, render docs
internal/plan/             # go-git reads: resolve range → DAG → match rules → Plan
internal/stream/           # fast-export/fast-import stream vocabulary
   lexer.go                # incremental line/AST reader (large blobs via headers)
   transform.go            # applies plan ops to commit/tag/author/committer lines
   emit.go                 # writes transformed stream (+ original-oid passthrough)
internal/exec/             # os/exec wrappers: export, import, fsck, rev-parse
internal/fab/              # synthetic-history generator (recipe → stream)
internal/audit/            # detection scans (§9)
internal/sign/             # re-sign (gpg/ssh) orchestration
internal/ui/               # Bubble Tea TUI (§8)
internal/gitx/             # small go-git helpers (ident parsing, preview hashing)
```

### 5.1 Data flow (`apply`)

```mermaid
flowchart LR
    A[source repo] -->|"git fast-export --show-original-ids --signed-commits=strip --signed-tags=strip --export-marks"| B[stream]
    B --> C[stream/lexer]
    C --> D[plan: rules matched per commit oid]
    D --> E[stream/transform]
    E --> F[stream/emit]
    F -->|"git fast-import (temp bare repo)"| G[new history]
    G --> H["ref mapping: original-oid/marks → new oids"]
    H --> I["update refs in place (--in-place) or leave in --to"]
    I --> J["verify: git fsck --full; per-commit tree-oid equality"]
    J --> K["backup refs/forgery/original + report"]
```

Preview (`plan`) reuses C + D and computes new commit hashes by re-encoding planned
commit objects in Go (trees unchanged → cheap; exact hash must match what
fast-import will produce, verified by tests). `plan` exits non-zero if the preview
hash disagrees with the post-import reality — a canary for stream-vocabulary drift.

### 5.2 Safety invariants (all modes)

1. Default output is a **new repo** (`--to`); `--in-place` requires explicit opt-in.
2. `--in-place` first creates `refs/forgery/original/<ref>` pointing at pre-rewrite tips.
3. Ref updating uses `git update-ref` per-ref with a recorded transaction log.
4. Dirty worktree → refuse unless `--force` (a rewrite invalidates any working tree
   against the old tips).
5. Post-verify: `git fsck --full` clean, and every rewritten commit's tree OID equals
   its source commit's tree OID for metadata-only recipes (content must be untouched).
6. `--range` partial rewrites use `--reference-excluded-parents`; excluded ancestors
   are **not** rewritten; the stream may then only be imported into a repo that has
   those ancestors (we import into a clone of the source, then update refs).

## 6. Recipe format (YAML)

```yaml
# recipe.yaml — identity/date/message forgery plan
range: "main~10..main"              # git rev-list range, or "all" (default: all reachable)
match:                              # ordered; first match wins per commit
  - when:
      author_email: "@oldcorp\\.com$"   # regex on author/committer fields
    set:
      author: "Ada Lovelace <ada@example.com>"
      committer: "inherit"          # mirror the rewritten author
      author_date: "2020-01-01T10:00:00+00:00"
      committer_date: "author"      # = author_date | "now" | RFC3339 | "first+4h"
      subject: "{{.Subject}}"       # text/template over {Subject,Body,Author,Committer,AuthorDate}
      body: "{{.Body}}"
  - when:
      sha: "a1b2c3d4"
    set:
      author_date: "prev+2h"        # relative to previous rewritten commit
options:
  strip_signatures: true            # always true while re_sign=false (they'd be invalid)
  re_sign: false
  preserve_date_order: true         # clamp so committer dates stay monotonic
  refs: ["refs/heads/*", "refs/tags/*", "refs/notes/*"]
  backup_ref: "refs/forgery/original"
```

Design notes:
- `when` selectors: `sha`, `author`, `author_email`, `committer`, `committer_email`,
  `subject`, `branch` (each regex, absent = match-all).
- Value grammar: literals, `inherit`, `author`, `now`, `first±dur`, `prev±dur`,
  templates. No eval — safety and determinism.
- `apply --dry-run` == `plan`; recipes are diffable, reviewable, CI-able.

## 7. Fabricate (synthetic history)

```yaml
# demo.yaml
out: ./demo.git
tree: ./scaffold/          # optional seed directory copied into every/root commit
commits: 120
time: { start: "2023-01-01", end: "2024-06-01", clustering: 0.8 }
authors:
  - { name: "Ada", email: "ada@x.io", weight: 60 }
  - { name: "Bob", email: "bob@x.io", weight: 40 }
branches:
  - { name: "feature/auth", from: "main", commits: 8, merge: true }
messages:
  style: conventional      # or custom templates with subject/body pools
seed: 42                   # deterministic output
```

Generator: schedules commit times (cluster + weekday/sleep modeling), picks authors
by weight, emits a fast-import stream, imports into `git init --bare`. Deterministic
under `seed`. This is the "contribution graph" and decoy/honeypot use case.

## 8. TUI (Bubble Tea)

Screens (model per screen, `ui/`):

1. **Repo/range** — repo picker `filepicker`; range as textinput validating via
   `git rev-parse`.
2. **Commit table** — `table`: sha, date, author, subject; row select → edit screen.
3. **Identity/date editor** — textinputs for name/email; date via textinput
   (RFC3339, validated live) + a small custom calendar grid. Note: bubbles has **no
   datepicker** component (it's an open feature request, bubbles#404) — the calendar
   is ~100 lines of pure view code; or use `bubbles` v2's `textinput` only.
4. **Preview** — side-by-side old→new table (lipgloss colors), changed-fields diff
   per commit; space toggles apply.
5. **Apply/verify** — `spinner` + `progress` during export/import/verify; result
   summary with backup-ref instructions.

The template pins bubbles **v1** (`github.com/charmbracelet/bubbles v1.0.0`) while
current docs describe v2 (`charm.land/bubbles/v2`, Bubble Tea v2, `tea.KeyPressMsg`).
M0 decides: stay on v1 paths (fine; v1 remains published) or migrate to v2 (churn now,
current API). Recommendation: migrate to v2 at M0 while the codebase is 20 lines.

## 9. Audit (forgery-indicator scan)

Read-only; each check independently reported, severity-ranked:

1. **Rewrite residue**: unreachable objects (`git fsck --unreachable` count) and
   reflog entries whose tips don't match current refs — the universal fingerprint of
   a history rewrite.
2. **Signature state**: any unsigned commits claiming identity, any *recently
   committed* commits whose gpgsig is stale/invalid.
3. **Date anomalies**: committer_date < author_date; committer dates far before
   object file mtimes; date clustering that matches generator heuristics (flag, low
   confidence).
4. **Collision hygiene**: confirm the local toolchain's SHA-1 is collision-detecting
   (git sha1dc; go-git sha1cd — detect the native `sha1.New` fallback in any vendored
   go-git and flag it); with `--full`, rescan the ODB re-hashing every object and
   report any hash shared by two distinct objects (should never legitimately happen).
5. **Exotic structure**: tags pointing at non-commits, `refs/notes` churn — cheap
   smells of manipulation.

## 10. Milestones

| M | Scope | Acceptance |
|---|---|---|
| M0 | Rename module → `github.com/sauryagur/gitforgery`; cobra root; `version`; remove spinner demo; (optional) migrate to bubbles v2 | `go build`, `go vet`, golangci clean; `gitforgery --version` |
| M1 | `recipe` + `plan`: open repo (go-git), resolve range, match rules, preview table | golden tests on fixture repos with known SHAs; `plan` matches hand-computed expectations |
| M2 | `stream` + `exec` + `apply --to` in temp repo; marks/original-oid mapping; `fsck` verify | end-to-end fixture test: rewrite author/dates, tree OIDs unchanged, new repo `fsck` clean |
| M3 | `--in-place` + backup refs; tags & annotated tags; partial ranges | backup restore works; annotated-tag mapping correct |
| M4 | TUI (editor, preview, confirm, progress) | scripted TUI test (bubbletea testing harness) applies a recipe |
| M5 | `fabricate` | deterministic under seed; output clones cleanly; fsck clean |
| M6 | `audit` + `re-sign` + SHA-256-repo handling + README + sample recipes | audit flags a deliberately forged fixture; sha256 repo gives clear behavior |
| M7 | Polish: goreleaser (already configured), docs site, `--no-data` fast path | release artifact builds |

## 11. Security & ethics posture

- The tool does not defeat git's integrity guarantees: it manufactures the *metadata*
  git deliberately leaves forgeable. We state this in `--help` and README.
- **Detectability is a feature**: `audit` exists precisely because rewritten history
  leaves fingerprints. The README will list them so users understand the limits.
- No exfiltration/network behavior; recipes are local files; `re-sign` only ever uses
  the user's own keys (gpg/ssh agent, no key material handling).
- Supply-chain hygiene: minimal deps (cobra, yaml.v3, go-git, bubbletea stack);
  dependabot already enabled on the template's workflows.

## 12. Open questions

1. **SHA-256 repos**: go-git can't read them; `plan` must detect `--object-format`
   and degrade to CLI-only (or refuse with a clear error). Confirm go-git status at
   M1.
2. **bubbles v1 vs v2** at M0 (see §8).
3. **Stream grammar drift**: fast-import stream vocabulary evolves (e.g. new
   `gpgsig <algo> <fmt>` lines in 2.45). Mitigation: `plan` preview-hash canary (§5.1)
   plus golden fixture tests pinned per git version.
4. **Annotated-tag and notes refs** mapping strategy (marks-based; verify fast-export
   covers `refs/notes` under `--all`).
5. Whether `fabricate` should also support grafting onto an existing root
   (`--parent`) — nice for "backdating an existing repo's founding commit" demos;
   defer to M5+.

## References

- `git-fast-export(1)` — https://git-scm.com/docs/git-fast-export (transport, options,
  `--signed-commits` default strip, marks, `--show-original-ids`)
- `git-fast-import(1)` — https://git-scm.com/docs/git-fast-import (stream grammar)
- git-filter-repo — https://github.com/newren/git-filter-repo (precedent; content filtering)
- go-git compatibility notes (collision-detecting hash default vs native sha1) —
  https://deepwiki.com/go-git/go-git/10-compatibility
- bubbles v2 upgrade guide (component inventory, no datepicker) —
  https://github.com/charmbracelet/bubbles/blob/master/UPGRADE_GUIDE_V2.md
- SHA-1 chosen-prefix collisions are practical (~$45k): "SHA-1 is a Shambles",
  Leurent/Peyrin, USENIX Security 2020 — https://sha-mbles.github.io/
- Git SHA-256 repos (since 2.29, experimental) — https://git-scm.com/docs/git-init